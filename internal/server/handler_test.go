package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestPeekRequest_LargeBodyNotTruncated 是回归测试。
// peekRequest 之前只读 1MB 就把 body 替换成截断内容，超过 1MB 的请求
// （Codex 的大上下文会话）会变成非法 JSON，上游稳定返回
// 400 {"error":{"code":"invalid_request","message":"Request body must be JSON."}}。
func TestPeekRequest_LargeBodyNotTruncated(t *testing.T) {
	filler := strings.Repeat("x", 3<<20) // 3MB，远超过旧的 1MB 截断点
	body := `{"model":"deepseek-v4.1-flash","stream":true,"messages":[{"role":"user","content":"` + filler + `"}]}`

	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	model, stream := peekRequest(r)
	if model != "deepseek-v4.1-flash" || !stream {
		t.Fatalf("peekRequest = (%q, %v), want (deepseek-v4.1-flash, true)", model, stream)
	}

	// 缓存必须完整：调用方随后再读一次，应拿到与原始请求等长的合法 JSON。
	rest, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("重新读取 body: %v", err)
	}
	if len(rest) != len(body) {
		t.Errorf("缓存后 body 长度 = %d, want %d（说明被截断）", len(rest), len(body))
	}
	if !json.Valid(rest) {
		t.Error("缓存后的 body 不是合法 JSON，上游会返回 400 Request body must be JSON.")
	}
}

// TestPeekRequest_SmallBody 小请求体也能正常返回并保留可读 body。
func TestPeekRequest_SmallBody(t *testing.T) {
	body := `{"model":"kimi-k3","stream":false,"messages":[]}`
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	model, stream := peekRequest(r)
	if model != "kimi-k3" || stream {
		t.Fatalf("peekRequest = (%q, %v), want (kimi-k3, false)", model, stream)
	}
	rest, _ := io.ReadAll(r.Body)
	if string(rest) != body {
		t.Errorf("body = %q, want %q", rest, body)
	}
}

// TestPeekRequest_NilBody 空请求不应 panic。
func TestPeekRequest_NilBody(t *testing.T) {
	if model, stream := peekRequest(nil); model != "" || stream {
		t.Errorf("peekRequest(nil) = (%q, %v), want empty", model, stream)
	}
}
