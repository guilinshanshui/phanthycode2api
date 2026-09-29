package scheduler

import (
	"testing"
	"time"
)

// TestNextDailyClaimUsesBeijingMidnight 领取时刻按北京时间计算，
// 与上游划分业务日的时区保持一致。
func TestNextDailyClaimUsesBeijingMidnight(t *testing.T) {
	cases := []struct {
		name string
		now  time.Time
		want string
	}{
		{
			name: "北京时间当天 0 点前 → 当天",
			now:  time.Date(2026, time.September, 29, 13, 0, 0, 0, time.UTC), // 北京 9/29 21:00
			want: "2026-09-30T00:05:00+08:00",
		},
		{
			name: "北京时间 0 点已过 → 次日",
			now:  time.Date(2026, time.September, 30, 0, 6, 0, 0, beijing),
			want: "2026-10-01T00:05:00+08:00",
		},
		{
			name: "UTC 16:00 正好跨日",
			now:  time.Date(2026, time.September, 29, 15, 59, 0, 0, time.UTC),
			want: "2026-09-30T00:05:00+08:00",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := nextDailyClaim(tc.now, 0, 5).In(beijing).Format(time.RFC3339)
			if got != tc.want {
				t.Fatalf("nextDailyClaim = %s, want %s", got, tc.want)
			}
			if !nextDailyClaim(tc.now, 0, 5).After(tc.now) {
				t.Fatalf("触发时刻必须晚于 now")
			}
		})
	}
}

// TestNextFireKeepsKeepaliveBehaviour 保留原有 keepalive 整点逻辑。
func TestNextFireKeepsKeepaliveBehaviour(t *testing.T) {
	now := time.Date(2026, time.September, 29, 23, 30, 0, 0, time.UTC)
	got := nextFire(now, []int{22})
	if got.Hour() != 22 || !got.After(now) {
		t.Fatalf("nextFire = %s, want 次日 22:00", got)
	}
	if got.Day() != 30 {
		t.Fatalf("22 点已过应顺延到次日，实际 %s", got)
	}
}
