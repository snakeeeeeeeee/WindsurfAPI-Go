package direct

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/zhangyu/windsurfapi-go/internal/models"
	p "github.com/zhangyu/windsurfapi-go/internal/proto"
	"github.com/zhangyu/windsurfapi-go/internal/windsurf"
)

func errInternal() error {
	return errors.New("an internal error occurred")
}

func TestNormalizeUserStatusQuota(t *testing.T) {
	status := normalizeUserStatus(map[string]any{
		"userStatus": map[string]any{
			"planStatus": map[string]any{
				"dailyQuotaRemainingPercent":  42.5,
				"weeklyQuotaRemainingPercent": 88.0,
				"dailyQuotaResetAtUnix":       float64(123),
				"weeklyQuotaResetAtUnix":      "456",
				"overageBalanceMicros":        float64(1230000),
				"planStart":                   "2026-05-01",
				"planEnd":                     "2026-06-01",
				"planInfo": map[string]any{
					"planName":                        "Trial",
					"monthlyPromptCredits":            float64(20000),
					"monthlyFlexCreditPurchaseAmount": float64(30000),
				},
				"availablePromptCredits": float64(15000),
				"usedPromptCredits":      float64(5000),
				"availableFlexCredits":   float64(25000),
				"usedFlexCredits":        float64(5000),
			},
		},
	})
	if status.PlanName != "Trial" {
		t.Fatalf("PlanName=%q", status.PlanName)
	}
	if status.Percent == nil || *status.Percent != 42.5 {
		t.Fatalf("Percent=%v", status.Percent)
	}
	if status.Prompt.Limit == nil || *status.Prompt.Limit != 200 {
		t.Fatalf("Prompt.Limit=%v", status.Prompt.Limit)
	}
	if status.Prompt.Remaining == nil || *status.Prompt.Remaining != 150 {
		t.Fatalf("Prompt.Remaining=%v", status.Prompt.Remaining)
	}
	if status.DailyResetAt == nil || *status.DailyResetAt != 123 {
		t.Fatalf("DailyResetAt=%v", status.DailyResetAt)
	}
	if status.WeeklyResetAt == nil || *status.WeeklyResetAt != 456 {
		t.Fatalf("WeeklyResetAt=%v", status.WeeklyResetAt)
	}
	if status.OverageBalance == nil || *status.OverageBalance != 1.23 {
		t.Fatalf("OverageBalance=%v", status.OverageBalance)
	}
	if status.Flex.Limit == nil || *status.Flex.Limit != 300 {
		t.Fatalf("Flex.Limit=%v", status.Flex.Limit)
	}
	if status.PlanStart != "2026-05-01" || status.PlanEnd != "2026-06-01" {
		t.Fatalf("plan range=%q %q", status.PlanStart, status.PlanEnd)
	}
}

func TestRateLimitDefaults(t *testing.T) {
	rl := &RateLimit{
		HasCapacity:       boolValue(nil, true),
		MessagesRemaining: intValue(nil, -1),
		MaxMessages:       intValue(nil, -1),
	}
	if !rl.HasCapacity || rl.MessagesRemaining != -1 || rl.MaxMessages != -1 {
		t.Fatalf("bad defaults: %+v", rl)
	}
}

func TestBuildAPIGetChatMessageRequest(t *testing.T) {
	model := models.GetModelByID("claude-sonnet-4.6")
	if model == nil {
		t.Fatal("missing test model")
	}
	req := buildAPIGetChatMessageRequest("devin-token", model, "hello direct", 5, nil, nil, false)
	fields, err := p.ParseFields(req)
	if err != nil {
		t.Fatalf("parse request: %v", err)
	}

	requireField(t, fields, 1, p.WireLenDelim)  // metadata
	requireField(t, fields, 2, p.WireLenDelim)  // prompt
	requireField(t, fields, 3, p.WireLenDelim)  // chat_message_prompts
	requireField(t, fields, 7, p.WireVarint)    // request_type
	requireField(t, fields, 14, p.WireLenDelim) // chat_model_name
	requireField(t, fields, 17, p.WireLenDelim) // prompt_id
	requireField(t, fields, 21, p.WireLenDelim) // chat_model_uid

	if got := p.GetField(fields, 2, p.WireLenDelim).String(); got != "hello direct" {
		t.Fatalf("prompt=%q", got)
	}
	if got := p.GetField(fields, 7, p.WireVarint).Uint(); got != 5 {
		t.Fatalf("request_type=%d", got)
	}
	if got := p.GetField(fields, 14, p.WireLenDelim).String(); got != model.CascadeName {
		t.Fatalf("chat_model_name=%q", got)
	}
	if got := p.GetField(fields, 21, p.WireLenDelim).String(); got != model.ModelUID {
		t.Fatalf("chat_model_uid=%q", got)
	}
}

func TestBuildAPIGetChatMessageRequestWithTools(t *testing.T) {
	model := models.GetModelByID("claude-sonnet-4.6")
	if model == nil {
		t.Fatal("missing test model")
	}
	req := buildAPIGetChatMessageRequest("devin-token", model, "call the tool", 5, []ToolDefinition{{
		Name:        "echo_text",
		Description: "Echo text",
		SchemaJSON:  `{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`,
		Strict:      true,
	}}, &ToolChoice{ToolName: "echo_text"}, true)
	fields, err := p.ParseFields(req)
	if err != nil {
		t.Fatalf("parse request: %v", err)
	}
	tool := requireField(t, fields, 10, p.WireLenDelim)
	toolFields, err := p.ParseFields(tool.Bytes())
	if err != nil {
		t.Fatalf("parse tool: %v", err)
	}
	if got := p.GetField(toolFields, 1, p.WireLenDelim).String(); got != "echo_text" {
		t.Fatalf("tool name=%q", got)
	}
	if got := p.GetField(toolFields, 3, p.WireLenDelim).String(); !strings.Contains(got, `"text"`) {
		t.Fatalf("schema=%q", got)
	}
	if !p.GetField(toolFields, 4, p.WireVarint).Bool() {
		t.Fatal("strict=false")
	}
	if !p.GetField(fields, 11, p.WireVarint).Bool() {
		t.Fatal("disable_parallel_tool_calls=false")
	}
	choice := requireField(t, fields, 12, p.WireLenDelim)
	choiceFields, err := p.ParseFields(choice.Bytes())
	if err != nil {
		t.Fatalf("parse choice: %v", err)
	}
	if got := p.GetField(choiceFields, 2, p.WireLenDelim).String(); got != "echo_text" {
		t.Fatalf("tool choice=%q", got)
	}
}

func TestBuildAPIGetChatMessageRequestWithNativePrompts(t *testing.T) {
	model := models.GetModelByID("claude-sonnet-4.6")
	if model == nil {
		t.Fatal("missing test model")
	}
	messages := []windsurf.ChatMessage{
		{Role: "system", Content: "be brief"},
		{Role: "user", Content: "call tool"},
		{Role: "assistant", ToolCalls: []windsurf.ToolCall{{ID: "toolu_1", Name: "echo_text", ArgumentsJSON: `{"text":"hi"}`}}},
		{Role: "tool", ToolCallID: "toolu_1", Content: "hi"},
	}
	req := buildAPIGetChatMessageRequestWithMessages("devin-token", model, "flattened fallback", 5, nil, nil, false, true, messages)
	fields, err := p.ParseFields(req)
	if err != nil {
		t.Fatalf("parse request: %v", err)
	}
	prompts := p.GetAllFields(fields, 3)
	if len(prompts) != 4 {
		t.Fatalf("prompts=%d fields=%+v", len(prompts), fields)
	}
	gotSources := make([]uint64, 0, len(prompts))
	gotTexts := make([]string, 0, len(prompts))
	var assistantFields []p.Field
	var toolFields []p.Field
	for _, prompt := range prompts {
		nested, err := p.ParseFields(prompt.Bytes())
		if err != nil {
			t.Fatalf("parse prompt: %v", err)
		}
		source := p.GetField(nested, 2, p.WireVarint).Uint()
		gotSources = append(gotSources, source)
		gotTexts = append(gotTexts, p.GetField(nested, 3, p.WireLenDelim).String())
		switch source {
		case 3:
			assistantFields = nested
		case 4:
			toolFields = nested
		}
	}
	wantSources := []uint64{2, 1, 3, 4}
	for i := range wantSources {
		if gotSources[i] != wantSources[i] {
			t.Fatalf("sources=%v want=%v texts=%q", gotSources, wantSources, gotTexts)
		}
	}
	for _, text := range gotTexts {
		if strings.Contains(text, "<tool_calls>") || strings.Contains(text, "<tool_result") {
			t.Fatalf("native prompt text leaked XML tool history: %q", gotTexts)
		}
	}
	toolCall := requireField(t, assistantFields, 6, p.WireLenDelim)
	toolCallFields, err := p.ParseFields(toolCall.Bytes())
	if err != nil {
		t.Fatalf("parse native tool call: %v", err)
	}
	if got := p.GetField(toolCallFields, 1, p.WireLenDelim).String(); got != "toolu_1" {
		t.Fatalf("tool_call id=%q", got)
	}
	if got := p.GetField(toolCallFields, 2, p.WireLenDelim).String(); got != "echo_text" {
		t.Fatalf("tool_call name=%q", got)
	}
	if got := p.GetField(toolCallFields, 3, p.WireLenDelim).String(); got != `{"text":"hi"}` {
		t.Fatalf("tool_call args=%q", got)
	}
	if got := p.GetField(toolFields, 7, p.WireLenDelim).String(); got != "toolu_1" {
		t.Fatalf("tool_call_id=%q", got)
	}
	if topPrompt := p.GetField(fields, 2, p.WireLenDelim).String(); topPrompt != "Continue as the assistant using the latest tool result. Do not repeat completed tool calls unless a new tool call is required." {
		t.Fatalf("top prompt=%q", topPrompt)
	}
}

func TestBuildAPIGetChatMessageRequestWithConversationFields(t *testing.T) {
	model := models.GetModelByID("claude-sonnet-4.6")
	if model == nil {
		t.Fatal("missing test model")
	}
	trajectoryRef := p.Concat(
		p.WriteStringField(1, "cascade-1"),
		p.WriteVarintField(2, 1),
		p.WriteVarintField(3, 2),
	)
	req := buildAPIGetChatMessageRequestWithOptions(apiGetChatMessageRequestOptions{
		APIKey:              "devin-token",
		Model:               model,
		Prompt:              "continue",
		RequestType:         5,
		SessionID:           "session-1",
		CascadeID:           "cascade-1",
		PromptID:            "prompt-1",
		TrajectoryReference: trajectoryRef,
	})
	fields, err := p.ParseFields(req)
	if err != nil {
		t.Fatalf("parse request: %v", err)
	}
	metaFields, err := p.ParseFields(requireField(t, fields, 1, p.WireLenDelim).Bytes())
	if err != nil {
		t.Fatalf("parse metadata: %v", err)
	}
	if got := p.GetField(metaFields, 10, p.WireLenDelim).String(); got != "session-1" {
		t.Fatalf("metadata session_id=%q", got)
	}
	if got := p.GetField(fields, 15, p.WireLenDelim).Bytes(); string(got) != string(trajectoryRef) {
		t.Fatalf("trajectory_reference=%x want=%x", got, trajectoryRef)
	}
	if got := p.GetField(fields, 16, p.WireLenDelim).String(); got != "cascade-1" {
		t.Fatalf("cascade_id=%q", got)
	}
	if got := p.GetField(fields, 17, p.WireLenDelim).String(); got != "prompt-1" {
		t.Fatalf("prompt_id=%q", got)
	}
}

func TestBuildAPIGetChatMessageRequestForcesNativePromptsWithToolHistory(t *testing.T) {
	model := models.GetModelByID("claude-opus-4-7-high")
	if model == nil {
		t.Fatal("missing test model")
	}
	messages := []windsurf.ChatMessage{
		{Role: "user", Content: "read file"},
		{Role: "assistant", ToolCalls: []windsurf.ToolCall{{ID: "call_1", Name: "read_file", ArgumentsJSON: `{"path":"README.md"}`}}},
		{Role: "tool", ToolCallID: "call_1", Content: "file body"},
		{Role: "user", Content: "continue"},
	}
	body := buildAPIGetChatMessageRequestWithOptions(apiGetChatMessageRequestOptions{
		APIKey:        "devin-token",
		Model:         model,
		Prompt:        "flattened prompt with xml should not be the only history",
		RequestType:   5,
		Tools:         []ToolDefinition{{Name: "read_file", SchemaJSON: `{"type":"object"}`}},
		NativePrompts: false,
		Messages:      messages,
		SessionID:     "session-1",
		CascadeID:     "cascade-1",
		PromptID:      "prompt-1",
	})
	fields, err := p.ParseFields(body)
	if err != nil {
		t.Fatal(err)
	}
	prompts := p.GetAllFields(fields, 3)
	if len(prompts) != 4 {
		t.Fatalf("prompts=%d", len(prompts))
	}
	if topPrompt := p.GetField(fields, 2, p.WireLenDelim).String(); strings.Contains(topPrompt, "<tool_calls>") || strings.Contains(topPrompt, "<tool_result") {
		t.Fatalf("top prompt leaked XML tool history: %q", topPrompt)
	}
	var sawToolCall, sawToolResultID bool
	for _, prompt := range prompts {
		nested, err := p.ParseFields(prompt.Bytes())
		if err != nil {
			t.Fatalf("parse prompt: %v", err)
		}
		if text := p.GetField(nested, 3, p.WireLenDelim).String(); strings.Contains(text, "<tool_calls>") || strings.Contains(text, "<tool_result") {
			t.Fatalf("forced native prompt leaked XML text: %q", text)
		}
		if len(p.GetAllFields(nested, 6)) > 0 {
			sawToolCall = true
		}
		if p.GetField(nested, 7, p.WireLenDelim).String() == "call_1" {
			sawToolResultID = true
		}
	}
	if !sawToolCall || !sawToolResultID {
		t.Fatalf("native tool history missing: sawToolCall=%v sawToolResultID=%v", sawToolCall, sawToolResultID)
	}
}

func TestToolCallAccumulatorMergesFragmentedDeltas(t *testing.T) {
	acc := newToolCallAccumulator()
	for _, delta := range []windsurf.ToolCall{
		{ID: "toolu_1", Name: "echo_text"},
		{ArgumentsJSON: `{"text":`},
		{ArgumentsJSON: ` "`},
		{ArgumentsJSON: `HELLO_`},
		{ArgumentsJSON: `FROM_`},
		{ArgumentsJSON: `DIRECT_TOOL"}`},
	} {
		acc.Add(delta)
	}
	calls := acc.All()
	if len(calls) != 1 {
		t.Fatalf("len(calls)=%d calls=%+v", len(calls), calls)
	}
	if calls[0].ID != "toolu_1" || calls[0].Name != "echo_text" || calls[0].ArgumentsJSON != `{"text": "HELLO_FROM_DIRECT_TOOL"}` {
		t.Fatalf("call=%+v", calls[0])
	}
}

func TestProbeAPIChatResultOKAllowsToolOnly(t *testing.T) {
	if !(ProbeAPIChatResult{ToolCalls: []windsurf.ToolCall{{ID: "toolu_1"}}}).OK() {
		t.Fatal("tool-only result should be OK")
	}
	if (ProbeAPIChatResult{}).OK() {
		t.Fatal("empty result should not be OK")
	}
}

func TestToolModeDefaultsToNativeForOpus47WithoutChangingModel(t *testing.T) {
	model := models.GetModelByID("claude-opus-4-7-xhigh")
	tools := []ToolDefinition{{Name: "echo_text", SchemaJSON: `{"type":"object"}`}}
	if got := ToolModeForRequest(ToolModeNative, model, tools, &ToolChoice{ToolName: "echo_text"}, []windsurf.ChatMessage{{Role: "user", Content: "hi"}}); got != ToolModeNative {
		t.Fatalf("mode=%s", got)
	}
	if model.ID != "claude-opus-4-7-xhigh" {
		t.Fatalf("tool emulation must not mutate or downgrade model: %+v", model)
	}
	if got := ToolModeForRequest(ToolModeAuto, model, tools, &ToolChoice{ToolName: "echo_text"}, nil); got != ToolModeEmulated {
		t.Fatalf("auto mode should emulate Opus 4.7 tools, got=%s", got)
	}
	if got := ToolModeForRequest(ToolModeEmulated, models.GetModelByID("claude-opus-4.6"), tools, nil, nil); got != ToolModeEmulated {
		t.Fatalf("explicit emulated mode=%s", got)
	}
	if got := ToolModeForRequest(ToolModeNative, models.GetModelByID("claude-opus-4.6"), tools, nil, nil); got != ToolModeNative {
		t.Fatalf("native mode=%s", got)
	}
	if got := ToolModeForRequest(ToolModeEmulated, model, tools, &ToolChoice{OptionName: "none"}, nil); got != ToolModeNone {
		t.Fatalf("tool_choice none mode=%s", got)
	}
	if got := ToolModeForRequest(ToolModeEmulated, model, nil, nil, nil); got != ToolModeNone {
		t.Fatalf("no tools mode=%s", got)
	}
}

func TestUpstreamToolPayloadDropsToolsWhenModeNone(t *testing.T) {
	req := ChatRequest{
		Messages: []windsurf.ChatMessage{{Role: "user", Content: "answer directly"}},
		Tools:    []ToolDefinition{{Name: "echo_text", SchemaJSON: `{"type":"object"}`}},
		ToolChoice: &ToolChoice{
			OptionName: "none",
		},
		DisableParallelToolCalls: true,
	}
	prompt, tools, choice, disableParallel := upstreamToolPayload(req, "answer directly", ToolModeNone)
	if prompt != "answer directly" || len(tools) != 0 || choice != nil || disableParallel {
		t.Fatalf("prompt=%q tools=%+v choice=%+v disable=%v", prompt, tools, choice, disableParallel)
	}
	model := models.GetModelByID("claude-opus-4.6")
	body := buildAPIGetChatMessageRequestWithMessages("devin-token", model, prompt, 5, tools, choice, disableParallel, false, req.Messages)
	fields, err := p.ParseFields(body)
	if err != nil {
		t.Fatalf("parse request: %v", err)
	}
	if got := p.GetAllFields(fields, 10); len(got) != 0 {
		t.Fatalf("tool fields should be absent: %d", len(got))
	}
	if got := p.GetAllFields(fields, 12); len(got) != 0 {
		t.Fatalf("tool choice fields should be absent: %d", len(got))
	}
}

func TestNativeOpus47AutoToolsFallbacksToPlainOnInternalError(t *testing.T) {
	req := ChatRequest{
		APIKey: "devin-token",
		Model:  models.GetModelByID("claude-opus-4-7-medium"),
		Messages: []windsurf.ChatMessage{{
			Role:    "user",
			Content: "answer directly",
		}},
		Tools: []ToolDefinition{{
			Name:       "echo_text",
			SchemaJSON: `{"type":"object"}`,
		}},
	}
	if !shouldFallbackNativeToolsToPlain(req, ToolModeNative, errInternal()) {
		t.Fatal("expected plain fallback for Opus 4.7 native auto tools")
	}
	prompt := flattenMessages(req.Messages)
	nativePrompt, nativeTools, nativeChoice, nativeDisable := upstreamToolPayload(req, prompt, ToolModeNative)
	nativeBody := buildAPIGetChatMessageRequestWithMessages(req.APIKey, req.Model, nativePrompt, 5, nativeTools, nativeChoice, nativeDisable, false, req.Messages)
	nativeFields, err := p.ParseFields(nativeBody)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(p.GetAllFields(nativeFields, 10)); got != 1 {
		t.Fatalf("native tools=%d", got)
	}
	plainBody := buildAPIGetChatMessageRequestWithMessages(req.APIKey, req.Model, prompt, 5, nil, nil, false, false, req.Messages)
	plainFields, err := p.ParseFields(plainBody)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(p.GetAllFields(plainFields, 10)); got != 0 {
		t.Fatalf("plain tools=%d", got)
	}
}

func TestNativeOpus47ForcedToolDoesNotFallbackToPlain(t *testing.T) {
	req := ChatRequest{
		APIKey: "devin-token",
		Model:  models.GetModelByID("claude-opus-4-7-medium"),
		Messages: []windsurf.ChatMessage{{
			Role:    "user",
			Content: "call tool",
		}},
		Tools: []ToolDefinition{{
			Name:       "echo_text",
			SchemaJSON: `{"type":"object"}`,
		}},
		ToolChoice: &ToolChoice{ToolName: "echo_text"},
	}
	if shouldFallbackNativeToolsToPlain(req, ToolModeNative, errInternal()) {
		t.Fatal("forced tools must not fall back to plain text")
	}
}

func TestNativeOpus47ToolHistoryDoesNotFallbackToEmulated(t *testing.T) {
	req := ChatRequest{
		APIKey: "devin-token",
		Model:  models.GetModelByID("claude-opus-4-7-high"),
		Messages: []windsurf.ChatMessage{
			{Role: "user", Content: "read file"},
			{Role: "assistant", ToolCalls: []windsurf.ToolCall{{ID: "call_1", Name: "read_file", ArgumentsJSON: `{"path":"README.md"}`}}},
			{Role: "tool", ToolCallID: "call_1", Content: "file body"},
			{Role: "user", Content: "continue"},
		},
		Tools: []ToolDefinition{{
			Name:       "read_file",
			SchemaJSON: `{"type":"object"}`,
		}},
	}
	prompt := flattenMessages(req.Messages)
	nativePrompt, tools, choice, disableParallel := upstreamToolPayload(req, prompt, ToolModeNative)
	if nativePrompt != prompt || len(tools) != 1 || choice != nil || disableParallel {
		t.Fatalf("native upstream prompt=%q tools=%+v choice=%+v disable=%v", nativePrompt, tools, choice, disableParallel)
	}
	body := buildAPIGetChatMessageRequestWithOptions(apiGetChatMessageRequestOptions{
		APIKey:      req.APIKey,
		Model:       req.Model,
		Prompt:      nativePrompt,
		RequestType: 5,
		Tools:       tools,
		Messages:    req.Messages,
		SessionID:   "session-1",
		CascadeID:   "cascade-1",
		PromptID:    "prompt-1",
	})
	fields, err := p.ParseFields(body)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(p.GetAllFields(fields, 10)); got != 1 {
		t.Fatalf("native tools=%d", got)
	}
	if got := p.GetField(fields, 16, p.WireLenDelim).String(); got != "cascade-1" {
		t.Fatalf("cascade_id=%q", got)
	}
	if topPrompt := p.GetField(fields, 2, p.WireLenDelim).String(); strings.Contains(topPrompt, "<tool_calls>") || strings.Contains(topPrompt, "<tool_result") {
		t.Fatalf("top prompt leaked XML tool history: %q", topPrompt)
	}
	prompts := p.GetAllFields(fields, 3)
	if len(prompts) != 4 {
		t.Fatalf("prompts=%d", len(prompts))
	}
	var sawToolCall, sawToolResultID bool
	for _, prompt := range prompts {
		nested, err := p.ParseFields(prompt.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		if len(p.GetAllFields(nested, 6)) > 0 {
			sawToolCall = true
		}
		if p.GetField(nested, 7, p.WireLenDelim).String() == "call_1" {
			sawToolResultID = true
		}
	}
	if !sawToolCall || !sawToolResultID {
		t.Fatalf("native tool history missing sawToolCall=%v sawToolResultID=%v", sawToolCall, sawToolResultID)
	}
}

func TestBuildEmulatedToolPromptAndParser(t *testing.T) {
	prompt := BuildEmulatedToolPrompt(
		[]windsurf.ChatMessage{{Role: "user", Content: "Use echo_text."}},
		[]ToolDefinition{{Name: "echo_text", Description: "Echo input", SchemaJSON: `{"type":"object","properties":{"text":{"type":"string"}}}`}},
		&ToolChoice{ToolName: "echo_text"},
	)
	for _, want := range []string{
		"Output exactly one tool call block",
		"<tool_call>",
		"echo_text",
		`"text"`,
		"Use echo_text.",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q in %q", want, prompt)
		}
	}
	text, calls := parseEmulatedToolCalls(` <tool_call>{"name":"echo_text","arguments":{"text":"HELLO"}}</tool_call> `)
	if strings.TrimSpace(text) != "" {
		t.Fatalf("text=%q", text)
	}
	if len(calls) != 1 || calls[0].Name != "echo_text" || calls[0].ArgumentsJSON != `{"text":"HELLO"}` || calls[0].ID == "" {
		t.Fatalf("calls=%+v", calls)
	}
}

func TestParseEmulatedToolCallsSupportsOpenAIShape(t *testing.T) {
	_, calls := parseEmulatedToolCalls(`{"function_call":{"name":"bash","arguments":"{\"command\":\"pwd\"}"}}`)
	if len(calls) != 1 || calls[0].Name != "bash" || calls[0].ArgumentsJSON != `{"command":"pwd"}` {
		t.Fatalf("calls=%+v", calls)
	}
}

func TestHTTPClientUsesPerProxyCacheAndMasksCredentials(t *testing.T) {
	c := NewClient(WithTimeout(3), WithAllowPrivateProxy(true))
	first, err := c.httpClient("http://user:secret@127.0.0.1:18080")
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.httpClient("http://user:secret@127.0.0.1:18080")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("proxy client was not cached")
	}
	if first == c.http {
		t.Fatal("proxy client should not be the default client")
	}
	if first.Timeout != 3 {
		t.Fatalf("timeout=%s", first.Timeout)
	}
	if _, ok := first.Transport.(*http.Transport); !ok {
		t.Fatalf("transport=%T want *http.Transport", first.Transport)
	}
	if got := maskProxyURL("http://user:secret@127.0.0.1:18080"); got != "http://user:%2A%2A%2A@127.0.0.1:18080" {
		t.Fatalf("masked proxy=%q", got)
	}
	snap := c.Snapshot()
	if snap.ProxyClients != 1 {
		t.Fatalf("ProxyClients=%d", snap.ProxyClients)
	}
}

func TestRecordStatsRedactsErrors(t *testing.T) {
	c := NewClient()
	c.recordStats("server.codeium.com", "http://user:secret@proxy.example.com:8080", "claude-sonnet-4.6", "Authorization: Bearer sk-test_abcdefghijklmnopqrstuvwxyz user@example.com", 0, 0)
	stats := c.Snapshot()
	if strings.Contains(stats.LastError, "sk-test_abcdefghijklmnopqrstuvwxyz") || strings.Contains(stats.LastError, "user@example.com") {
		t.Fatalf("direct stats leaked secret: %+v", stats)
	}
	if stats.LastProxy != "http://user:%2A%2A%2A@proxy.example.com:8080" {
		t.Fatalf("proxy was not masked: %+v", stats)
	}
}

func TestHTTPClientRejectsInvalidProxyURL(t *testing.T) {
	c := NewClient()
	if _, err := c.httpClient("://bad"); err == nil {
		t.Fatal("expected invalid proxy error")
	}
}

func TestHTTPClientRejectsPrivateProxyByDefault(t *testing.T) {
	c := NewClient()
	if _, err := c.httpClient("http://127.0.0.1:18080"); err == nil {
		t.Fatal("expected private proxy rejection")
	}
}

func TestHTTPClientUsesDefaultProxyWhenRequestProxyEmpty(t *testing.T) {
	c := NewClient(WithDefaultProxyURL("http://default.proxy:8080"))
	client, err := c.httpClient("")
	if err != nil {
		t.Fatal(err)
	}
	if client == c.http {
		t.Fatal("default proxy should create a proxy client")
	}
	if got := c.effectiveProxyURL(""); got != "http://default.proxy:8080" {
		t.Fatalf("effective proxy=%q", got)
	}
}

func TestBuildChatMessagePrompt(t *testing.T) {
	msg := buildChatMessagePrompt("hello prompt")
	fields, err := p.ParseFields(msg)
	if err != nil {
		t.Fatalf("parse prompt: %v", err)
	}
	if got := p.GetField(fields, 2, p.WireVarint).Uint(); got != 1 {
		t.Fatalf("source=%d", got)
	}
	if got := p.GetField(fields, 3, p.WireLenDelim).String(); got != "hello prompt" {
		t.Fatalf("prompt=%q", got)
	}
	if !p.GetField(fields, 5, p.WireVarint).Bool() {
		t.Fatalf("safe_for_code_telemetry=false")
	}
}

func TestParseAPIGetChatMessageResponse(t *testing.T) {
	usage := p.Concat(
		p.WriteVarintField(2, 10),
		p.WriteVarintField(3, 4),
		p.WriteVarintField(5, 7),
	)
	resp := p.Concat(
		p.WriteStringField(3, "he"),
		p.WriteMessageField(6, p.Concat(
			p.WriteStringField(1, "call_1"),
			p.WriteStringField(2, "echo_text"),
			p.WriteStringField(3, `{"text":"hi"}`),
		)),
		p.WriteMessageField(7, p.Concat(
			usage,
			p.WriteStringField(9, "claude-sonnet-4-6"),
		)),
		p.WriteStringField(9, "thinking"),
	)
	parsed, err := parseAPIGetChatMessageResponse(resp)
	if err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if parsed.Assistant != "he" {
		t.Fatalf("Assistant=%q", parsed.Assistant)
	}
	if parsed.Thinking != "thinking" {
		t.Fatalf("Thinking=%q", parsed.Thinking)
	}
	if parsed.ActualModel != "claude-sonnet-4-6" {
		t.Fatalf("ActualModel=%q", parsed.ActualModel)
	}
	if parsed.Usage == nil || parsed.Usage.InputTokens != 10 || parsed.Usage.OutputTokens != 4 || parsed.Usage.CacheReadTokens != 7 {
		t.Fatalf("Usage=%+v", parsed.Usage)
	}
	if len(parsed.ToolCalls) != 1 || parsed.ToolCalls[0].Name != "echo_text" || parsed.ToolCalls[0].ArgumentsJSON != `{"text":"hi"}` {
		t.Fatalf("ToolCalls=%+v", parsed.ToolCalls)
	}

	_, err = parseAPIGetChatMessageResponse([]byte{0xff})
	if err == nil || !strings.Contains(err.Error(), "varint") {
		t.Fatalf("expected parse error, got %v", err)
	}
}

func TestFlattenMessages(t *testing.T) {
	if got := flattenMessages([]windsurf.ChatMessage{{Role: "user", Content: "hi"}}); got != "hi" {
		t.Fatalf("single user flatten=%q", got)
	}

	got := flattenMessages([]windsurf.ChatMessage{
		{Role: "system", Content: "be brief"},
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello", ToolCalls: []windsurf.ToolCall{{
			ID:            "call_1",
			Name:          "echo_text",
			ArgumentsJSON: `{"text":"hi"}`,
		}}},
		{Role: "tool", ToolCallID: "call_1", Content: "{\"ok\":true}"},
		{Role: "user", Content: "what next?"},
	})
	for _, want := range []string{
		"be brief",
		"<user>\nhi\n</user>",
		"<assistant>\nhello",
		"<tool_calls>",
		"\"name\":\"echo_text\"",
		"<tool_result id=\"call_1\">\n{\"ok\":true}\n</tool_result>",
		"Latest user message:\nwhat next?",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("flattenMessages missing %q in %q", want, got)
		}
	}
}

func TestFlattenMessagesContinuesAfterToolResult(t *testing.T) {
	got := flattenMessages([]windsurf.ChatMessage{
		{Role: "user", Content: "call tool then answer"},
		{Role: "assistant", ToolCalls: []windsurf.ToolCall{{
			ID:            "toolu_local_1",
			Name:          "echo_text",
			ArgumentsJSON: `{"text":"SECOND_LEG_OK"}`,
		}}},
		{Role: "tool", ToolCallID: "toolu_local_1", Content: "SECOND_LEG_OK"},
	})
	for _, want := range []string{
		"Conversation transcript:",
		"<user>\ncall tool then answer\n</user>",
		"<tool_calls>",
		"<tool_result id=\"toolu_local_1\">\nSECOND_LEG_OK\n</tool_result>",
		"use that result to answer",
		"do not repeat any tool_call id",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("flattenMessages missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "Latest user message:") {
		t.Fatalf("continuation should not use latest-user flattening: %q", got)
	}
}

func TestFlattenMessagesThreeToolRounds(t *testing.T) {
	got := flattenMessages([]windsurf.ChatMessage{
		{Role: "user", Content: "use tools three times then answer"},
		{Role: "assistant", ToolCalls: []windsurf.ToolCall{{ID: "toolu_1", Name: "step_one", ArgumentsJSON: `{"n":1}`}}},
		{Role: "tool", ToolCallID: "toolu_1", Content: "one"},
		{Role: "assistant", ToolCalls: []windsurf.ToolCall{{ID: "toolu_2", Name: "step_two", ArgumentsJSON: `{"n":2}`}}},
		{Role: "tool", ToolCallID: "toolu_2", Content: "two"},
		{Role: "assistant", ToolCalls: []windsurf.ToolCall{{ID: "toolu_3", Name: "step_three", ArgumentsJSON: `{"n":3}`}}},
		{Role: "tool", ToolCallID: "toolu_3", Content: "three"},
	})
	for _, want := range []string{
		"<tool_result id=\"toolu_1\">\none\n</tool_result>",
		"<tool_result id=\"toolu_2\">\ntwo\n</tool_result>",
		"<tool_result id=\"toolu_3\">\nthree\n</tool_result>",
		"\"id\":\"toolu_1\"",
		"\"id\":\"toolu_2\"",
		"\"id\":\"toolu_3\"",
		"call only a new tool with a new id",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("flattenMessages missing %q in %q", want, got)
		}
	}
}

func requireField(t *testing.T, fields []p.Field, fieldNum int, wireType int) *p.Field {
	t.Helper()
	field := p.GetField(fields, fieldNum, wireType)
	if field == nil {
		t.Fatalf("missing field %d wire %d", fieldNum, wireType)
	}
	return field
}
