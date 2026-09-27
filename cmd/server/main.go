// Package main phanthycode2api 入口：加载配置、构建 pool、起调度器与 HTTP 服务。
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"phanthycode2api/internal/admin"
	"phanthycode2api/internal/auth"
	"phanthycode2api/internal/pool"
	"phanthycode2api/internal/scheduler"
	"phanthycode2api/internal/server"
	"phanthycode2api/internal/upstream"
)

func main() {
	cfgPath := flag.String("config", "config.json", "path to config json")
	flag.Parse()

	baseDir, err := filepath.Abs(filepath.Dir(os.Args[0]))
	if err != nil {
		log.Fatalf("resolve app directory: %v", err)
	}
	if !filepath.IsAbs(*cfgPath) {
		candidate := filepath.Join(baseDir, *cfgPath)
		if _, statErr := os.Stat(candidate); statErr == nil {
			*cfgPath = candidate
		} else if _, cwdStatErr := os.Stat(*cfgPath); cwdStatErr != nil {
			*cfgPath = candidate
		}
	}

	cfg, err := Load(*cfgPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			log.Printf("config %s not found; create default config and run from app directory", *cfgPath)
			cfgPath, cfgErr := EnsureDefaultConfig(*cfgPath, baseDir)
			if cfgErr != nil {
				log.Fatalf("create default config: %v", cfgErr)
			}
			cfg, err = Load(cfgPath)
		}
		if err != nil {
			log.Fatalf("load config: %v", err)
		}
	}
	if err := MakeRelativePathsAbsolute(cfg, baseDir); err != nil {
		log.Fatalf("resolve config paths: %v", err)
	}

	auths, err := auth.LoadDir(cfg.AuthDir)
	if err != nil {
		log.Fatalf("load auths: %v", err)
	}
	log.Printf("loaded %d account(s) from %s", len(auths), cfg.AuthDir)

	p := pool.New(cfg.StateFile)
	for _, a := range auths {
		p.Add(a)
	}

	up := upstream.New(cfg.BaseURL)
	up.HTTP.Timeout = time.Duration(cfg.Upstream.TimeoutSeconds) * time.Second

	sch := scheduler.New(scheduler.Config{
		Pool:           p,
		Upstream:       up,
		KeepaliveHours: cfg.Schedule.KeepaliveHours,
	})

	if !cfg.Admin.Enabled && cfg.Admin.PasswordHash == "" {
		cfg.Admin.Enabled = true
		log.Print("admin password not configured; enabling /admin with default password admin123")
	}

	adminStore, err := admin.New(cfg.Admin.DataDir)
	if err != nil {
		log.Fatalf("load admin data: %v", err)
	}
	var adminHandler *admin.Handler
	if cfg.Admin.Enabled {
		adminStore.SetConfigState(admin.ConfigState{
			GetConfig:  func() map[string]any { return cfg.ToMap() },
			SaveConfig: func(raw map[string]any) error { return SaveConfigMap(*cfgPath, raw) },
		})
		adminHandler = admin.NewHandler(adminStore, *cfgPath, &cfg.Admin.PasswordHash)
		adminHandler.Verify = func(password string) bool {
			if cfg.Admin.PasswordHash == "" {
				return subtle.ConstantTimeCompare([]byte(password), []byte(defaultAdminPassword)) == 1
			}
			return admin.VerifyPassword(password, cfg.Admin.PasswordHash)
		}
		adminHandler.Accounts = &admin.AccountManager{
			StartOAuth: func() (map[string]any, error) { return StartOAuthLogin(cfg.BaseURL, cfg.AuthDir) },
			List: func() []map[string]any {
				out := []map[string]any{}
				for _, status := range p.List() {
					out = append(out, map[string]any{
						"uid": status.UID, "nickname": status.Nickname, "has_api_key": status.HasAPI,
						"cooling": status.Cooling, "until": status.Until, "reason": status.Reason,
						"disabled": status.Disabled, "err_count": status.ErrCount,
					})
				}
				return out
			},
			Add: func(req map[string]any) error {
				code, _ := req["code"].(string)
				verifier, _ := req["verifier"].(string)
				account, err := ExchangeAndSaveAccount(upstream.New(cfg.BaseURL), cfg.AuthDir, code, verifier)
				if err != nil {
					return err
				}
				p.Add(account)
				return nil
			},
			Delete:    func(uid string) error { return DeleteAccount(cfg.AuthDir, p, uid) },
			Refresh:   func(uid string) error { return RefreshAccount(upstream.New(cfg.BaseURL), p, uid) },
			Keepalive: func(uid string) error { return KeepaliveAccount(upstream.New(cfg.BaseURL), p, uid) },
		}
	}

	h := server.NewHandler(server.Config{
		Pool:         p,
		Upstream:     up,
		APIKey:       cfg.APIKey,
		HardCooldown: cfg.HardCreditDur,
		SoftCooldown: cfg.SoftRateDur,
		ErrThreshold: cfg.Cooldown.ErrThresh,
		ErrCooldown:  cfg.ErrCooldownDur,
		Admin:        adminHandler,
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go sch.Run(ctx)

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           h,
		ReadHeaderTimeout: 30 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	log.Printf("phanthycode2api listening on %s (api_key=%v, admin=%v)", cfg.Listen, cfg.APIKey != "", adminHandler != nil)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("http: %v", err)
	}
	log.Printf("bye")
}

const defaultAdminPassword = "admin123"

// EnsureDefaultConfig 在可执行文件目录创建开箱即用的 config.json。
func EnsureDefaultConfig(path, baseDir string) (string, error) {
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return "", err
	}
	cfg := Default()
	if err := MakeRelativePathsAbsolute(cfg, baseDir); err != nil {
		return "", err
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", err
	}
	raw = append(raw, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", err
	}
	return path, nil
}

// MakeRelativePathsAbsolute 让双击运行时始终读写 exe 旁边的 auths/ 与 data/。
func MakeRelativePathsAbsolute(cfg *Config, baseDir string) error {
	resolve := func(value string) (string, error) {
		if value == "" || filepath.IsAbs(value) {
			return value, nil
		}
		return filepath.Abs(filepath.Join(baseDir, value))
	}
	var err error
	if cfg.AuthDir, err = resolve(cfg.AuthDir); err != nil {
		return err
	}
	if cfg.StateFile, err = resolve(cfg.StateFile); err != nil {
		return err
	}
	if cfg.Admin.DataDir, err = resolve(cfg.Admin.DataDir); err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.AuthDir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(cfg.StateFile), 0o755); err != nil {
		return err
	}
	return os.MkdirAll(cfg.Admin.DataDir, 0o755)
}

// ToMap 导出当前配置，供管理端设置页展示。
func (c *Config) ToMap() map[string]any {
	raw, _ := json.Marshal(c)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	out["hard_credit_duration"] = c.HardCreditDur.String()
	out["soft_rate_duration"] = c.SoftRateDur.String()
	out["err_cooldown_duration"] = c.ErrCooldownDur.String()
	if adminConfig, ok := out["admin"].(map[string]any); ok {
		if adminConfig["password_hash"] != "" {
			adminConfig["password_hash"] = "***"
		}
	}
	return out
}

// SaveConfigMap 保存设置页提交的配置；listen 与 admin 保留原值，避免网页误改。
func SaveConfigMap(path string, raw map[string]any) error {
	existing := map[string]any{}
	if current, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(current, &existing)
	}
	out := map[string]any{}
	for key, value := range raw {
		switch key {
		case "listen", "admin", "hard_credit_duration", "soft_rate_duration", "err_cooldown_duration":
			continue
		}
		out[key] = value
	}
	if listen, ok := existing["listen"]; ok {
		out["listen"] = listen
	}
	if adminConfig, ok := existing["admin"]; ok {
		out["admin"] = adminConfig
	}
	encoded, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, encoded, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// StartOAuthLogin 生成 PKCE verifier 和授权链接；verifier 暂存到账号目录内，等待 code 提交。
func StartOAuthLogin(baseURL, authDir string) (map[string]any, error) {
	verifier, err := randomPKCEVerifier()
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	values := url.Values{}
	values.Set("client_id", "phanthy-code-cli")
	values.Set("response_type", "code")
	values.Set("redirect_uri", "https://code.phanthy.com/oauth/code/success")
	values.Set("scope", "user:inference user:profile user:sessions:claude_code")
	values.Set("code_challenge", challenge)
	values.Set("code_challenge_method", "S256")
	values.Set("state", "p2a-admin-login")
	if err := os.MkdirAll(authDir, 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(authDir, ".admin-login-verifier"), []byte(verifier), 0o600); err != nil {
		return nil, err
	}
	return map[string]any{
		"url": strings.TrimRight(baseURL, "/") + "/oauth/authorize?" + values.Encode(),
	}, nil
}

// ExchangeAndSaveAccount 用授权码换取 token 并写入账号目录。
func ExchangeAndSaveAccount(client *upstream.Client, dir, rawCode, rawVerifier string) (*auth.Auth, error) {
	code := extractCode(rawCode)
	if code == "" {
		return nil, fmt.Errorf("authorization code is empty")
	}
	verifier := strings.TrimSpace(rawVerifier)
	if verifier == "" {
		raw, err := os.ReadFile(filepath.Join(dir, ".admin-login-verifier"))
		if err != nil {
			return nil, fmt.Errorf("read verifier: %w", err)
		}
		verifier = strings.TrimSpace(string(raw))
	}
	if verifier == "" {
		return nil, fmt.Errorf("PKCE verifier is empty")
	}
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("code_verifier", verifier)
	form.Set("redirect_uri", "https://code.phanthy.com/oauth/code/success")
	form.Set("client_id", "phanthy-code-cli")
	req, _ := http.NewRequest(http.MethodPost, client.OAuthTokenURL, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("anthropic-beta", client.OAuthBetaHeader)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var token struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(raw, &token); err != nil {
		return nil, err
	}
	if token.AccessToken == "" {
		return nil, fmt.Errorf("no access_token in response")
	}
	account := &auth.Auth{
		AccessToken:  token.AccessToken,
		RefreshToken: token.RefreshToken,
		ExpiresAt:    time.Now().Add(time.Duration(token.ExpiresIn) * time.Second).Unix(),
		UID:          fmt.Sprintf("phanthy-%d", time.Now().Unix()),
		Nickname:     "phanthy-" + time.Now().Format("0102"),
	}
	if err := saveAuth(dir, account); err != nil {
		return nil, err
	}
	_ = client.EnsureAPIKey(account)
	if err := account.SaveAtomic(); err != nil {
		return nil, err
	}
	return account, nil
}

// DeleteAccount 删除凭证文件并从账号池移除。
func DeleteAccount(dir string, pool *pool.Pool, uid string) error {
	if uid == "" || strings.ContainsAny(uid, `/\`) {
		return fmt.Errorf("invalid account id")
	}
	matches, err := filepath.Glob(filepath.Join(dir, uid+".json"))
	if err != nil {
		return err
	}
	for _, path := range matches {
		if filepath.Base(path) != uid+".json" {
			continue
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		pool.Remove(uid)
		return nil
	}
	return fmt.Errorf("account file not found")
}

// RefreshAccount 手动刷新指定账号的 OAuth token。
func RefreshAccount(client *upstream.Client, pool *pool.Pool, uid string) error {
	acct := pool.AuthByUID(uid)
	if acct == nil {
		return fmt.Errorf("account not found")
	}
	if err := client.RefreshToken(acct); err != nil {
		return err
	}
	if err := acct.SaveAtomic(); err != nil {
		return err
	}
	pool.Enable(uid)
	return nil
}

// KeepaliveAccount 拉取账号 profile，用于校验会话并触发上游活跃度。
func KeepaliveAccount(client *upstream.Client, pool *pool.Pool, uid string) error {
	acct := pool.AuthByUID(uid)
	if acct == nil {
		return fmt.Errorf("account not found")
	}
	if acct.NeedsRefresh(time.Minute) {
		if err := client.RefreshToken(acct); err != nil {
			return err
		}
		if err := acct.SaveAtomic(); err != nil {
			return err
		}
	}
	if _, err := client.FetchProfile(acct); err != nil {
		return err
	}
	pool.Enable(uid)
	return nil
}

// saveAuth 写入新账号凭证。
func saveAuth(dir string, account *auth.Auth) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	account.FilePath = filepath.Join(dir, account.UID+".json")
	return account.SaveAtomic()
}

// extractCode 提取完整回调 URL 或裸授权码中的纯 code。
// randomPKCEVerifier 生成浏览器授权流程使用的 verifier。
func randomPKCEVerifier() (string, error) {
	buf := make([]byte, 48)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
func extractCode(input string) string {
	input = strings.TrimSpace(input)
	if parsed, err := url.Parse(input); err == nil && parsed.Query().Get("code") != "" {
		return parsed.Query().Get("code")
	}
	if index := strings.IndexByte(input, '#'); index >= 0 {
		input = input[:index]
	}
	if index := strings.Index(input, "code="); index >= 0 {
		input = input[index+5:]
	}
	if index := strings.IndexAny(input, "& "); index >= 0 {
		input = input[:index]
	}
	return strings.TrimSpace(input)
}
