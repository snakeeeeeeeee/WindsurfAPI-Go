package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zhangyu/windsurfapi-go/internal/account"
	"github.com/zhangyu/windsurfapi-go/internal/models"
	reusepool "github.com/zhangyu/windsurfapi-go/internal/reuse"
	"github.com/zhangyu/windsurfapi-go/internal/sse"
	"github.com/zhangyu/windsurfapi-go/internal/store"
	usagepkg "github.com/zhangyu/windsurfapi-go/internal/usage"
	"github.com/zhangyu/windsurfapi-go/internal/windsurf"
	"github.com/zhangyu/windsurfapi-go/internal/windsurf/direct"
)

type fakeDirectChatClient struct {
	calls []direct.ChatRequest
}

func (f *fakeDirectChatClient) Chat(_ context.Context, req direct.ChatRequest) (*windsurf.ChatResult, error) {
	f.calls = append(f.calls, req)
	return &windsurf.ChatResult{Text: "ok", FinishReason: "stop"}, nil
}

type sequenceDirectChatClient struct {
	results []*windsurf.ChatResult
	errors  []error
	calls   []direct.ChatRequest
}

func (f *sequenceDirectChatClient) Chat(_ context.Context, req direct.ChatRequest) (*windsurf.ChatResult, error) {
	f.calls = append(f.calls, req)
	if len(f.errors) > 0 {
		err := f.errors[0]
		f.errors = f.errors[1:]
		if err != nil {
			return nil, err
		}
	}
	if len(f.results) == 0 {
		return &windsurf.ChatResult{Text: "ok", FinishReason: "stop"}, nil
	}
	res := f.results[0]
	f.results = f.results[1:]
	return res, nil
}

type streamFailDirectChatClient struct {
	calls    []direct.ChatRequest
	failOnce bool
}

func (f *streamFailDirectChatClient) Chat(_ context.Context, req direct.ChatRequest) (*windsurf.ChatResult, error) {
	f.calls = append(f.calls, req)
	if f.failOnce {
		f.failOnce = false
		return nil, errors.New("connection refused before first delta")
	}
	if req.OnFirstDelta != nil {
		req.OnFirstDelta()
	}
	if req.OnDelta != nil {
		if err := req.OnDelta("ok"); err != nil {
			return nil, err
		}
	}
	return &windsurf.ChatResult{Text: "ok", FinishReason: "stop"}, nil
}

type streamAfterDeltaErrorClient struct {
	calls []direct.ChatRequest
}

func (f *streamAfterDeltaErrorClient) Chat(_ context.Context, req direct.ChatRequest) (*windsurf.ChatResult, error) {
	f.calls = append(f.calls, req)
	if req.OnFirstDelta != nil {
		req.OnFirstDelta()
	}
	if req.OnDelta != nil {
		if err := req.OnDelta("partial"); err != nil {
			return nil, err
		}
	}
	return nil, errors.New("connection refused after first delta")
}

func TestHasToolUse(t *testing.T) {
	tools, err := toDirectTools([]Tool{{Type: "function", Function: ToolFunction{
		Name:        "read_file",
		Description: "Read file",
		Parameters:  map[string]any{"type": "object"},
	}}})
	if err != nil {
		t.Fatalf("toDirectTools: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "read_file" || tools[0].SchemaJSON != `{"type":"object"}` {
		t.Fatalf("tools=%+v", tools)
	}

	choice, err := toDirectToolChoice(map[string]any{"type": "function", "function": map[string]any{"name": "read_file"}})
	if err != nil {
		t.Fatalf("toDirectToolChoice: %v", err)
	}
	if choice == nil || choice.ToolName != "read_file" {
		t.Fatalf("choice=%+v", choice)
	}

	choice, err = toDirectToolChoice("none")
	if err != nil {
		t.Fatalf("string tool choice: %v", err)
	}
	if choice == nil || choice.OptionName != "none" {
		t.Fatalf("string choice=%+v", choice)
	}

	choice, err = toDirectToolChoice(map[string]any{"type": "tool", "name": "echo_text"})
	if err != nil {
		t.Fatalf("tool choice alias: %v", err)
	}
	if choice == nil || choice.ToolName != "echo_text" {
		t.Fatalf("tool alias choice=%+v", choice)
	}
}

func TestPruneDirectToolChoiceDropsUnavailableNamedTool(t *testing.T) {
	choice := &direct.ToolChoice{ToolName: "web_search"}
	if got := pruneDirectToolChoice(choice, nil); got != nil {
		t.Fatalf("choice should be dropped without tools: %+v", got)
	}
	if got := pruneDirectToolChoice(choice, []direct.ToolDefinition{{Name: "read_file"}}); got != nil {
		t.Fatalf("choice should be dropped when tool is unavailable: %+v", got)
	}
	if got := pruneDirectToolChoice(choice, []direct.ToolDefinition{{Name: "web_search"}}); got == nil || got.ToolName != "web_search" {
		t.Fatalf("choice should survive when tool exists: %+v", got)
	}
	required := &direct.ToolChoice{OptionName: "required"}
	if got := pruneDirectToolChoice(required, []direct.ToolDefinition{{Name: "read_file"}}); got != required {
		t.Fatalf("option choice should survive with tools: %+v", got)
	}
}

func TestCallerKeyForBodyScopesSharedAPIKey(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.RemoteAddr = "10.0.0.8:12345"
	req.Header.Set("Authorization", "Bearer sk-shared")
	req.Header.Set("User-Agent", "ClaudeCode/2")

	a := callerKeyForBody(req, ChatCompletionRequest{User: "alice"})
	b := callerKeyForBody(req, ChatCompletionRequest{User: "bob"})
	if a == b || !strings.Contains(a, ":user:") || !strings.Contains(b, ":user:") {
		t.Fatalf("caller keys should be user-scoped a=%q b=%q", a, b)
	}
	plain := callerKeyForBody(req, ChatCompletionRequest{})
	if !strings.Contains(plain, ":client:") || strings.Contains(plain, "sk-shared") {
		t.Fatalf("plain key should be hashed client-scoped without raw secret: %q", plain)
	}
}

func TestCallerKeyForBodyUsesAnthropicMetadataUserID(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.RemoteAddr = "10.0.0.8:12345"
	req.Header.Set("Authorization", "Bearer sk-shared")
	body := MessagesRequest{Metadata: map[string]any{"user_id": `{"device_id":"device-a","session_id":"session-a"}`}}
	got := callerKeyForBody(req, body)
	if !strings.Contains(got, ":user:") {
		t.Fatalf("metadata.user_id should scope caller key: %q", got)
	}
}

func TestResponseFormatHintPrependsSystemOnly(t *testing.T) {
	hint := responseFormatHint(map[string]any{
		"type": "json_schema",
		"json_schema": map[string]any{
			"schema": map[string]any{"type": "object", "properties": map[string]any{"answer": map[string]any{"type": "string"}}},
		},
	})
	if !strings.Contains(hint, "Respond with valid JSON only") || !strings.Contains(hint, `"answer"`) {
		t.Fatalf("hint=%q", hint)
	}
	msgs := []windsurf.ChatMessage{{Role: "user", Content: "say hi"}}
	out := prependSystemHint(msgs, hint)
	if len(out) != 2 || out[0].Role != "system" || out[1].Content != "say hi" {
		t.Fatalf("out=%+v", out)
	}
	if msgs[0].Content != "say hi" {
		t.Fatalf("original user message mutated: %+v", msgs)
	}
}

func TestOpenAIReasoningPrompt(t *testing.T) {
	got := openAIReasoningPrompt(ChatCompletionRequest{ReasoningEffort: "high"})
	if !strings.Contains(got, "high effort") || !strings.Contains(got, "reasoning_content") {
		t.Fatalf("high effort prompt=%q", got)
	}
	got = openAIReasoningPrompt(ChatCompletionRequest{Reasoning: map[string]any{"effort": "medium"}})
	if !strings.Contains(got, "medium effort") {
		t.Fatalf("medium effort prompt=%q", got)
	}
	if got := openAIReasoningPrompt(ChatCompletionRequest{ReasoningEffort: "disabled"}); got != "" {
		t.Fatalf("disabled reasoning should be empty, got=%q", got)
	}
}

func TestExecuteOpenAIChatPassesReasoningPrompt(t *testing.T) {
	am := testChatAccountManager(t)
	_, _ = am.AddAccount("first@example.com", "tok-a", "u1", "", "")
	fake := &fakeDirectChatClient{}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	result, status, err := executeOpenAIChat(req, fake, am, nil, models.GetModelByID("claude-sonnet-4.6"), "claude-sonnet-4.6", []windsurf.ChatMessage{{Role: "user", Content: "hi"}}, "caller-reasoning", false, httptest.NewRecorder(), nil, nil, nil, false, nil, nil, "think harder", 0)
	if err != nil || status != http.StatusOK || result.Text != "ok" {
		t.Fatalf("result=%+v status=%d err=%v", result, status, err)
	}
	if len(fake.calls) != 1 || fake.calls[0].Thinking != "think harder" {
		t.Fatalf("thinking not passed calls=%+v", fake.calls)
	}
}

func TestChatHandlerRoutesReasoningEffortModel(t *testing.T) {
	am := testChatAccountManager(t)
	_, _ = am.AddAccount("first@example.com", "tok-a", "u1", "", "")
	fake := &fakeDirectChatClient{}
	handler := ChatCompletionsHandler(nil, am, fake, nil, nil)
	body := `{"model":"claude-opus-4-7","reasoning_effort":"xhigh","messages":[{"role":"user","content":"hi"}]}`
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(fake.calls) != 1 || fake.calls[0].Model.ID != "claude-opus-4-7-xhigh" {
		t.Fatalf("routed model calls=%+v", fake.calls)
	}
	if fake.calls[0].Model.ModelUID != "claude-opus-4-7-xhigh" {
		t.Fatalf("model uid=%q", fake.calls[0].Model.ModelUID)
	}
}

func TestChatHandlerKeepsOpus47ModelForToolRequests(t *testing.T) {
	am := testChatAccountManager(t)
	_, _ = am.AddAccount("first@example.com", "tok-a", "u1", "", "")
	fake := &fakeDirectChatClient{}
	handler := ChatCompletionsHandler(nil, am, fake, nil, nil)
	body := `{"model":"claude-opus-4-7","reasoning_effort":"xhigh","messages":[{"role":"user","content":"call tool"}],"tools":[{"type":"function","function":{"name":"echo_text","parameters":{"type":"object"}}}]}`
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))

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

func TestExecuteDirectChatStoresAndHitsStickyReuseAccount(t *testing.T) {
	am := testChatAccountManager(t)
	id1, _ := am.AddAccount("first@example.com", "tok-a", "u1", "", "")
	id2, _ := am.AddAccount("second@example.com", "tok-b", "u2", "", "")
	_ = am.UpdateAccount(int(id1), map[string]interface{}{"quota_daily_percent": 90, "quota_weekly_percent": 90})
	_ = am.UpdateAccount(int(id2), map[string]interface{}{"quota_daily_percent": 10, "quota_weekly_percent": 10})

	rp := reusepool.NewPool()
	fake := &fakeDirectChatClient{}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("X-Caller-Key", "caller-a")
	params := directChatParams{
		Model:     models.GetModelByID("claude-sonnet-4.6"),
		Messages:  []windsurf.ChatMessage{{Role: "user", Content: "hi"}},
		CallerKey: "caller-a",
		Route:     "chat_completions",
	}
	result, status, err := executeDirectChat(req, fake, am, rp, params)
	if err != nil || status != http.StatusOK || result.Text != "ok" {
		t.Fatalf("first result=%+v status=%d err=%v", result, status, err)
	}
	if len(rp.Snapshot()) == 0 || rp.Stats().Stores == 0 {
		t.Fatalf("reuse was not stored stats=%+v entries=%+v", rp.Stats(), rp.Snapshot())
	}

	_ = am.UpdateAccount(int(id1), map[string]interface{}{"quota_daily_percent": 1, "quota_weekly_percent": 1})
	_, status, err = executeDirectChat(req, fake, am, rp, params)
	if err != nil || status != http.StatusOK {
		t.Fatalf("second status=%d err=%v", status, err)
	}
	if rp.Stats().Hits != 1 {
		t.Fatalf("expected reuse hit stats=%+v", rp.Stats())
	}
	snap := am.Snapshot()
	var first account.DebugAccount
	for _, row := range snap.Accounts {
		if row.ID == int(id1) {
			first = row
		}
	}
	if first.RPMUsed != 2 {
		t.Fatalf("expected sticky reuse to reserve first account twice, first=%+v all=%+v", first, snap.Accounts)
	}
}

func TestExecuteDirectChatStrictReuseReturns429WhenStickyAccountUnavailable(t *testing.T) {
	am := testChatAccountManager(t)
	id, _ := am.AddAccount("first@example.com", "tok-a", "u1", "", "")
	rp := reusepool.NewPool()
	model := models.GetModelByID("claude-sonnet-4.6")
	messages := []windsurf.ChatMessage{{Role: "user", Content: "hi"}}
	fp := reusepool.FingerprintWithOptions(model.ID, "caller-a", "chat_completions", "", "", messages)
	rp.Checkin(fp, &reusepool.Entry{AccountID: int(id), APIKeyHash: "hash", ModelID: model.ID, CallerKey: "caller-a"}, time.Minute)
	if err := am.MarkCooldown(int(id), model.ID, time.Now().Add(time.Minute), "rate limited"); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	params := directChatParams{Model: model, Messages: messages, CallerKey: "caller-a", Route: "chat_completions", StrictReuse: true}
	_, status, err := executeDirectChat(req, &fakeDirectChatClient{}, am, rp, params)
	if err == nil || status != http.StatusTooManyRequests {
		t.Fatalf("status=%d err=%v", status, err)
	}
	if rp.Stats().Hits != 1 {
		t.Fatalf("strict reuse should have checked out existing entry stats=%+v", rp.Stats())
	}
}

func TestExecuteDirectChatLocalTestAccountIDsLimitCandidates(t *testing.T) {
	am := testChatAccountManager(t)
	id1, _ := am.AddAccount("first@example.com", "tok-a", "u1", "", "")
	id2, _ := am.AddAccount("second@example.com", "tok-b", "u2", "", "")
	_ = am.UpdateAccount(int(id1), map[string]interface{}{"quota_daily_percent": 100, "quota_weekly_percent": 100})
	_ = am.UpdateAccount(int(id2), map[string]interface{}{"quota_daily_percent": 10, "quota_weekly_percent": 10})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Windsurf-Test-Account-IDs", strconv.Itoa(int(id2)))
	params := directChatParams{
		Model:          models.GetModelByID("claude-sonnet-4.6"),
		Messages:       []windsurf.ChatMessage{{Role: "user", Content: "hi"}},
		CallerKey:      "caller-test-subset",
		Route:          "chat_completions",
		TestAccountIDs: testAccountIDsFromRequest(req),
	}
	result, status, err := executeDirectChat(req, &fakeDirectChatClient{}, am, nil, params)
	if err != nil || status != http.StatusOK || result.Text != "ok" {
		t.Fatalf("result=%+v status=%d err=%v", result, status, err)
	}
	snap := am.Snapshot()
	for _, row := range snap.Accounts {
		if row.ID == int(id1) && row.RPMUsed != 0 {
			t.Fatalf("local test header should not use first account: %+v", row)
		}
		if row.ID == int(id2) && row.RPMUsed != 1 {
			t.Fatalf("local test header should use second account: %+v", row)
		}
	}
}

func TestTestAccountIDsHeaderIgnoredForRemoteRequests(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.RemoteAddr = "203.0.113.20:12345"
	req.Header.Set("X-Windsurf-Test-Account-IDs", "2")
	if got := testAccountIDsFromRequest(req); len(got) != 0 {
		t.Fatalf("remote test account header should be ignored: %v", got)
	}
}

func TestExecuteDirectChatToolContinuationReusesFirstLegAccount(t *testing.T) {
	am := testChatAccountManager(t)
	id1, _ := am.AddAccount("first@example.com", "tok-a", "u1", "", "")
	id2, _ := am.AddAccount("second@example.com", "tok-b", "u2", "", "")
	_ = am.UpdateAccount(int(id1), map[string]interface{}{"quota_daily_percent": 90, "quota_weekly_percent": 90})
	_ = am.UpdateAccount(int(id2), map[string]interface{}{"quota_daily_percent": 10, "quota_weekly_percent": 10})

	rp := reusepool.NewPool()
	model := models.GetModelByID("claude-sonnet-4.6")
	firstMsgs := []windsurf.ChatMessage{{Role: "user", Content: "read both files"}}
	fake := &sequenceDirectChatClient{results: []*windsurf.ChatResult{{
		FinishReason: "tool_calls",
		ToolCalls: []windsurf.ToolCall{
			{ID: "toolu_1", Name: "read_file", ArgumentsJSON: `{"path":"a.txt"}`},
			{ID: "toolu_2", Name: "read_file", ArgumentsJSON: `{"path":"b.txt"}`},
		},
	}, {Text: "done", FinishReason: "stop"}}}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("X-Caller-Key", "caller-tools")
	params := directChatParams{
		Model:     model,
		Messages:  firstMsgs,
		CallerKey: "caller-tools",
		Route:     "chat_completions",
		Tools: []direct.ToolDefinition{{
			Name:       "read_file",
			SchemaJSON: `{"type":"object","properties":{"path":{"type":"string"}}}`,
		}},
	}
	result, status, err := executeDirectChat(req, fake, am, rp, params)
	if err != nil || status != http.StatusOK || len(result.ToolCalls) != 2 {
		t.Fatalf("first result=%+v status=%d err=%v", result, status, err)
	}

	_ = am.UpdateAccount(int(id1), map[string]interface{}{"quota_daily_percent": 1, "quota_weekly_percent": 1})
	params.Messages = []windsurf.ChatMessage{
		{Role: "user", Content: "read both files"},
		{Role: "assistant", Content: "", ToolCalls: result.ToolCalls},
		{Role: "tool", ToolCallID: "toolu_1", Content: "a"},
		{Role: "tool", ToolCallID: "toolu_2", Content: "b"},
	}
	params.Tools = nil
	result, status, err = executeDirectChat(req, fake, am, rp, params)
	if err != nil || status != http.StatusOK || result.Text != "done" {
		t.Fatalf("second result=%+v status=%d err=%v", result, status, err)
	}
	if rp.Stats().Hits != 1 {
		t.Fatalf("expected tool continuation reuse hit stats=%+v", rp.Stats())
	}
	snap := am.Snapshot()
	var first, second account.DebugAccount
	for _, row := range snap.Accounts {
		if row.ID == int(id1) {
			first = row
		}
		if row.ID == int(id2) {
			second = row
		}
	}
	if first.RPMUsed != 2 || second.RPMUsed != 0 {
		t.Fatalf("expected sticky account reuse first=%+v second=%+v all=%+v", first, second, snap.Accounts)
	}
}

func TestExecuteDirectChatStreamRetriesBeforeFirstDelta(t *testing.T) {
	am := testChatAccountManager(t)
	id1, _ := am.AddAccount("first@example.com", "tok-a", "u1", "", "")
	id2, _ := am.AddAccount("second@example.com", "tok-b", "u2", "", "")
	_ = id1
	_ = id2
	model := models.GetModelByID("claude-sonnet-4.6")
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	fake := &streamFailDirectChatClient{failOnce: true}
	var deltas []string
	params := directChatParams{
		Model:     model,
		Messages:  []windsurf.ChatMessage{{Role: "user", Content: "hi"}},
		CallerKey: "caller-stream",
		Route:     "chat_completions",
		Stream:    true,
		OnTextDelta: func(delta string) error {
			deltas = append(deltas, delta)
			return nil
		},
		OnStreamError: func(class account.ErrorClass, err error) error {
			deltas = append(deltas, "error:"+string(class)+":"+err.Error())
			return nil
		},
		OnStreamFinish: func(_ *windsurf.ChatResult) error {
			deltas = append(deltas, "[finish]")
			return nil
		},
	}
	result, status, err := executeDirectChat(req, fake, am, reusepool.NewPool(), params)
	if err != nil || status != http.StatusOK || result.Text != "ok" {
		t.Fatalf("result=%+v status=%d err=%v", result, status, err)
	}
	if len(fake.calls) != 2 {
		t.Fatalf("expected retry before first delta calls=%d", len(fake.calls))
	}
	if strings.Join(deltas, "|") != "ok|[finish]" {
		t.Fatalf("deltas=%v", deltas)
	}
	snap := am.Snapshot()
	for _, row := range snap.Accounts {
		if row.Inflight != 0 {
			t.Fatalf("inflight leaked row=%+v", row)
		}
	}
}

func TestExecuteDirectChatStreamDoesNotRetryAfterFirstDelta(t *testing.T) {
	am := testChatAccountManager(t)
	_, _ = am.AddAccount("first@example.com", "tok-a", "u1", "", "")
	_, _ = am.AddAccount("second@example.com", "tok-b", "u2", "", "")
	model := models.GetModelByID("claude-sonnet-4.6")
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	fake := &streamAfterDeltaErrorClient{}
	var deltas []string
	params := directChatParams{
		Model:     model,
		Messages:  []windsurf.ChatMessage{{Role: "user", Content: "hi"}},
		CallerKey: "caller-stream",
		Route:     "chat_completions",
		Stream:    true,
		OnTextDelta: func(delta string) error {
			deltas = append(deltas, delta)
			return nil
		},
		OnStreamError: func(class account.ErrorClass, err error) error {
			deltas = append(deltas, "error:"+string(class)+":"+err.Error())
			return nil
		},
		OnStreamFinish: func(_ *windsurf.ChatResult) error {
			deltas = append(deltas, "[finish]")
			return nil
		},
	}
	result, status, err := executeDirectChat(req, fake, am, reusepool.NewPool(), params)
	if err != nil || status != http.StatusOK || result != nil {
		t.Fatalf("result=%+v status=%d err=%v", result, status, err)
	}
	if len(fake.calls) != 1 {
		t.Fatalf("must not retry after first delta calls=%d", len(fake.calls))
	}
	body := strings.Join(deltas, "|")
	if !strings.Contains(body, "partial") || !strings.Contains(body, "error:transport:connection refused after first delta") || !strings.Contains(body, "[finish]") {
		t.Fatalf("deltas=%v", deltas)
	}
	snap := am.Snapshot()
	for _, row := range snap.Accounts {
		if row.Inflight != 0 {
			t.Fatalf("inflight leaked row=%+v", row)
		}
	}
}

func testChatAccountManager(t *testing.T) *account.Manager {
	t.Helper()
	sqliteStore, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "windsurf.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqliteStore.Close() })
	return account.NewManager(sqliteStore)
}

func TestToDirectToolsRejectsUnsupportedType(t *testing.T) {
	_, err := toDirectTools([]Tool{{Type: "web_search", Function: ToolFunction{Name: "search"}}})
	if err == nil || !strings.Contains(err.Error(), "unsupported tool type") {
		t.Fatalf("err=%v", err)
	}
}

func TestShouldSuppressToolsForContinuation(t *testing.T) {
	msgs := []ChatMessage{
		{Role: "user", Content: "call tool"},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "call_1"}}},
		{Role: "tool", ToolCallID: "call_1", Content: "ok"},
	}
	if !shouldSuppressToolsForContinuation(msgs, nil) {
		t.Fatal("expected tool suppression after tool result")
	}
	if shouldSuppressToolsForContinuation(msgs, &direct.ToolChoice{ToolName: "echo_text"}) {
		t.Fatal("forced function choice should keep tools available")
	}
	if shouldSuppressToolsForContinuation([]ChatMessage{{Role: "user", Content: "hi"}}, nil) {
		t.Fatal("plain user turn should not suppress tools")
	}
}

func TestDirectRequestShapeDistinguishesAutoAndNoneToolChoice(t *testing.T) {
	params := directChatParams{Tools: []direct.ToolDefinition{{Name: "echo_text"}}}
	if got := directRequestShape(params); !strings.Contains(got, "tool_choice=auto") {
		t.Fatalf("shape=%q", got)
	}
	params.ToolChoice = &direct.ToolChoice{OptionName: "none"}
	if got := directRequestShape(params); !strings.Contains(got, "tool_choice=none") {
		t.Fatalf("shape=%q", got)
	}
}

func TestWriteUnaryResponseWithToolCalls(t *testing.T) {
	rec := httptest.NewRecorder()
	writeUnaryResponse(rec, "claude-sonnet-4.6", &windsurf.ChatResult{
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
	choices := body["choices"].([]any)
	choice := choices[0].(map[string]any)
	if choice["finish_reason"] != "tool_calls" {
		t.Fatalf("finish_reason=%v", choice["finish_reason"])
	}
	message := choice["message"].(map[string]any)
	calls := message["tool_calls"].([]any)
	call := calls[0].(map[string]any)
	fn := call["function"].(map[string]any)
	if call["id"] != "toolu_1" || fn["name"] != "echo_text" || fn["arguments"] != `{"text":"hi"}` {
		t.Fatalf("tool call=%v", call)
	}
}

func TestWriteUnaryResponseIncludesReasoningContent(t *testing.T) {
	rec := httptest.NewRecorder()
	writeUnaryResponse(rec, "claude-sonnet-4.6", &windsurf.ChatResult{Text: "answer", Thinking: "private summary", FinishReason: "stop"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("json: %v", err)
	}
	choice := body["choices"].([]any)[0].(map[string]any)
	message := choice["message"].(map[string]any)
	if message["content"] != "answer" || message["reasoning_content"] != "private summary" {
		t.Fatalf("message=%v", message)
	}
}

func TestWriteUnaryResponseSanitizesWorkspacePaths(t *testing.T) {
	rec := httptest.NewRecorder()
	writeUnaryResponse(rec, "claude-sonnet-4.6", &windsurf.ChatResult{
		Text:         "look at /tmp/windsurf-workspace/src/index.js",
		Thinking:     "thinking about /home/user/projects/workspace-abc123/a.go",
		FinishReason: "tool_calls",
		ToolCalls: []windsurf.ToolCall{{
			ID:            "call_1",
			Name:          "Read",
			ArgumentsJSON: `{"file_path":"/home/user/projects/workspace-abc123/a.go"}`,
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

func TestUsageMapDoesNotExposeUnverifiedCacheFields(t *testing.T) {
	got := usageMap(usagepkg.FromUpstream(&windsurf.Usage{InputTokens: 100, OutputTokens: 20, CacheReadTokens: 40, CacheWriteTokens: 10}, 0))
	if got["prompt_tokens"] != uint64(100) || got["completion_tokens"] != uint64(20) || got["total_tokens"] != uint64(120) {
		t.Fatalf("base usage=%v", got)
	}
	if _, ok := got["cache_read_input_tokens"]; ok {
		t.Fatalf("cache read should not be exposed by default: %v", got)
	}
	if _, ok := got["cache_creation_input_tokens"]; ok {
		t.Fatalf("cache creation should not be exposed by default: %v", got)
	}
}

func TestOpenAISSEWriterToolCallShape(t *testing.T) {
	rec := httptest.NewRecorder()
	sw, err := sse.NewWriter(rec, "claude-sonnet-4.6")
	if err != nil {
		t.Fatal(err)
	}
	if err := sw.Role(); err != nil {
		t.Fatal(err)
	}
	if err := sw.ToolCallDelta(0, "call_1", "echo_text", ""); err != nil {
		t.Fatal(err)
	}
	if err := sw.ToolCallDelta(0, "", "", `{"text":"hi"}`); err != nil {
		t.Fatal(err)
	}
	if err := sw.Finish("tool_calls", nil); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	for _, want := range []string{`"role":"assistant"`, `"tool_calls"`, `"id":"call_1"`, `"name":"echo_text"`, `"arguments":"{\"text\":\"hi\"}"`, `"finish_reason":"tool_calls"`, "data: [DONE]"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in %q", want, body)
		}
	}
}
