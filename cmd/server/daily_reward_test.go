package main

import (
	"bytes"
	"log"
	"strings"
	"testing"

	"phanthycode2api/internal/auth"
	"phanthycode2api/internal/logx"
	"phanthycode2api/internal/reward"
)

// captureLog 把标准日志重定向到缓冲区，便于断言提示文案。
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prevWriter, prevFlags := log.Writer(), log.Flags()
	prevLevel := logx.Current()
	log.SetOutput(buf)
	log.SetFlags(0)
	logx.SetLevel(logx.LevelInfo)
	t.Cleanup(func() {
		log.SetOutput(prevWriter)
		log.SetFlags(prevFlags)
		logx.SetLevel(prevLevel)
	})
	return buf
}

// TestNotifyDailyRewardLogsOncePerDay 到账当天只提示一次，次日到账再提示一次。
func TestNotifyDailyRewardLogsOncePerDay(t *testing.T) {
	buf := captureLog(t)
	account := &auth.Auth{UID: "phanthy-notify-1", Nickname: "phanthy-0928"}
	today := reward.Daily{Today: "2026-09-28", GrantedToday: true, TodayPoints: 1500, Streak: 3, TotalGranted: 3000}

	notifyDailyReward(account, today)
	notifyDailyReward(account, today)

	out := buf.String()
	if got := strings.Count(out, "开工奖励"); got != 1 {
		t.Fatalf("同一业务日提示 %d 次，want 1；输出：%q", got, out)
	}
	if !strings.Contains(out, "已到账 +1500 积分") {
		t.Fatalf("提示缺少积分信息：%q", out)
	}
	if !strings.Contains(out, "phanthy-0928") || !strings.Contains(out, "2026-09-28") {
		t.Fatalf("提示缺少昵称或业务日：%q", out)
	}

	buf.Reset()
	notifyDailyReward(account, reward.Daily{Today: "2026-09-29"})
	if buf.Len() != 0 {
		t.Fatalf("未到账不应提示，实际：%q", buf.String())
	}

	notifyDailyReward(account, reward.Daily{Today: "2026-09-29", GrantedToday: true, TodayPoints: 1500, Streak: 4})
	if buf.Len() == 0 {
		t.Fatal("次日到账应当提示")
	}
}

// TestNotifyDailyRewardSkipsIncomplete 缺少业务日或未到账时不提示。
func TestNotifyDailyRewardSkipsIncomplete(t *testing.T) {
	buf := captureLog(t)
	account := &auth.Auth{UID: "phanthy-notify-2", Nickname: "phanthy-0929"}

	notifyDailyReward(account, reward.Daily{GrantedToday: true, TodayPoints: 1500})
	notifyDailyReward(account, reward.Daily{Today: "2026-09-29"})

	if buf.Len() != 0 {
		t.Fatalf("不应提示，实际：%q", buf.String())
	}
}
