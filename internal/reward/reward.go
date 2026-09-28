// Package reward 解析账号的「每日开工奖励」台账。
//
// 上游没有可以直接调用的领取接口：奖励由服务端在每个北京时间业务日 0 点，
// 依据账号已登记的桌面端安装（rewards 里的 source_object_type=DesktopInstallation）
// 自动发放，落账为 /api/oauth/rewards 中的一条 daily_login 记录。
// 所以本项目能做的是「自动对账 + 到账提醒」，而不是代替桌面端去点领取。
package reward

import (
	"encoding/json"
	"sort"
	"time"
)

// beijing 是上游划分业务日使用的时区（UTC+8）。
// 奖励在每天北京时间 0 点发放，落在 UTC 上就是前一天 16:00Z。
var beijing = time.FixedZone("CST", 8*60*60)

const dayLayout = "2006-01-02"

// Daily 是账号的每日开工奖励状态，供管理台与日志展示。
type Daily struct {
	Today          string  `json:"today"`            // 当前业务日（北京时间日期）
	GrantedToday   bool    `json:"granted_today"`    // 今天这一笔是否已到账
	TodayPoints    float64 `json:"today_points"`     // 今天到账的积分
	Streak         int     `json:"streak"`           // 连续领取天数（今天未到账时算到昨天为止）
	LastGrantedAt  string  `json:"last_granted_at"`  // 最近一次到账时间（北京时间 RFC3339）
	LastPoints     float64 `json:"last_points"`      // 最近一次到账积分
	NextExpectedAt string  `json:"next_expected_at"` // 下一笔预计到账时间（北京时间 0 点）
	TotalGranted   float64 `json:"total_granted"`    // 历史累计到账积分
	GrantedDays    int     `json:"granted_days"`     // 历史到账天数
}

// Analyze 汇总 rewards 台账里的每日开工奖励。
// now 用于判定「今天」；台账为空时返回可读的默认值。
func Analyze(rewards []map[string]any, now time.Time) Daily {
	pointsByDay := map[string]float64{}
	var last time.Time
	var lastPoints float64
	total := 0.0

	for _, r := range rewards {
		if !isDailyLogin(r) || status(r) != "granted" {
			continue
		}
		points, ok := toFloat(r["points"])
		if !ok || points <= 0 {
			continue
		}
		granted, ok := parseTime(r["granted_at"])
		if !ok {
			continue
		}
		day := granted.In(beijing).Format(dayLayout)
		pointsByDay[day] += points
		total += points
		if granted.After(last) {
			last = granted
			lastPoints = points
		}
	}

	out := Daily{
		NextExpectedAt: nextMidnight(now).Format(time.RFC3339),
		TotalGranted:   total,
		GrantedDays:    len(pointsByDay),
		LastPoints:     lastPoints,
	}
	if !last.IsZero() {
		out.LastGrantedAt = last.In(beijing).Format(time.RFC3339)
	}

	today := now.In(beijing).Format(dayLayout)
	out.Today = today
	if points, ok := pointsByDay[today]; ok {
		out.GrantedToday = true
		out.TodayPoints = points
	}
	out.Streak = streak(pointsByDay, now)
	return out
}

// streak 计算连续领取天数：今天已到账就从今天数，否则从昨天数
// （今天 0 点那一笔可能还没到账，但连续记录并未中断）。
func streak(pointsByDay map[string]float64, now time.Time) int {
	cursor := midnight(now)
	if _, ok := pointsByDay[cursor.Format(dayLayout)]; !ok {
		cursor = cursor.AddDate(0, 0, -1)
	}
	days := 0
	for {
		if _, ok := pointsByDay[cursor.Format(dayLayout)]; !ok {
			return days
		}
		days++
		cursor = cursor.AddDate(0, 0, -1)
	}
}

// midnight 返回 now 所在的北京时间当天 0 点。
func midnight(now time.Time) time.Time {
	local := now.In(beijing)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, beijing)
}

// nextMidnight 返回 now 之后最近的一个北京时间 0 点，即下一笔奖励的发放时间。
func nextMidnight(now time.Time) time.Time {
	return midnight(now).AddDate(0, 0, 1)
}

// isDailyLogin 判断一条奖励是否为每日开工奖励。
func isDailyLogin(r map[string]any) bool {
	for _, key := range []string{"reward_type", "reward_code", "reason_code"} {
		if v, _ := r[key].(string); v == "daily_login" {
			return true
		}
	}
	return false
}

func status(r map[string]any) string {
	v, _ := r["status"].(string)
	return v
}

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

// parseTime 解析上游的 RFC3339 时间字段。
func parseTime(v any) (time.Time, bool) {
	text, ok := v.(string)
	if !ok || text == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, text); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

// SortedDays 返回台账里出现过的业务日，按时间倒序，便于排查漏发。
func SortedDays(rewards []map[string]any) []string {
	seen := map[string]bool{}
	for _, r := range rewards {
		if !isDailyLogin(r) || status(r) != "granted" {
			continue
		}
		granted, ok := parseTime(r["granted_at"])
		if !ok {
			continue
		}
		seen[granted.In(beijing).Format(dayLayout)] = true
	}
	days := make([]string, 0, len(seen))
	for day := range seen {
		days = append(days, day)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(days)))
	return days
}
