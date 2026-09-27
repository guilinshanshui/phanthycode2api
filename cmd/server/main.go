// Package main phanthycode2api 入口：加载配置、构建 pool、起调度器与 HTTP 服务。
package main

import (
	"context"
	"encoding/json"
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

	cfg, err := Load(*cfgPath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("config %s not found, using defaults+env", *cfgPath)
			cfg, err = Load("")
		}
		if err != nil {
			log.Fatalf("load config: %v", err)
		}
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

	adminStore, err := admin.New(cfg.Admin.DataDir)
	if err != nil {
		log.Fatalf("load admin data: %v", err)
	}
	if cfg.Admin.Enabled && cfg.Admin.PasswordHash == "" {
		log.Print("admin.enabled=true but admin.password_hash is empty; /admin disabled")
	}
	var adminHandler *admin.Handler
	if cfg.Admin.Enabled && cfg.Admin.PasswordHash != "" {
		adminStore.SetConfigState(admin.ConfigState{
			GetConfig:  func() map[string]any { return cfg.ToMap() },
			SaveConfig: func(raw map[string]any) error { return SaveConfigMap(*cfgPath, raw) },
		})
		adminHandler = admin.NewHandler(adminStore,
			func() bool { return cfg.Admin.PasswordHash != "" },
			func(password string) bool { return admin.VerifyPassword(password, cfg.Admin.PasswordHash) },
		)
		adminHandler.Accounts = &admin.AccountManager{
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
				return ExchangeAndSaveAccount(upstream.New(cfg.BaseURL), cfg.AuthDir, code, verifier)
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

// ToMap 导出当前配置，供管理端设置页展示。
func (c *Config) ToMap() map[string]any {
	raw, _ := json.Marshal(c)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	out["hard_credit_duration"] = c.HardCreditDur.String()
	out["soft_rate_duration"] = c.SoftRateDur.String()
	out["err_cooldown_duration"] = c.ErrCooldownDur.String()
	return out
}

// SaveConfigMap 保存设置页提交的 JSON，并禁止修改监听地址与管理员密码。
func SaveConfigMap(path string, raw map[string]any) error {
	delete(raw, "listen")
	delete(raw, "admin")
	delete(raw, "hard_credit_duration")
	delete(raw, "soft_rate_duration")
	delete(raw, "err_cooldown_duration")
	encoded, err := json.MarshalIndent(raw, "", "  ")
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

// ExchangeAndSaveAccount 用授权码换取 token 并写入账号目录。
func ExchangeAndSaveAccount(client *upstream.Client, dir, rawCode, rawVerifier string) error {
	code := extractCode(rawCode)
	if code == "" {
		return fmt.Errorf("authorization code is empty")
	}
	verifier := strings.TrimSpace(rawVerifier)
	if verifier == "" {
		raw, err := os.ReadFile(".login-verifier")
		if err != nil {
			return fmt.Errorf("read verifier: %w", err)
		}
		verifier = strings.TrimSpace(string(raw))
	}
	if verifier == "" {
		return fmt.Errorf("PKCE verifier is empty")
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
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var token struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(raw, &token); err != nil {
		return err
	}
	if token.AccessToken == "" {
		return fmt.Errorf("no access_token in response")
	}
	auth := &auth.Auth{
		AccessToken:  token.AccessToken,
		RefreshToken: token.RefreshToken,
		ExpiresAt:    time.Now().Add(time.Duration(token.ExpiresIn) * time.Second).Unix(),
		UID:          fmt.Sprintf("phanthy-%d", time.Now().Unix()),
		Nickname:     "phanthy-" + time.Now().Format("0102"),
	}
	if err := saveAuth(dir, auth); err != nil {
		return err
	}
	_ = client.EnsureAPIKey(auth)
	return auth.SaveAtomic()
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
		pool.SyncToDir(nil)
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
	return acct.SaveAtomic()
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
	_, err := client.FetchProfile(acct)
	return err
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
