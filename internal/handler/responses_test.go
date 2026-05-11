package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zhangyu/windsurfapi-go/internal/account"
	"github.com/zhangyu/windsurfapi-go/internal/models"
	reusepool "github.com/zhangyu/windsurfapi-go/internal/reuse"
	"github.com/zhangyu/windsurfapi-go/internal/windsurf"
	"github.com/zhangyu/windsurfapi-go/internal/windsurf/direct"
)

func TestResponsesInputToMessages(t *testing.T) {
	msgs, err := responsesInputToMessages(ResponsesRequest{
		Instructions: "be brief",
		Input: []any{
			map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "hi"}}},
			map[string]any{"type": "input_text", "text": "top-level input"},
			map[string]any{"type": "output_text", "text": "prior assistant"},
			map[string]any{"type": "function_call", "call_id": "call_1", "name": "echo_text", "arguments": `{"text":"hi"}`},
			map[string]any{"type": "function_call", "call_id": "call_1b", "name": "read_file", "arguments": `{"path":"a.txt"}`},
			map[string]any{"type": "function_call_output", "call_id": "call_1", "output": "ok"},
			map[string]any{"type": "custom_tool_call", "call_id": "call_2", "name": "shell", "input": "pwd"},
			map[string]any{"type": "custom_tool_call_output", "call_id": "call_2", "output": "repo"},
		},
	})
	if err != nil {
		t.Fatalf("responsesInputToMessages: %v", err)
	}
	if len(msgs) != 8 {
		t.Fatalf("len=%d msgs=%+v", len(msgs), msgs)
	}
	if msgs[0].Role != "system" || msgs[1].Content != "hi" {
		t.Fatalf("msgs=%+v", msgs)
	}
	if msgs[2].Role != "user" || msgs[2].Content != "top-level input" {
		t.Fatalf("top-level input=%+v", msgs[2])
	}
	if msgs[3].Role != "assistant" || msgs[3].Content != "prior assistant" {
		t.Fatalf("top-level output=%+v", msgs[3])
	}
	if len(msgs[4].ToolCalls) != 2 || msgs[4].ToolCalls[0].Name != "echo_text" || msgs[4].ToolCalls[1].Name != "read_file" {
		t.Fatalf("tool call=%+v", msgs[4])
	}
	if msgs[5].Role != "tool" || msgs[5].ToolCallID != "call_1" || msgs[5].Content != "ok" {
		t.Fatalf("tool output=%+v", msgs[5])
	}
	if len(msgs[6].ToolCalls) != 1 || msgs[6].ToolCalls[0].ID != "call_2" || msgs[6].ToolCalls[0].Name != "shell" || msgs[6].ToolCalls[0].ArgumentsJSON != `{"input":"pwd"}` {
		t.Fatalf("custom tool call=%+v", msgs[6])
	}
	if msgs[7].Role != "tool" || msgs[7].ToolCallID != "call_2" || msgs[7].Content != "repo" {
		t.Fatalf("custom tool output=%+v", msgs[7])
	}
}

func TestResponsesToolsToDirectRejectsUnsupported(t *testing.T) {
	_, err := responsesToolsToDirect([]any{map[string]any{"type": "unknown_server_tool", "name": "search"}})
	if err == nil || !strings.Contains(err.Error(), "unsupported responses tool type") {
		t.Fatalf("err=%v", err)
	}
}

func TestResponsesToolsToDirectFlattensNativeToolShapes(t *testing.T) {
	tools, meta, err := responsesToolsToDirectWithMeta([]any{
		map[string]any{"type": "custom", "name": "runner", "description": "Run shell"},
		map[string]any{"type": "web_search_preview", "description": "Search"},
		map[string]any{"type": "namespace", "name": "mcp__desktop", "tools": []any{
			map[string]any{"type": "function", "name": "read_file", "parameters": map[string]any{"type": "object"}},
		}},
		map[string]any{"type": "file_search"},
	})
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	got := strings.Join(names, ",")
	for _, want := range []string{"runner", "web_search", "mcp__desktop__read_file"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %s in %v", want, names)
		}
	}
	if meta["runner"].Type != "custom" || meta["web_search"].Type != "web_search" || meta["mcp__desktop__read_file"].Namespace != "mcp__desktop" {
		t.Fatalf("meta=%+v", meta)
	}
}

func TestResponsesTextFormatHint(t *testing.T) {
	hint := responsesTextFormatHint(map[string]any{"format": map[string]any{
		"type":   "json_schema",
		"name":   "title_response",
		"schema": map[string]any{"type": "object", "properties": map[string]any{"title": map[string]any{"type": "string"}}},
	}})
	if !strings.Contains(hint, "Respond with valid JSON only") || !strings.Contains(hint, `"title"`) {
		t.Fatalf("hint=%q", hint)
	}
	if got := responsesTextFormatHint(map[string]any{"format": map[string]any{"type": "text"}}); got != "" {
		t.Fatalf("plain text format hint=%q", got)
	}
	nested := responsesTextFormatHint(map[string]any{"format": map[string]any{
		"type":        "json_schema",
		"json_schema": map[string]any{"name": "nested_response", "schema": map[string]any{"type": "object", "required": []any{"ok"}}},
	}})
	if !strings.Contains(nested, `"required"`) || !strings.Contains(nested, `"ok"`) {
		t.Fatalf("nested hint=%q", nested)
	}
}

func TestResponsesReasoningPrompt(t *testing.T) {
	if got := responsesReasoningPrompt(map[string]any{"effort": "high"}); !strings.Contains(got, "high effort") {
		t.Fatalf("prompt=%q", got)
	}
	if got := responsesReasoningPrompt(map[string]any{"effort": "none"}); got != "" {
		t.Fatalf("none prompt=%q", got)
	}
}

func TestResponsesHandlerRoutesReasoningEffortModel(t *testing.T) {
	am := testMessagesAccountManager(t)
	_, _ = am.AddAccount("first@example.com", "tok-a", "u1", "", "")
	fake := &fakeMessagesDirectClient{}
	handler := ResponsesHandler(am, fake, nil, nil)
	body := `{"model":"claude-opus-4-7","reasoning":{"effort":"max"},"input":"hi"}`
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(fake.calls) != 1 || fake.calls[0].Model.ID != "claude-opus-4-7-max" {
		t.Fatalf("routed model calls=%+v", fake.calls)
	}
}

func TestWriteResponsesResponseWithToolCall(t *testing.T) {
	rec := httptest.NewRecorder()
	writeResponsesResponse(rec, "claude-sonnet-4.6", &windsurf.ChatResult{
		ToolCalls: []windsurf.ToolCall{{ID: "call_1", Name: "echo_text", ArgumentsJSON: `{"text":"hi"}`}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("json: %v", err)
	}
	out := body["output"].([]any)
	item := out[0].(map[string]any)
	if item["type"] != "function_call" || item["name"] != "echo_text" {
		t.Fatalf("item=%v", item)
	}
	if body["status"] != "incomplete" {
		t.Fatalf("tool call response should be incomplete status=%v", body["status"])
	}
}

func TestResponsesOutputRestoresNativeToolItems(t *testing.T) {
	result := &windsurf.ChatResult{ToolCalls: []windsurf.ToolCall{
		{ID: "call_custom", Name: "runner", ArgumentsJSON: `{"input":"echo hi"}`},
		{ID: "call_search", Name: "web_search", ArgumentsJSON: `{"query":"codex"}`},
		{ID: "call_ns", Name: "mcp__desktop__read_file", ArgumentsJSON: `{"path":"README.md"}`},
	}}
	out := responsesOutputWithMeta(result, map[string]responseToolMeta{
		"runner":                  {Type: "custom", OriginalName: "runner"},
		"web_search":              {Type: "web_search", OriginalName: "web_search"},
		"mcp__desktop__read_file": {Type: "namespace", Namespace: "mcp__desktop", OriginalName: "read_file"},
	})
	if out[0].(map[string]any)["type"] != "custom_tool_call" || out[0].(map[string]any)["input"] != "echo hi" {
		t.Fatalf("custom=%v", out[0])
	}
	if out[1].(map[string]any)["type"] != "web_search_call" {
		t.Fatalf("web=%v", out[1])
	}
	if out[2].(map[string]any)["namespace"] != "mcp__desktop" || out[2].(map[string]any)["name"] != "read_file" {
		t.Fatalf("namespace=%v", out[2])
	}
}

func TestResponsesOutputIncludesReasoningItem(t *testing.T) {
	out := responsesOutputWithMeta(&windsurf.ChatResult{Thinking: "thinking", Text: "answer"}, nil)
	if len(out) != 2 {
		t.Fatalf("out=%v", out)
	}
	reasoning := out[0].(map[string]any)
	if reasoning["type"] != "reasoning" {
		t.Fatalf("reasoning=%v", reasoning)
	}
	summary := reasoning["summary"].([]any)
	if summary[0].(map[string]any)["text"] != "thinking" {
		t.Fatalf("summary=%v", summary)
	}
	if out[1].(map[string]any)["type"] != "message" {
		t.Fatalf("message=%v", out[1])
	}
}

func TestResponsesToolChoiceAlias(t *testing.T) {
	choice, err := responsesToolChoiceToDirect(map[string]any{"type": "function", "name": "echo_text"})
	if err != nil {
		t.Fatal(err)
	}
	if choice == nil || choice.ToolName != "echo_text" {
		t.Fatalf("choice=%+v", choice)
	}
}

func TestResponsesToolChoiceForDroppedServerToolIsPruned(t *testing.T) {
	tools, _, err := responsesToolsToDirectWithMeta([]any{map[string]any{"type": "file_search"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 0 {
		t.Fatalf("file_search should not be bridged tools=%+v", tools)
	}
	choice, err := responsesToolChoiceToDirect(map[string]any{"type": "function", "name": "file_search"})
	if err != nil {
		t.Fatal(err)
	}
	if got := pruneDirectToolChoice(choice, tools); got != nil {
		t.Fatalf("choice should be dropped for unbridged tool: %+v", got)
	}
}

func TestResponsesStreamWriterShape(t *testing.T) {
	rec := httptest.NewRecorder()
	sw, err := newResponsesStreamWriter(rec, "claude-sonnet-4.6")
	if err != nil {
		t.Fatal(err)
	}
	if err := sw.TextDelta("hi"); err != nil {
		t.Fatal(err)
	}
	if err := sw.Finish(&windsurf.ChatResult{Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	for _, want := range []string{"event: response.created", "event: response.output_item.added", "event: response.output_text.done", "event: response.output_item.done", "event: response.completed"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in %q", want, body)
		}
	}
	if !strings.Contains(body, `"status":"completed"`) || !strings.Contains(body, `"content":[{"text":"hi","type":"output_text"}]`) {
		t.Fatalf("missing completed text item in %q", body)
	}
}

func TestResponsesStreamWriterToolCallShape(t *testing.T) {
	rec := httptest.NewRecorder()
	sw, err := newResponsesStreamWriter(rec, "claude-sonnet-4.6")
	if err != nil {
		t.Fatal(err)
	}
	if err := sw.ToolCallDelta(0, windsurf.ToolCall{ID: "call_1", Name: "echo_text"}); err != nil {
		t.Fatal(err)
	}
	if err := sw.ToolCallDelta(0, windsurf.ToolCall{ArgumentsJSON: `{"text":"hi"}`}); err != nil {
		t.Fatal(err)
	}
	if err := sw.Finish(&windsurf.ChatResult{ToolCalls: []windsurf.ToolCall{{ID: "call_1", Name: "echo_text", ArgumentsJSON: `{"text":"hi"}`}}}); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	for _, want := range []string{"event: response.output_item.added", `"type":"function_call"`, "event: response.function_call_arguments.delta", "event: response.function_call_arguments.done", "event: response.output_item.done", "event: response.completed"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in %q", want, body)
		}
	}
	if !strings.Contains(body, `"arguments":"{\"text\":\"hi\"}"`) || !strings.Contains(body, `"name":"echo_text"`) {
		t.Fatalf("missing completed function call in %q", body)
	}
}

func TestResponsesStreamWriterReasoningShape(t *testing.T) {
	rec := httptest.NewRecorder()
	sw, err := newResponsesStreamWriter(rec, "claude-sonnet-4.6")
	if err != nil {
		t.Fatal(err)
	}
	if err := sw.ThinkingDelta("think"); err != nil {
		t.Fatal(err)
	}
	if err := sw.TextDelta("answer"); err != nil {
		t.Fatal(err)
	}
	if err := sw.Finish(&windsurf.ChatResult{Thinking: "think", Text: "answer"}); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	for _, want := range []string{"event: response.reasoning_summary_text.delta", "event: response.reasoning_summary_text.done", `"type":"reasoning"`, `"text":"think"`, "event: response.output_text.done"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in %q", want, body)
		}
	}
	if !strings.Contains(body, `"output_index":0`) || !strings.Contains(body, `"output_index":1`) {
		t.Fatalf("expected ordered reasoning/text output indexes body=%q", body)
	}
}

func TestResponsesStreamWriterBuffersArgsBeforeToolName(t *testing.T) {
	rec := httptest.NewRecorder()
	sw, err := newResponsesStreamWriter(rec, "claude-sonnet-4.6", map[string]responseToolMeta{
		"runner": {Type: "custom", OriginalName: "runner"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := sw.ToolCallDelta(0, windsurf.ToolCall{ArgumentsJSON: `{"input":"echo `}); err != nil {
		t.Fatal(err)
	}
	if err := sw.ToolCallDelta(0, windsurf.ToolCall{ID: "call_custom", Name: "runner", ArgumentsJSON: `hi"}`}); err != nil {
		t.Fatal(err)
	}
	if err := sw.Finish(&windsurf.ChatResult{ToolCalls: []windsurf.ToolCall{{ID: "call_custom", Name: "runner", ArgumentsJSON: `{"input":"echo hi"}`}}}); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	firstAdded := strings.Index(body, "response.output_item.added")
	firstArgs := strings.Index(body, `{\"input\":\"echo `)
	if firstAdded == -1 || firstArgs == -1 || firstArgs > firstAdded {
		t.Fatalf("expected args delta before item start when name is late body=%q", body)
	}
	if !strings.Contains(body, `"type":"custom_tool_call"`) || !strings.Contains(body, `"input":"echo hi"`) {
		t.Fatalf("missing restored custom item body=%q", body)
	}
}

func TestResponsesStreamWriterArgsBeforeToolNameAfterReasoningKeepsIndex(t *testing.T) {
	rec := httptest.NewRecorder()
	sw, err := newResponsesStreamWriter(rec, "claude-sonnet-4.6")
	if err != nil {
		t.Fatal(err)
	}
	if err := sw.ThinkingDelta("think"); err != nil {
		t.Fatal(err)
	}
	if err := sw.ToolCallDelta(0, windsurf.ToolCall{ArgumentsJSON: `{"text":"hi"}`}); err != nil {
		t.Fatal(err)
	}
	if err := sw.ToolCallDelta(0, windsurf.ToolCall{ID: "call_1", Name: "echo_text"}); err != nil {
		t.Fatal(err)
	}
	if err := sw.Finish(&windsurf.ChatResult{Thinking: "think", ToolCalls: []windsurf.ToolCall{{ID: "call_1", Name: "echo_text", ArgumentsJSON: `{"text":"hi"}`}}}); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	if strings.Count(body, `"output_index":1`) < 3 {
		t.Fatalf("expected pending args, item added, and done to share tool output index body=%q", body)
	}
	if !strings.Contains(body, `"output_index":0`) {
		t.Fatalf("expected reasoning to keep output index 0 body=%q", body)
	}
}

func TestResponsesStreamWriterStructuredError(t *testing.T) {
	rec := httptest.NewRecorder()
	sw, err := newResponsesStreamWriter(rec, "claude-sonnet-4.6")
	if err != nil {
		t.Fatal(err)
	}
	if err := sw.TextDelta("partial"); err != nil {
		t.Fatal(err)
	}
	if err := sw.Error(account.ErrorTransport, errors.New("dial tcp")); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "event: response.error") || !strings.Contains(body, `"type":"transport"`) || !strings.Contains(body, `"message":"dial tcp"`) {
		t.Fatalf("body=%q", body)
	}
}

func TestResponsesStreamWriterStructuredErrorRedactsSecrets(t *testing.T) {
	rec := httptest.NewRecorder()
	sw, err := newResponsesStreamWriter(rec, "claude-sonnet-4.6")
	if err != nil {
		t.Fatal(err)
	}
	if err := sw.Error(account.ErrorTransport, errors.New(`api_key=sk-test_abcdefghijklmnopqrstuvwxyz email=user@example.com`)); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	if strings.Contains(body, "sk-test_abcdefghijklmnopqrstuvwxyz") || strings.Contains(body, "user@example.com") {
		t.Fatalf("secret leaked in responses stream error: %s", body)
	}
	if !strings.Contains(body, "[REDACTED]") || !strings.Contains(body, "[REDACTED_EMAIL]") {
		t.Fatalf("missing redaction markers: %s", body)
	}
}

func TestWriteResponsesResponseSanitizesWorkspacePaths(t *testing.T) {
	rec := httptest.NewRecorder()
	writeResponsesResponse(rec, "claude-sonnet-4.6", &windsurf.ChatResult{
		Text:         "read /tmp/windsurf-workspace/a.txt",
		Thinking:     "thinking /home/user/projects/workspace-abc123/b.txt",
		FinishReason: "tool_calls",
		ToolCalls: []windsurf.ToolCall{{
			ID:            "call_1",
			Name:          "Read",
			ArgumentsJSON: `{"file_path":"/home/user/projects/workspace-abc123/b.txt"}`,
		}},
	})
	body := rec.Body.String()
	if strings.Contains(body, "windsurf-workspace") || strings.Contains(body, "workspace-abc123") {
		t.Fatalf("workspace path leaked: %s", body)
	}
	if !strings.Contains(body, "<workspace>") {
		t.Fatalf("missing workspace marker: %s", body)
	}
}

func TestResponsesStreamWriterSanitizesWorkspacePaths(t *testing.T) {
	rec := httptest.NewRecorder()
	sw, err := newResponsesStreamWriter(rec, "claude-sonnet-4.6")
	if err != nil {
		t.Fatal(err)
	}
	if err := sw.TextDelta("see /tmp/windsurf-workspace/a.txt"); err != nil {
		t.Fatal(err)
	}
	if err := sw.ToolCallDelta(0, windsurf.ToolCall{ID: "call_1", Name: "Read", ArgumentsJSON: `{"file_path":"/home/user/projects/workspace-abc123/a.txt"}`}); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	if strings.Contains(body, "windsurf-workspace") || strings.Contains(body, "workspace-abc123") {
		t.Fatalf("workspace path leaked in stream: %s", body)
	}
}

func TestResponsesToolContinuationReusesFirstLegAccount(t *testing.T) {
	am := testMessagesAccountManager(t)
	id1, _ := am.AddAccount("first@example.com", "tok-a", "u1", "", "")
	id2, _ := am.AddAccount("second@example.com", "tok-b", "u2", "", "")
	_ = am.UpdateAccount(int(id1), map[string]interface{}{"quota_daily_percent": 90, "quota_weekly_percent": 90})
	_ = am.UpdateAccount(int(id2), map[string]interface{}{"quota_daily_percent": 10, "quota_weekly_percent": 10})

	rp := reusepool.NewPool()
	model := models.GetModelByID("claude-sonnet-4.6")
	client := &fakeMessagesDirectClient{results: []*windsurf.ChatResult{
		{FinishReason: "tool_calls", ToolCalls: []windsurf.ToolCall{{ID: "call_1", Name: "echo_text", ArgumentsJSON: `{"text":"hi"}`}}},
		{Text: "done", FinishReason: "stop"},
	}}
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	req.Header.Set("X-Caller-Key", "caller-responses")
	params := directChatParams{
		Model:     model,
		Messages:  []windsurf.ChatMessage{{Role: "user", Content: "echo"}},
		CallerKey: "caller-responses",
		Route:     "responses",
		Tools:     []direct.ToolDefinition{{Name: "echo_text", SchemaJSON: `{"type":"object"}`}},
	}
	result, status, err := executeDirectChat(req, client, am, rp, params)
	if err != nil || status != http.StatusOK || len(result.ToolCalls) != 1 {
		t.Fatalf("first result=%+v status=%d err=%v", result, status, err)
	}

	_ = am.UpdateAccount(int(id1), map[string]interface{}{"quota_daily_percent": 1, "quota_weekly_percent": 1})
	params.Tools = nil
	params.Messages = []windsurf.ChatMessage{
		{Role: "user", Content: "echo"},
		{Role: "assistant", ToolCalls: result.ToolCalls},
		{Role: "tool", ToolCallID: "call_1", Content: "hi"},
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
