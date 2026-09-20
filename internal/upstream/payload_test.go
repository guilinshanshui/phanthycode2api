package upstream

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPrepareBody_Basic(t *testing.T) {
	in := `{
		"model": "claude-sonnet-4-6",
		"max_tokens": 4096,
		"stream": true,
		"messages": [
			{"role": "system", "content": "You are helpful"},
			{"role": "user", "content": "hi"}
		],
		"temperature": 0.5
	}`
	out := PrepareBody([]byte(in))

	var parsed struct {
		Model       string          `json:"model"`
		MaxTokens   int             `json:"max_tokens"`
		System      string          `json:"system"`
		Stream      bool            `json:"stream"`
		Temperature float64         `json:"temperature"`
		Messages    []struct {
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if parsed.Model != "claude-sonnet-4-6" {
		t.Errorf("model = %s", parsed.Model)
	}
	if parsed.System != "You are helpful" {
		t.Errorf("system = %q", parsed.System)
	}
	if !parsed.Stream {
		t.Error("stream must be true")
	}
	if len(parsed.Messages) != 1 || parsed.Messages[0].Role != "user" {
		t.Fatalf("messages = %+v", parsed.Messages)
	}
	if len(parsed.Messages[0].Content) != 1 || parsed.Messages[0].Content[0].Text != "hi" {
		t.Errorf("content = %+v", parsed.Messages[0].Content)
	}
}

func TestPrepareBody_Tools(t *testing.T) {
	in := `{
		"model": "claude-3-7-sonnet",
		"messages": [{"role": "user", "content": "what's the weather"}],
		"tools": [{
			"type": "function",
			"function": {
				"name": "get_weather",
				"description": "Get weather",
				"parameters": {"type": "object", "properties": {"city": {"type": "string"}}}
			}
		}],
		"tool_choice": {"type": "function", "function": {"name": "get_weather"}}
	}`
	out := PrepareBody([]byte(in))

	var parsed struct {
		Tools      []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			InputSchema any    `json:"input_schema"`
		} `json:"tools"`
		ToolChoice struct {
			Type string `json:"type"`
			Name string `json:"name"`
		} `json:"tool_choice"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(parsed.Tools) != 1 {
		t.Fatalf("tools = %+v", parsed.Tools)
	}
	if parsed.Tools[0].Name != "get_weather" {
		t.Errorf("tool name = %s", parsed.Tools[0].Name)
	}
	if parsed.ToolChoice.Type != "tool" || parsed.ToolChoice.Name != "get_weather" {
		t.Errorf("tool_choice = %+v", parsed.ToolChoice)
	}
}

func TestPrepareBody_ToolChoiceStrings(t *testing.T) {
	cases := map[string]string{
		`"none"`:     "none",
		`"auto"`:     "auto",
		`"required"`: "any",
	}
	for in, want := range cases {
		body := []byte(`{"model":"x","messages":[{"role":"user","content":"hi"}],"tool_choice":` + in + `}`)
		out := PrepareBody(body)
		var parsed struct {
			ToolChoice json.RawMessage `json:"tool_choice"`
		}
		if err := json.Unmarshal(out, &parsed); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		var got struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(parsed.ToolChoice, &got)
		if got.Type != want {
			t.Errorf("tool_choice %s → type=%q, want %q", in, got.Type, want)
		}
	}
}

func TestPrepareBody_ToolResults(t *testing.T) {
	in := `{
		"model": "claude-3-7-sonnet",
		"messages": [
			{"role": "user", "content": "weather in SF?"},
			{"role": "assistant", "tool_calls": [{"id": "call_1", "type": "function", "function": {"name": "get_weather", "arguments": "{\"city\":\"SF\"}"}}]},
			{"role": "tool", "tool_call_id": "call_1", "content": "Sunny"}
		]
	}`
	out := PrepareBody([]byte(in))

	var parsed struct {
		Messages []struct {
			Role    string `json:"role"`
			Content []struct {
				Type      string `json:"type"`
				Text      string `json:"text"`
				ToolUseID string `json:"tool_use_id"`
				Name      string `json:"name"`
				ID        string `json:"id"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(parsed.Messages) != 3 {
		t.Fatalf("messages = %d, want 3", len(parsed.Messages))
	}
	assistant := parsed.Messages[1]
	if assistant.Role != "assistant" || len(assistant.Content) != 1 || assistant.Content[0].Type != "tool_use" {
		t.Errorf("assistant msg = %+v", assistant)
	}
	toolMsg := parsed.Messages[2]
	if toolMsg.Role != "user" || toolMsg.Content[0].Type != "tool_result" || toolMsg.Content[0].ToolUseID != "call_1" {
		t.Errorf("tool msg = %+v", toolMsg)
	}
}

func BenchmarkPrepareBody(b *testing.B) {
	in := []byte(`{"model":"claude-sonnet-4-6","max_tokens":4096,"messages":[{"role":"user","content":"hi"}],"stream":true}`)
	for i := 0; i < b.N; i++ {
		_ = PrepareBody(in)
	}
}

func TestResolveModel(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		// 规范 ID：原样透传（大小写归一化）。
		{"kimi-k3", "kimi-k3"},
		{"Kimi-k3", "kimi-k3"},
		{"glm-5.2", "glm-5.2"},
		{"glm-5.3-flash", "glm-5.3-flash"},
		{"deepseek-v4.1-flash", "deepseek-v4.1-flash"},
		{"phanthy-pro", "phanthy-pro"},

		// 展示名：内部空白折成连字符后落到规范 ID。
		{"Kimi K3", "kimi-k3"},
		{"GLM 5.2", "glm-5.2"},
		{"  Kimi-k2.7-code  ", "kimi-k2.7-code"},

		// 上下文后缀：上游只认裸模型名，必须剥离。
		{"kimi-k3[1m]", "kimi-k3"},
		{"glm-5.2[2m]", "glm-5.2"},
		{"kimi-k3:1m", "kimi-k3"},

		// Auto：落到最经济的公开档。
		{"auto", "phanthy-fast"},

		// 历史商业命名 → 当前公开 ID。
		{"DeepSeek-V4", "deepseek-v4.1-flash"},
		{"Claude Opus 4.8", "claude-opus-4-8"},
		{"claude-sonnet-4.6", "claude-sonnet-4-6"},
		{"gpt-5.6-sol", "phanthy-pro"},

		// 上游内部代号（已不在公开目录）→ 当前公开 ID，避免 403。
		{"Iris-1.0", "deepseek-v4.1-flash"},
		{"Zeus-1.1-pro", "phanthy-pro"},
		{"Gaia-1.2", "claude-opus-4-8"},
		{"Apollo-2.0", "kimi-k3"},
		{"Metis-1.1", "glm-5.2"},

		// 未知模型：返回归一化名，交给上游判定。
		{"unknown-model", "unknown-model"},
		{"Unknown Model", "unknown-model"},
		{"", ""},
	}
	for _, c := range cases {
		got := ResolveModel(c.input)
		if got != c.want {
			t.Errorf("ResolveModel(%q) = %q, want %q", c.input, got, c.want)
		}
	}
}

// TestCanonicalModelsAreResolvable 保证 /v1/models 里的每个 ID 都能被解析回来，
// 即别名表不会把某个规范 ID 改写掉，否则列表与实际调用会不一致。
func TestCanonicalModelsAreResolvable(t *testing.T) {
	for _, m := range CanonicalModels {
		if got := ResolveModel(m.ID); got != m.ID {
			t.Errorf("ResolveModel(%q) = %q, want 规范 ID 不被改写", m.ID, got)
		}
	}
}

// TestModelAliasTargetsAreReal 防止别名表指向已下线的上游代号。
// 旧版映射把客户端模型翻译成 iris/zeus/gaia/apollo/metis 这类内部代号，
// 而这些代号已不在上游可调用目录中，导致所有请求吃 403；这里锁死方向。
func TestModelAliasTargetsAreReal(t *testing.T) {
	deadPrefixes := []string{"iris-", "zeus-", "gaia-", "apollo-", "metis-"}
	for from, to := range ModelAlias {
		for _, prefix := range deadPrefixes {
			if strings.HasPrefix(to, prefix) {
				t.Errorf("ModelAlias[%q] = %q 指向已下线的内部代号，会稳定拿到 403", from, to)
			}
		}
		// 键必须是归一化后的形态，否则客户端实际发送的名字查不到。
		if from != normalizeModel(from) {
			t.Errorf("ModelAlias 键 %q 未归一化（应为 %q）", from, normalizeModel(from))
		}
	}
}

// TestNormalizeModel 确认归一化与官网客户端的处理顺序一致。
func TestNormalizeModel(t *testing.T) {
	cases := map[string]string{
		"  Kimi-K3  ":     "kimi-k3",
		"GLM 5.2":         "glm-5.2",
		"kimi-k3[1m]":     "kimi-k3",
		"kimi-k3[2M]":     "kimi-k3",
		"claude-opus-4.8": "claude-opus-4.8",
		"":                "",
	}
	for in, want := range cases {
		if got := normalizeModel(in); got != want {
			t.Errorf("normalizeModel(%q) = %q, want %q", in, got, want)
		}
	}
}
