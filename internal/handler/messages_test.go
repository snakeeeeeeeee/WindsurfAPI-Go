package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhangyu/windsurfapi-go/internal/account"
	"github.com/zhangyu/windsurfapi-go/internal/models"
	reusepool "github.com/zhangyu/windsurfapi-go/internal/reuse"
	"github.com/zhangyu/windsurfapi-go/internal/store"
	usagepkg "github.com/zhangyu/windsurfapi-go/internal/usage"
	"github.com/zhangyu/windsurfapi-go/internal/windsurf"
	"github.com/zhangyu/windsurfapi-go/internal/windsurf/direct"
)

type fakeMessagesDirectClient struct {
	results []*windsurf.ChatResult
	calls   []direct.ChatRequest
}

func (f *fakeMessagesDirectClient) Chat(_ context.Context, req direct.ChatRequest) (*windsurf.ChatResult, error) {
	f.calls = append(f.calls, req)
	if len(f.results) == 0 {
		return &windsurf.ChatResult{Text: "ok", FinishReason: "stop"}, nil
	}
	result := f.results[0]
	f.results = f.results[1:]
	return result, nil
}

func TestAnthropicToWindsurfMessages(t *testing.T) {
	req := MessagesRequest{
		System: "be brief",
		Messages: []AnthropicMessage{
			{Role: "user", Content: "hi"},
			{Role: "assistant", Content: []any{map[string]any{
				"type": "tool_use", "id": "toolu_1", "name": "echo_text", "input": map[string]any{"text": "hi"},
			}}},
			{Role: "user", Content: []any{map[string]any{
				"type": "tool_result", "tool_use_id": "toolu_1", "content": "ok",
			}}},
		},
	}
	msgs, err := anthropicToWindsurfMessages(req)
	if err != nil {
		t.Fatalf("anthropicToWindsurfMessages: %v", err)
	}
	if len(msgs) != 4 {
		t.Fatalf("len=%d msgs=%+v", len(msgs), msgs)
	}
	if msgs[0].Role != "system" || msgs[0].Content != "be brief" {
		t.Fatalf("system=%+v", msgs[0])
	}
	if len(msgs[2].ToolCalls) != 1 || msgs[2].ToolCalls[0].Name != "echo_text" {
		t.Fatalf("tool calls=%+v", msgs[2].ToolCalls)
	}
	if msgs[3].Role != "tool" || msgs[3].ToolCallID != "toolu_1" || msgs[3].Content != "ok" {
		t.Fatalf("tool result=%+v", msgs[3])
	}
}

func TestAnthropicClaudeCodePayloadNormalization(t *testing.T) {
	req := MessagesRequest{
		System: []any{
			map[string]any{"type": "text", "text": "x-anthropic-billing-header: cc_version=2.1.138.49f; cc_entrypoint=sdk-cli; cch=358ff;"},
			map[string]any{"type": "text", "text": "You are a Claude agent, built on Anthropic's Claude Agent SDK."},
			map[string]any{"type": "text", "text": "CWD: /Users/zhangyu/code/myProject/supertoken-projects/WindsurfAPI-Go\nDate: 2026-05-11"},
		},
		Messages: []AnthropicMessage{{
			Role: "user",
			Content: []any{
				map[string]any{"type": "text", "text": "<system-reminder>\nAs you answer the user's questions, you can use the following context:\n# currentDate\nToday's date is 2026/05/11.\n</system-reminder>\n\n"},
				map[string]any{"type": "text", "text": "Reply with exactly: CLAUDE_CODE_OK"},
			},
		}},
	}
	msgs, err := anthropicToWindsurfMessages(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("msgs=%+v", msgs)
	}
	system := msgs[0].Content
	for _, banned := range []string{"x-anthropic-billing-header", "cc_version=", "Claude Agent SDK", "You are a Claude agent"} {
		if strings.Contains(system, banned) {
			t.Fatalf("system still contains %q: %q", banned, system)
		}
	}
	if !strings.Contains(system, "CWD: /Users/zhangyu/code/myProject/supertoken-projects/WindsurfAPI-Go") &&
		!strings.Contains(system, "Working directory: /Users/zhangyu/code/myProject/supertoken-projects/WindsurfAPI-Go") {
		t.Fatalf("system lost cwd: %q", system)
	}
	user := msgs[1].Content
	if strings.Contains(user, "system-reminder") || strings.Contains(user, "Today's date") {
		t.Fatalf("user reminder leaked: %q", user)
	}
	if user != "Reply with exactly: CLAUDE_CODE_OK" {
		t.Fatalf("user=%q", user)
	}
}

func TestAnthropicReadToolResultAnnotation(t *testing.T) {
	req := MessagesRequest{
		Messages: []AnthropicMessage{
			{Role: "assistant", Content: []any{map[string]any{
				"type": "tool_use", "id": "toolu_1", "name": "Read", "input": map[string]any{"file_path": "big.md"},
			}}},
			{Role: "user", Content: []any{map[string]any{
				"type":        "tool_result",
				"tool_use_id": "toolu_1",
				"is_error":    true,
				"content":     "File content (377.3KB) exceeds maximum allowed size (256KB). Use offset and limit parameters to read specific portions of the file.",
			}}},
		},
	}
	msgs, err := anthropicToWindsurfMessages(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || !strings.Contains(msgs[1].Content, "does not prove the full file body") {
		t.Fatalf("msgs=%+v", msgs)
	}
	normal := annotateRiskyReadToolResult("1\t// cached keyword in real body", "Read", true)
	if strings.Contains(normal, "WindsurfAPI note") {
		t.Fatalf("real line-numbered body should not be annotated: %q", normal)
	}
	if got := annotateRiskyReadToolResult("File unchanged since last read.", "Bash", true); strings.Contains(got, "WindsurfAPI note") {
		t.Fatalf("non-Read tool should not be annotated: %q", got)
	}
}

func TestWriteMessagesResponseWithToolUse(t *testing.T) {
	rec := httptest.NewRecorder()
	writeMessagesResponse(rec, "claude-sonnet-4.6", &windsurf.ChatResult{
		FinishReason: "tool_calls",
		ToolCalls: []windsurf.ToolCall{{
			ID:            "toolu_1",
			Name:          "echo_text",
			ArgumentsJSON: `{"text":"hi"}`,
		}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("json: %v", err)
	}
	if body["stop_reason"] != "tool_use" {
		t.Fatalf("stop_reason=%v", body["stop_reason"])
	}
	content := body["content"].([]any)
	block := content[0].(map[string]any)
	if block["type"] != "tool_use" || block["name"] != "echo_text" {
		t.Fatalf("block=%v", block)
	}
}

func TestWriteMessagesResponseWithThinking(t *testing.T) {
	rec := httptest.NewRecorder()
	writeMessagesResponse(rec, "claude-sonnet-4.6", &windsurf.ChatResult{Thinking: "private notes", Text: "final"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("json: %v", err)
	}
	content := body["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("content=%v", content)
	}
	thinking := content[0].(map[string]any)
	if thinking["type"] != "thinking" || thinking["thinking"] != "private notes" {
		t.Fatalf("thinking block=%v", thinking)
	}
}

func TestAnthropicUsageDoesNotExposeUnverifiedCacheFields(t *testing.T) {
	got := anthropicUsage(usagepkg.FromUpstream(&windsurf.Usage{InputTokens: 100, OutputTokens: 20, CacheReadTokens: 40, CacheWriteTokens: 10}, 0))
	if got["input_tokens"] != uint64(100) || got["output_tokens"] != uint64(20) {
		t.Fatalf("usage=%v", got)
	}
	creation := got["cache_creation"].(map[string]any)
	if got["cache_read_input_tokens"] != uint64(0) || got["cache_creation_input_tokens"] != uint64(0) {
		t.Fatalf("cache fields should be zeroed: %v", got)
	}
	if creation["ephemeral_5m_input_tokens"] != uint64(0) || creation["ephemeral_1h_input_tokens"] != uint64(0) {
		t.Fatalf("creation=%v", creation)
	}
}

func TestAnthropicThinkingConfig(t *testing.T) {
	cfg, err := anthropicThinkingConfigFromRequest(map[string]any{"type": "enabled", "budget_tokens": float64(1024)})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Enabled || cfg.Budget != 1024 || anthropicThinkingPrompt(cfg) != "" {
		t.Fatalf("cfg=%+v prompt=%q", cfg, anthropicThinkingPrompt(cfg))
	}
	cfg, err = anthropicThinkingConfigFromRequest(map[string]any{"type": "disabled"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Enabled || anthropicThinkingPrompt(cfg) != "" {
		t.Fatalf("disabled cfg=%+v", cfg)
	}
	cfg, err = anthropicThinkingConfigFromRequest(map[string]any{"type": "adaptive"})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Enabled {
		t.Fatalf("adaptive cfg=%+v", cfg)
	}
	cfg = mergeAnthropicOutputConfigEffort(anthropicThinkingConfig{}, map[string]any{"effort": "high"})
	if !cfg.Enabled || cfg.Budget != 8192 {
		t.Fatalf("output_config effort cfg=%+v", cfg)
	}
	already := mergeAnthropicOutputConfigEffort(anthropicThinkingConfig{Enabled: true, Budget: 512}, map[string]any{"effort": "high"})
	if already.Budget != 512 {
		t.Fatalf("explicit thinking should win cfg=%+v", already)
	}
}

func TestAnthropicOutputConfigResponseFormatHint(t *testing.T) {
	hint := anthropicOutputConfigResponseFormatHint(map[string]any{
		"format": map[string]any{
			"type":   "json_schema",
			"name":   "answer",
			"schema": map[string]any{"type": "object", "required": []any{"answer"}},
		},
	})
	if !strings.Contains(hint, "Respond with valid JSON only") || !strings.Contains(hint, `"required"`) {
		t.Fatalf("hint=%q", hint)
	}
}

func TestAnthropicCachePolicyFromRequest(t *testing.T) {
	req := MessagesRequest{
		CacheControl: map[string]any{"type": "ephemeral"},
		System: []any{
			map[string]any{"type": "text", "text": "stable", "cache_control": map[string]any{"type": "ephemeral", "ttl": "1h"}},
		},
		Tools: []AnthropicTool{{
			Name: "echo_text", CacheControl: map[string]any{"type": "ephemeral"},
		}},
		Messages: []AnthropicMessage{{
			Role: "user",
			Content: []any{
				map[string]any{"type": "text", "text": "hi", "cache_control": map[string]any{"type": "ephemeral"}},
			},
		}},
	}
	policy := anthropicCachePolicyFromRequest(req)
	if policy.BreakpointCount != 4 || !policy.Has1h {
		t.Fatalf("policy=%+v", policy)
	}
	if policy.ReuseTTL() != time.Hour {
		t.Fatalf("ttl=%s", policy.ReuseTTL())
	}
	msgs, err := anthropicToWindsurfMessages(req)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(msgs[0].Content, "cache_control") || strings.Contains(msgs[len(msgs)-1].Content, "cache_control") {
		t.Fatalf("cache_control leaked into messages: %+v", msgs)
	}
}

func TestAnthropicServerSideToolsAreDroppedAndChoicePruned(t *testing.T) {
	tools, dropped, err := anthropicToolsToDirect([]AnthropicTool{
		{Type: "web_search_20250305", Name: "web_search"},
		{Name: "echo_text", InputSchema: map[string]any{"type": "object"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].Name != "echo_text" || !dropped["web_search"] {
		t.Fatalf("tools=%+v dropped=%+v", tools, dropped)
	}
	choice, err := anthropicToolChoiceToDirect(map[string]any{"type": "tool", "name": "web_search"})
	if err != nil {
		t.Fatal(err)
	}
	if got := pruneAnthropicToolChoiceByDroppedNames(pruneDirectToolChoice(choice, tools), dropped); got != nil {
		t.Fatalf("server-side tool choice should be pruned: %+v", got)
	}
}

func TestMessagesHandlerRoutesOutputConfigEffortModel(t *testing.T) {
	am := testMessagesAccountManager(t)
	_, _ = am.AddAccount("first@example.com", "tok-a", "u1", "", "")
	fake := &fakeMessagesDirectClient{}
	handler := MessagesHandler(am, fake, nil, nil)
	body := `{"model":"claude-opus-4-7","max_tokens":128,"output_config":{"effort":"high"},"messages":[{"role":"user","content":"hi"}]}`
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(fake.calls) != 1 || fake.calls[0].Model.ID != "claude-opus-4-7-high" {
		t.Fatalf("routed model calls=%+v", fake.calls)
	}
}

func TestMessagesHandlerKeepsOpus47ModelForToolRequests(t *testing.T) {
	am := testMessagesAccountManager(t)
	_, _ = am.AddAccount("first@example.com", "tok-a", "u1", "", "")
	fake := &fakeMessagesDirectClient{}
	handler := MessagesHandler(am, fake, nil, nil)
	body := `{"model":"claude-opus-4-7","max_tokens":128,"output_config":{"effort":"xhigh"},"tools":[{"name":"echo_text","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"call tool"}]}`
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(fake.calls) != 1 || fake.calls[0].Model.ID != "claude-opus-4-7-xhigh" {
		t.Fatalf("tool request should keep requested Opus 4.7 model, calls=%+v", fake.calls)
	}
	if rec.Header().Get("X-Windsurf-Requested-Model") != "" || rec.Header().Get("X-Windsurf-Served-Model") != "" {
		t.Fatalf("fallback headers should not be present requested=%q served=%q", rec.Header().Get("X-Windsurf-Requested-Model"), rec.Header().Get("X-Windsurf-Served-Model"))
	}
	if rec.Header().Get("X-Windsurf-Tool-Mode") != "native" {
		t.Fatalf("tool mode header=%q", rec.Header().Get("X-Windsurf-Tool-Mode"))
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response json: %v", err)
	}
	if resp["model"] != "claude-opus-4-7" {
		t.Fatalf("response model should preserve requested public model, got=%v", resp["model"])
	}
}

func TestMessagesHandlerRoutesThinkingModel(t *testing.T) {
	am := testMessagesAccountManager(t)
	_, _ = am.AddAccount("first@example.com", "tok-a", "u1", "", "")
	fake := &fakeMessagesDirectClient{}
	handler := MessagesHandler(am, fake, nil, nil)
	body := `{"model":"claude-sonnet-4-6","max_tokens":128,"thinking":{"type":"enabled"},"messages":[{"role":"user","content":"hi"}]}`
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(fake.calls) != 1 || fake.calls[0].Model.ID != "claude-sonnet-4.6-thinking" {
		t.Fatalf("routed model calls=%+v", fake.calls)
	}
	if fake.calls[0].Thinking != "" {
		t.Fatalf("thinking prompt leaked: %q", fake.calls[0].Thinking)
	}
}

func TestAnthropicToolChoiceAlias(t *testing.T) {
	choice, err := anthropicToolChoiceToDirect(map[string]any{"type": "tool", "name": "echo_text"})
	if err != nil {
		t.Fatal(err)
	}
	if choice == nil || choice.ToolName != "echo_text" {
		t.Fatalf("choice=%+v", choice)
	}
	choice, err = anthropicToolChoiceToDirect(map[string]any{"type": "auto"})
	if err != nil {
		t.Fatal(err)
	}
	if choice != nil {
		t.Fatalf("auto choice=%+v", choice)
	}
	if shouldSuppressAnthropicToolsForContinuation([]AnthropicMessage{{Role: "user", Content: []any{map[string]any{"type": "tool_result", "tool_use_id": "x", "content": "ok"}}}}, &direct.ToolChoice{ToolName: "echo_text"}) {
		t.Fatal("forced tool should not suppress")
	}
	if shouldSuppressAnthropicToolsForContinuation([]AnthropicMessage{{Role: "user", Content: []any{map[string]any{"type": "tool_result", "tool_use_id": "x", "content": "ok"}}}}, nil) {
		t.Fatal("tool-result continuation should keep Anthropic tools available for later tool-chain steps")
	}
}

func TestAnthropicStreamWriterShape(t *testing.T) {
	rec := httptest.NewRecorder()
	sw, err := newAnthropicStreamWriter(rec, "claude-sonnet-4.6")
	if err != nil {
		t.Fatalf("new stream: %v", err)
	}
	if err := sw.TextDelta("hi"); err != nil {
		t.Fatalf("text delta: %v", err)
	}
	if err := sw.Finish(&windsurf.ChatResult{Text: "hi"}); err != nil {
		t.Fatalf("finish: %v", err)
	}
	body := rec.Body.String()
	for _, want := range []string{"event: message_start", "event: content_block_start", "text_delta", "event: message_stop"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in %q", want, body)
		}
	}
}

func TestAnthropicStreamWriterTextThenToolStartsToolBlock(t *testing.T) {
	rec := httptest.NewRecorder()
	sw, err := newAnthropicStreamWriter(rec, "claude-sonnet-4.6")
	if err != nil {
		t.Fatalf("new stream: %v", err)
	}
	if err := sw.TextDelta("checking"); err != nil {
		t.Fatalf("text delta: %v", err)
	}
	if err := sw.ToolCallDelta(0, windsurf.ToolCall{ID: "toolu_1", Name: "Bash", ArgumentsJSON: `{"command":"go test ./internal/account -count=1"}`}); err != nil {
		t.Fatalf("tool delta: %v", err)
	}
	if err := sw.Finish(&windsurf.ChatResult{ToolCalls: []windsurf.ToolCall{{ID: "toolu_1", Name: "Bash"}}}); err != nil {
		t.Fatalf("finish: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"type":"tool_use"`) || !strings.Contains(body, `"type":"input_json_delta"`) {
		t.Fatalf("missing tool stream shape in %q", body)
	}
	if !strings.Contains(body, `"index":1`) {
		t.Fatalf("tool block should use next content block index after text block: %q", body)
	}
}

func TestAnthropicStreamWriterThinkingShape(t *testing.T) {
	rec := httptest.NewRecorder()
	sw, err := newAnthropicStreamWriter(rec, "claude-sonnet-4.6")
	if err != nil {
		t.Fatalf("new stream: %v", err)
	}
	if err := sw.ThinkingDelta("reasoning"); err != nil {
		t.Fatalf("thinking delta: %v", err)
	}
	if err := sw.TextDelta("final"); err != nil {
		t.Fatalf("text delta: %v", err)
	}
	if err := sw.Finish(&windsurf.ChatResult{Thinking: "reasoning", Text: "final"}); err != nil {
		t.Fatalf("finish: %v", err)
	}
	body := rec.Body.String()
	for _, want := range []string{`"type":"thinking"`, "thinking_delta", "text_delta", "event: content_block_stop"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in %q", want, body)
		}
	}
}

func TestAnthropicStreamWriterToolCallShape(t *testing.T) {
	rec := httptest.NewRecorder()
	sw, err := newAnthropicStreamWriter(rec, "claude-sonnet-4.6")
	if err != nil {
		t.Fatalf("new stream: %v", err)
	}
	if err := sw.ToolCallDelta(0, windsurf.ToolCall{ID: "toolu_1", Name: "echo_text"}); err != nil {
		t.Fatalf("tool start: %v", err)
	}
	if err := sw.ToolCallDelta(0, windsurf.ToolCall{ArgumentsJSON: `{"text":"hi"}`}); err != nil {
		t.Fatalf("tool args: %v", err)
	}
	if err := sw.Finish(&windsurf.ChatResult{FinishReason: "tool_calls", ToolCalls: []windsurf.ToolCall{{ID: "toolu_1", Name: "echo_text", ArgumentsJSON: `{"text":"hi"}`}}}); err != nil {
		t.Fatalf("finish: %v", err)
	}
	body := rec.Body.String()
	for _, want := range []string{`"type":"tool_use"`, `"name":"echo_text"`, "input_json_delta", `partial_json":"{\"text\":\"hi\"}`, `"stop_reason":"tool_use"`, "event: message_stop"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in %q", want, body)
		}
	}
}

func TestAnthropicStreamWriterStructuredError(t *testing.T) {
	rec := httptest.NewRecorder()
	sw, err := newAnthropicStreamWriter(rec, "claude-sonnet-4.6")
	if err != nil {
		t.Fatal(err)
	}
	if err := sw.TextDelta("partial"); err != nil {
		t.Fatal(err)
	}
	if err := sw.Error(account.ErrorUpstreamTransient, errors.New("boom")); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "event: error") || !strings.Contains(body, `"type":"upstream_transient"`) || !strings.Contains(body, `"message":"boom"`) {
		t.Fatalf("body=%q", body)
	}
	if !strings.Contains(body, "event: content_block_stop") {
		t.Fatalf("error should close active content block body=%q", body)
	}
}

func TestAnthropicStreamWriterStructuredErrorRedactsSecrets(t *testing.T) {
	rec := httptest.NewRecorder()
	sw, err := newAnthropicStreamWriter(rec, "claude-sonnet-4.6")
	if err != nil {
		t.Fatal(err)
	}
	if err := sw.Error(account.ErrorTransport, errors.New("Cookie: devin-session-token$abc123 user@example.com")); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	if strings.Contains(body, "devin-session-token$abc123") || strings.Contains(body, "user@example.com") {
		t.Fatalf("secret leaked in anthropic stream error: %s", body)
	}
	if !strings.Contains(body, "[REDACTED]") {
		t.Fatalf("missing redaction markers: %s", body)
	}
}

func TestWriteMessagesResponseSanitizesWorkspacePaths(t *testing.T) {
	rec := httptest.NewRecorder()
	writeMessagesResponse(rec, "claude-sonnet-4.6", &windsurf.ChatResult{
		Text:         "read /tmp/windsurf-workspace/a.txt",
		Thinking:     "thinking /home/user/projects/workspace-abc123/b.txt",
		FinishReason: "tool_calls",
		ToolCalls: []windsurf.ToolCall{{
			ID:            "toolu_1",
			Name:          "Read",
			ArgumentsJSON: `{"file_path":"/home/user/projects/workspace-abc123/b.txt"}`,
		}},
	})
	body := rec.Body.String()
	for _, forbidden := range []string{"windsurf-workspace", "workspace-abc123"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("workspace path leaked: %s", body)
		}
	}
	if !strings.Contains(body, "<workspace>") {
		t.Fatalf("missing workspace marker: %s", body)
	}
}

func TestAnthropicStreamWriterSanitizesWorkspacePaths(t *testing.T) {
	rec := httptest.NewRecorder()
	sw, err := newAnthropicStreamWriter(rec, "claude-sonnet-4.6")
	if err != nil {
		t.Fatal(err)
	}
	if err := sw.TextDelta("see /tmp/windsurf-workspace/a.txt"); err != nil {
		t.Fatal(err)
	}
	if err := sw.ToolCallDelta(0, windsurf.ToolCall{ID: "toolu_1", Name: "Read", ArgumentsJSON: `{"file_path":"/home/user/projects/workspace-abc123/a.txt"}`}); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	if strings.Contains(body, "windsurf-workspace") || strings.Contains(body, "workspace-abc123") {
		t.Fatalf("workspace path leaked in stream: %s", body)
	}
}

func TestMessagesToolContinuationReusesFirstLegAccount(t *testing.T) {
	am := testMessagesAccountManager(t)
	id1, _ := am.AddAccount("first@example.com", "tok-a", "u1", "", "")
	id2, _ := am.AddAccount("second@example.com", "tok-b", "u2", "", "")
	_ = am.UpdateAccount(int(id1), map[string]interface{}{"quota_daily_percent": 90, "quota_weekly_percent": 90})
	_ = am.UpdateAccount(int(id2), map[string]interface{}{"quota_daily_percent": 10, "quota_weekly_percent": 10})

	rp := reusepool.NewPool()
	model := models.GetModelByID("claude-sonnet-4.6")
	client := &fakeMessagesDirectClient{results: []*windsurf.ChatResult{
		{FinishReason: "tool_calls", ToolCalls: []windsurf.ToolCall{{ID: "toolu_1", Name: "read_file", ArgumentsJSON: `{"path":"a.txt"}`}}},
		{Text: "done", FinishReason: "stop"},
	}}
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("X-Caller-Key", "caller-messages")
	params := directChatParams{
		Model:     model,
		Messages:  []windsurf.ChatMessage{{Role: "user", Content: "read file"}},
		CallerKey: "caller-messages",
		Route:     "messages",
		Tools:     []direct.ToolDefinition{{Name: "read_file", SchemaJSON: `{"type":"object"}`}},
	}
	result, status, err := executeDirectChat(req, client, am, rp, params)
	if err != nil || status != http.StatusOK || len(result.ToolCalls) != 1 {
		t.Fatalf("first result=%+v status=%d err=%v", result, status, err)
	}

	_ = am.UpdateAccount(int(id1), map[string]interface{}{"quota_daily_percent": 1, "quota_weekly_percent": 1})
	params.Tools = nil
	params.Messages = []windsurf.ChatMessage{
		{Role: "user", Content: "read file"},
		{Role: "assistant", ToolCalls: result.ToolCalls},
		{Role: "tool", ToolCallID: "toolu_1", Content: "file content"},
	}
	result, status, err = executeDirectChat(req, client, am, rp, params)
	if err != nil || status != http.StatusOK || result.Text != "done" {
		t.Fatalf("second result=%+v status=%d err=%v", result, status, err)
	}
	if rp.Stats().Hits != 1 {
		t.Fatalf("expected reuse hit stats=%+v", rp.Stats())
	}
	assertAccountRPM(t, am, int(id1), 2)
	assertAccountRPM(t, am, int(id2), 0)
}

func testMessagesAccountManager(t *testing.T) *account.Manager {
	t.Helper()
	sqliteStore, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "windsurf.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqliteStore.Close() })
	return account.NewManager(sqliteStore)
}

func assertAccountRPM(t *testing.T, am *account.Manager, id int, want int) {
	t.Helper()
	for _, row := range am.Snapshot().Accounts {
		if row.ID == id {
			if row.RPMUsed != want {
				t.Fatalf("account %d rpm=%d want=%d snapshot=%+v", id, row.RPMUsed, want, am.Snapshot().Accounts)
			}
			return
		}
	}
	t.Fatalf("account %d not found", id)
}
