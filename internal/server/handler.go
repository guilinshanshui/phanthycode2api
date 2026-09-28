// Package server 暴露 OpenAI 兼容 HTTP 接口，内部驱动 pool 挑号 + upstream 转发。
package server

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"phanthycode2api/internal/admin"
	"phanthycode2api/internal/pool"
	"phanthycode2api/internal/upstream"
)

// maxBodyBytes 单次请求体上限。
// Codex 在大上下文下会发送数 MB 的会话历史，上限给足并显式报错，避免静默截断。
const maxBodyBytes = 32 << 20

// Config handler 依赖。
type Config struct {
	Pool         *pool.Pool
	Upstream     *upstream.Client
	APIKey       string                  // 空 = 不鉴权
	MaxRotate    int                     // 单请求最多换号次数，默认 2
	HardCooldown time.Duration           // 积分不足冷却，默认 12h
	SoftCooldown time.Duration           // 429 冷却，默认 60s
	ErrThreshold int                     // 连续其他错误冷却阈值，默认 3
	ErrCooldown  time.Duration           // 错误冷却时长，默认 10m
	Admin        *admin.Handler          // 可空；非空时启用 /admin 与分发密钥
	RefreshSkew  time.Duration           // token 提前刷新窗口，默认 10m
	Thinking     upstream.ThinkingOption // 扩展思考策略，见 upstream.ThinkingOption
}

// Handler 主路由。
type Handler struct {
	cfg Config
	mux *http.ServeMux
}

// NewHandler 构建 handler。
func NewHandler(cfg Config) *Handler {
	if cfg.MaxRotate <= 0 {
		// 单请求最多换号两次：上游超时类失败重试很少成功，
		// 换号次数过多只会把尾部等待时间成倍拉长。
		cfg.MaxRotate = 2
	}
	if cfg.HardCooldown <= 0 {
		cfg.HardCooldown = 12 * time.Hour
	}
	if cfg.SoftCooldown <= 0 {
		cfg.SoftCooldown = 60 * time.Second
	}
	if cfg.ErrThreshold <= 0 {
		cfg.ErrThreshold = 3
	}
	if cfg.ErrCooldown <= 0 {
		cfg.ErrCooldown = 10 * time.Minute
	}
	if cfg.RefreshSkew <= 0 {
		cfg.RefreshSkew = 10 * time.Minute
	}
	h := &Handler{cfg: cfg, mux: http.NewServeMux()}
	h.mux.HandleFunc("POST /v1/chat/completions", h.withAuth(h.chatCompletions))
	h.mux.HandleFunc("GET /v1/models", h.withAuth(h.models))
	h.mux.HandleFunc("GET /status", h.withAuth(h.status))
	h.mux.HandleFunc("GET /healthz", h.healthz)
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.cfg.Admin != nil && (r.URL.Path == "/admin" || strings.HasPrefix(r.URL.Path, "/admin/")) {
		h.cfg.Admin.ServeHTTP(w, r)
		return
	}
	h.mux.ServeHTTP(w, r)
}

func (h *Handler) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plain := ""
		authz := r.Header.Get("Authorization")
		if strings.HasPrefix(authz, "Bearer ") {
			plain = strings.TrimPrefix(authz, "Bearer ")
		}
		model, stream := peekRequest(r)
		if h.cfg.APIKey == "" && h.cfg.Admin == nil {
			next(w, r)
			return
		}
		if h.cfg.Admin != nil {
			if key, ok := h.cfg.Admin.LookupKey(plain, model); ok {
				h.serveWithLog(w, r, next, model, stream, key.ID, key.Name)
				return
			}
		}
		if h.cfg.APIKey != "" && subtle.ConstantTimeCompare([]byte(plain), []byte(h.cfg.APIKey)) == 1 {
			h.serveWithLog(w, r, next, model, stream, "", "")
			return
		}
		if h.cfg.APIKey == "" && (h.cfg.Admin == nil || h.cfg.Admin.LegacyUnauthenticated()) {
			h.serveWithLog(w, r, next, model, stream, "", "")
			return
		}
		writeOpenAIError(w, http.StatusUnauthorized, "invalid_api_key", "missing or invalid API key")
	}
}

// serveWithLog 记录 /v1/chat/completions 的模型、状态与耗时。
func (h *Handler) serveWithLog(w http.ResponseWriter, r *http.Request, next http.HandlerFunc, model string, stream bool, keyID, keyName string) {
	if h.cfg.Admin == nil || r.URL.Path != "/v1/chat/completions" {
		next(w, r)
		return
	}
	start := time.Now()
	recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	next(recorder, r)
	h.cfg.Admin.NoteRequest(admin.LogEntry{
		Time:      time.Now().Unix(),
		KeyID:     keyID,
		KeyName:   keyName,
		Model:     model,
		Status:    recorder.status,
		LatencyMs: time.Since(start).Milliseconds(),
		Stream:    stream,
		RemoteIP:  remoteIP(r),
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (w *statusRecorder) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusRecorder) Write(data []byte) (int, error) {
	return w.ResponseWriter.Write(data)
}

func (w *statusRecorder) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}
func (h *Handler) healthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"accounts": h.cfg.Pool.List(),
	})
}

// modelsCreated 是模型列表的固定创建时间（秒），保证响应稳定可缓存。
const modelsCreated = 1753600000

// modelEntries 由 upstream.CanonicalModels 生成，保证 /v1/models
// 与真实可调用的上游模型保持一致。
var modelEntries = buildModelEntries()

func buildModelEntries() []map[string]any {
	out := make([]map[string]any, 0, len(upstream.CanonicalModels))
	for _, m := range upstream.CanonicalModels {
		out = append(out, map[string]any{
			"id": m.ID, "object": "model", "created": modelsCreated,
			"owned_by": "phanthy", "context_length": m.Context,
		})
	}
	return out
}

func (h *Handler) models(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data":   modelEntries,
	})
}

func (h *Handler) chatCompletions(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request", "read body: "+err.Error())
		return
	}
	if len(body) == 0 {
		// 探测类空请求（部分启动器会这样探活）不必打扰上游。
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request", "empty request body")
		return
	}
	if len(body) > maxBodyBytes {
		writeOpenAIError(w, http.StatusRequestEntityTooLarge, "request_too_large",
			fmt.Sprintf("request body exceeds %d MB", maxBodyBytes>>20))
		return
	}

	// 探测是否流式
	var peek struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	_ = json.Unmarshal(body, &peek)

	tried := map[string]bool{}
	var lastErr error
	for i := 0; i < h.cfg.MaxRotate; i++ {
		acct := h.cfg.Pool.PickExcluding(tried)
		if acct == nil {
			break
		}
		tried[acct.UID] = true

		// token 临近过期 → 先 refresh（失败冷却换号）
		if acct.NeedsRefresh(h.cfg.RefreshSkew) {
			if err := h.cfg.Upstream.RefreshToken(acct); err != nil {
				lastErr = err
				var ue *upstream.Error
				if errors.As(err, &ue) && ue.Kind == upstream.ErrSessionDead {
					h.cfg.Pool.Disable(acct.UID, "refresh session dead")
				} else {
					h.cfg.Pool.Cooldown(acct.UID, pool.CoolErr, h.cfg.ErrCooldown, "refresh: "+err.Error())
				}
				continue
			}
			_ = acct.SaveAtomic()
		}

		// 确保 api_key 可用（失败不阻塞，ChatStream 会用 access_token 兜底）
		if acct.APIKey == "" {
			if err := h.cfg.Upstream.EnsureAPIKey(acct); err != nil {
				log.Printf("ensure_api_key uid=%s: %v", acct.UID, err)
			}
		}

		// 准备请求体（OpenAI → Anthropic 转换，并按配置下发 thinking 开关）
		anthroBody := upstream.PrepareBody(body, h.cfg.Thinking)

		rc, status, respBody, terr := h.cfg.Upstream.ChatStream(r.Context(), acct, anthroBody)
		if terr != nil {
			lastErr = terr
			h.cfg.Pool.NoteError(acct.UID, h.cfg.ErrThreshold, h.cfg.ErrCooldown)
			continue
		}
		if status >= 400 {
			kind := upstream.Classify(status, string(respBody))
			switch kind {
			case upstream.ErrHardCredit:
				h.cfg.Pool.Cooldown(acct.UID, pool.CoolHard, h.cfg.HardCooldown, "积分不足")
				lastErr = &upstream.Error{Kind: kind, Status: status, Msg: string(respBody)}
				continue
			case upstream.ErrSoftRate:
				h.cfg.Pool.Cooldown(acct.UID, pool.CoolSoft, h.cfg.SoftCooldown, "429 rate limit")
				lastErr = &upstream.Error{Kind: kind, Status: status, Msg: string(respBody)}
				continue
			case upstream.ErrSessionDead:
				h.cfg.Pool.Disable(acct.UID, "session dead")
				lastErr = &upstream.Error{Kind: kind, Status: status, Msg: string(respBody)}
				continue
			case upstream.ErrNotFound:
				h.cfg.Pool.Cooldown(acct.UID, pool.CoolSoft, h.cfg.SoftCooldown, "upstream 404")
				lastErr = &upstream.Error{Kind: kind, Status: status, Msg: string(respBody)}
				continue
			case upstream.ErrModelDenied:
				// 403 套餐不含该模型：请求侧问题，账号仍然健康。
				// 不冷却、不计错误，直接返回客户端，避免把可用账号误伤掉。
				writeOpenAIError(w, http.StatusBadRequest, "model_not_allowed",
					"model is not allowed for this plan: "+strings.TrimSpace(string(respBody)))
				return
			default:
				h.cfg.Pool.NoteError(acct.UID, h.cfg.ErrThreshold, h.cfg.ErrCooldown)
				lastErr = &upstream.Error{Kind: kind, Status: status, Msg: string(respBody)}
				continue
			}
		}
		defer rc.Close()
		h.cfg.Pool.NoteSuccess(acct.UID)

		if peek.Stream {
			if err := upstream.Stream(w, rc, peek.Model); err != nil {
				log.Printf("stream error: %v", err)
			}
			return
		}
		resp, err := upstream.Aggregate(rc, peek.Model)
		if err != nil {
			writeOpenAIError(w, http.StatusBadGateway, "upstream_parse", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}
	msg := "all accounts unavailable (cooling/disabled)"
	if lastErr != nil {
		msg += ": " + lastErr.Error()
	}
	writeOpenAIError(w, http.StatusServiceUnavailable, "no_healthy_account", msg)
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	raw, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}

func writeOpenAIError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"message": msg,
			"type":    "api_error",
			"code":    code,
		},
	})
}

func peekRequest(r *http.Request) (string, bool) {
	if r == nil || r.Body == nil {
		return "", false
	}
	// 一次性读完并缓存：只读开头一段会把大请求体截断成非法 JSON，
	// 而截断后的 JSON 正是上游 400「Request body must be JSON.」的来源。
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	r.Body = io.NopCloser(bytes.NewReader(body))
	if err != nil {
		return "", false
	}
	var payload struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	_ = json.Unmarshal(body, &payload)
	return payload.Model, payload.Stream
}
func remoteIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}
