// Package scheduler 定时任务：token keepalive + 每日开工奖励自动领取。
package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"phanthycode2api/internal/auth"
	"phanthycode2api/internal/logx"
	"phanthycode2api/internal/pool"
	"phanthycode2api/internal/reward"
	"phanthycode2api/internal/upstream"
)

// beijing 是上游划分业务日使用的时区（UTC+8）。
// 开工奖励按北京时间自然日发放，所以领取调度也用同一时区。
var beijing = time.FixedZone("CST", 8*60*60)

// Config 调度器依赖。
type Config struct {
	Pool           *pool.Pool
	Upstream       *upstream.Client
	KeepaliveHours []int // 默认 [22]
	DailyReward    bool  // 每天自动登记安装并领取开工奖励
	ClaimHour      int   // 领取时刻（北京时间小时），默认 0
	ClaimMinute    int   // 领取时刻（北京时间分钟），默认 5
}

// Scheduler 调度器。
type Scheduler struct {
	cfg Config
}

// New 构建。
func New(cfg Config) *Scheduler {
	if len(cfg.KeepaliveHours) == 0 {
		cfg.KeepaliveHours = []int{22}
	}
	if cfg.ClaimHour < 0 || cfg.ClaimHour > 23 {
		cfg.ClaimHour = 0
	}
	if cfg.ClaimMinute < 0 || cfg.ClaimMinute > 59 {
		cfg.ClaimMinute = 5
	}
	return &Scheduler{cfg: cfg}
}

// nextFire 返回 now 之后最近的一个整点触发时间；hours 为本地小时（0-23）。
func nextFire(now time.Time, hours []int) time.Time {
	var earliest time.Time
	for _, h := range hours {
		t := time.Date(now.Year(), now.Month(), now.Day(), h, 0, 0, 0, now.Location())
		if !t.After(now) {
			t = t.Add(24 * time.Hour)
		}
		if earliest.IsZero() || t.Before(earliest) {
			earliest = t
		}
	}
	return earliest
}

// nextDailyClaim 返回 now 之后最近的一个领取时刻（北京时间）。
func nextDailyClaim(now time.Time, hour, minute int) time.Time {
	local := now.In(beijing)
	t := time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, beijing)
	if !t.After(now) {
		t = t.AddDate(0, 0, 1)
	}
	return t
}

// Run 主循环，阻塞直到 ctx 取消。
func (s *Scheduler) Run(ctx context.Context) {
	// 启动时立即执行一次 keepalive（保证服务起来后账号立即可用）
	logx.Infof("scheduler: running initial keepalive")
	s.RunKeepaliveNow()

	if s.cfg.DailyReward {
		// 启动即补领：进程可能是在北京时间 0 点之后才起来的。
		logx.Infof("scheduler: 每日开工奖励自动领取已启用（每天北京时间 %02d:%02d）", s.cfg.ClaimHour, s.cfg.ClaimMinute)
		s.RunDailyClaimNow()
	}

	for {
		now := time.Now()
		nextKeep := nextFire(now, s.cfg.KeepaliveHours)
		nextClaim := time.Time{}
		if s.cfg.DailyReward {
			nextClaim = nextDailyClaim(now, s.cfg.ClaimHour, s.cfg.ClaimMinute)
		}
		target := nextKeep
		if !nextClaim.IsZero() && nextClaim.Before(target) {
			target = nextClaim
		}
		timer := time.NewTimer(time.Until(target))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			// 定时器可能比目标时刻略早触发，留 1 秒余量再判定。
			due := time.Now().Add(time.Second)
			if !nextClaim.IsZero() && !nextClaim.After(due) {
				s.RunDailyClaimNow()
			}
			if !nextKeep.After(due) {
				s.RunKeepaliveNow()
			}
		}
	}
}

// RunDailyClaimNow 为每个账号登记桌面端安装并领取当天开工奖励。
// 领取接口幂等（当天已发放时返回 already_granted），可安全地重复执行。
func (s *Scheduler) RunDailyClaimNow() {
	ok, skipped, failed := 0, 0, 0
	for _, st := range s.cfg.Pool.List() {
		account := s.cfg.Pool.AuthByUID(st.UID)
		if account == nil {
			continue
		}
		// 被禁用（会话失效等）的账号不再参与挑号，但 access_token 往往还有几小时有效期，
		// 这时候照样能领开工奖励。领到就是白赚，所以只要 token 没过期就试一次，
		// 避免因为一次 keepalive 失败把连续天数断掉。
		if account.ExpiresAt > 0 && time.Now().Unix() >= account.ExpiresAt {
			logx.Debugf("开工奖励 %s: access_token 已过期，跳过", account.Nickname)
			skipped++
			continue
		}
		if err := s.claimDaily(account); err != nil {
			failed++
			continue
		}
		ok++
	}
	if ok+skipped+failed > 0 {
		logx.Infof("开工奖励: 本轮结束（成功 %d · 跳过 %d · 失败 %d）", ok, skipped, failed)
	}
}

// ClaimForUID 为单个账号补领当天的开工奖励，供管理台「领取」按钮调用。
func (s *Scheduler) ClaimForUID(uid string) error {
	account := s.cfg.Pool.AuthByUID(uid)
	if account == nil {
		return fmt.Errorf("account not found")
	}
	return s.claimDaily(account)
}

// claimDaily 登记安装 → 查询状态 → 领取，三步都幂等。
// 安装身份按账号区分：上游的 installation id 全局唯一且绑定首个登记的账号。
// 每一步都会记日志，返回值供管理台手动领取时回显。
func (s *Scheduler) claimDaily(account *auth.Auth) error {
	id, err := s.cfg.Upstream.DesktopIdentity(account.UID)
	if err != nil {
		logx.Errorf("开工奖励 %s: 加载桌面端身份失败: %v", account.Nickname, err)
		return fmt.Errorf("加载桌面端身份失败: %w", err)
	}
	if err := s.cfg.Upstream.RegisterDesktopInstallation(account, id); err != nil {
		logx.Errorf("开工奖励 %s: 登记桌面端安装失败: %v", account.Nickname, err)
		return fmt.Errorf("登记桌面端安装失败: %w", err)
	}
	summary, err := s.cfg.Upstream.ActivitySummary(account, id)
	if err != nil {
		logx.Errorf("开工奖励 %s: 查询状态失败: %v", account.Nickname, err)
		return fmt.Errorf("查询开工奖励状态失败: %w", err)
	}
	state, ok := reward.ParseSummary(summary)
	if !ok {
		logx.Debugf("开工奖励 %s: 上游未返回 daily 段，跳过", account.Nickname)
		return fmt.Errorf("上游未返回 daily 段（账号可能没有开工奖励资格）")
	}
	if state.Status == "granted_today" {
		logx.Infof("开工奖励 %s: %s 已到账 +%.0f 积分（连续 %d 天%s）",
			account.Nickname, state.ServerDate, state.Points, state.StreakDay, nextPointsNote(state.NextPoints))
		return nil
	}
	serverDate := state.ServerDate
	if serverDate == "" {
		serverDate = reward.BeijingDate(time.Now())
	}
	result, err := s.cfg.Upstream.ClaimDailyLogin(account, id, serverDate)
	if err != nil {
		logx.Errorf("开工奖励 %s: 领取失败: %v", account.Nickname, err)
		return fmt.Errorf("领取失败: %w", err)
	}
	status, _ := result["status"].(string)
	if status == "" {
		status = "ok"
	}
	points, _ := toFloat(result["points"])
	if points <= 0 {
		points = state.Points
	}
	logx.Infof("开工奖励 %s: %s 领取结果 %s +%.0f 积分%s",
		account.Nickname, serverDate, status, points, nextPointsNote(state.NextPoints))
	return nil
}

// nextPointsNote 拼出「下一档积分」提示，缺数据时返回空串。
func nextPointsNote(next float64) string {
	if next <= 0 {
		return ""
	}
	return "，下一档 " + trimFloat(next) + " 积分"
}

// trimFloat 去掉小数末尾的 0，便于日志阅读。
func trimFloat(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// toFloat 兼容 JSON 反序列化后的各种数值类型。
func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

// RunKeepaliveNow 立即对所有账号刷新 token；session 死亡的自动禁用。
func (s *Scheduler) RunKeepaliveNow() {
	for _, st := range s.cfg.Pool.List() {
		if st.Disabled {
			continue
		}
		a := s.cfg.Pool.AuthByUID(st.UID)
		if a == nil || a.RefreshToken == "" {
			continue
		}
		// 仅在 token 接近过期时刷新
		if !a.NeedsRefresh(30 * time.Minute) {
			continue
		}
		if err := s.cfg.Upstream.RefreshToken(a); err != nil {
			logx.Errorf("keepalive %s: %v", st.UID, err)
			var ue *upstream.Error
			if errors.As(err, &ue) && ue.Kind == upstream.ErrSessionDead {
				s.cfg.Pool.Disable(st.UID, "session dead")
			}
			continue
		}
		// 确保 api_key 有效
		if a.APIKey == "" {
			if err := s.cfg.Upstream.EnsureAPIKey(a); err != nil {
				logx.Debugf("keepalive %s ensure_api_key: %v", st.UID, err)
			}
		}
		if err := a.SaveAtomic(); err != nil {
			logx.Errorf("keepalive %s save: %v", st.UID, err)
		}
		logx.Infof("keepalive %s: token refreshed", st.UID)
	}
}
