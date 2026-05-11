package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/zhangyu/windsurfapi-go/internal/account"
	"github.com/zhangyu/windsurfapi-go/internal/config"
	"github.com/zhangyu/windsurfapi-go/internal/modelaccess"
	"github.com/zhangyu/windsurfapi-go/internal/models"
	proxypool "github.com/zhangyu/windsurfapi-go/internal/proxy"
	"github.com/zhangyu/windsurfapi-go/internal/redact"
	reusepool "github.com/zhangyu/windsurfapi-go/internal/reuse"
	"github.com/zhangyu/windsurfapi-go/internal/sanitize"
	"github.com/zhangyu/windsurfapi-go/internal/sse"
	usagepkg "github.com/zhangyu/windsurfapi-go/internal/usage"
	"github.com/zhangyu/windsurfapi-go/internal/windsurf"
	"github.com/zhangyu/windsurfapi-go/internal/windsurf/direct"
)

const (
	fastSwitchMaxAttempts = 9
	fastSwitchBudget      = 8 * time.Second
)

// ChatCompletionRequest mirrors the OpenAI chat completion request.
type ChatCompletionRequest struct {
	Model             string        `json:"model"`
	Messages          []ChatMessage `json:"messages"`
	Stream            bool          `json:"stream,omitempty"`
	Temperature       float64       `json:"temperature,omitempty"`
	MaxTokens         int           `json:"max_tokens,omitempty"`
	ResponseFormat    any           `json:"response_format,omitempty"`
	Reasoning         any           `json:"reasoning,omitempty"`
	ReasoningEffort   string        `json:"reasoning_effort,omitempty"`
	Tools             []Tool        `json:"tools,omitempty"`
	ToolChoice        any           `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool         `json:"parallel_tool_calls,omitempty"`
	StrictReuse       *bool         `json:"strict_reuse,omitempty"`
	User              string        `json:"user,omitempty"`
	Metadata          any           `json:"metadata,omitempty"`
	Conversation      string        `json:"conversation,omitempty"`
	PreviousResponse  string        `json:"previous_response_id,omitempty"`
}

type directChatClient interface {
	Chat(context.Context, direct.ChatRequest) (*windsurf.ChatResult, error)
}

type directChatParams struct {
	Model              *models.Model
	DisplayModelID     string
	Messages           []windsurf.ChatMessage
	CallerKey          string
	Route              string
	Stream             bool
	HTTPWriter         http.ResponseWriter
	ProxyPool          *proxypool.Manager
	ReuseTTL           time.Duration
	Tools              []direct.ToolDefinition
	ToolChoice         *direct.ToolChoice
	Thinking           string
	ParallelToolCalls  *bool
	StrictReuse        bool
	TestAccountIDs     []int
	InputTokenEstimate uint64
	VirtualUsage       *usagepkg.Manager
	UsageOut           *usagepkg.Usage
	OnStreamStart      func() error
	OnTextDelta        func(string) error
	OnThinkingDelta    func(string) error
	OnToolCallDelta    func(int, windsurf.ToolCall) error
	OnStreamError      func(account.ErrorClass, error) error
	OnStreamFinish     func(*windsurf.ChatResult) error
}

type ChatMessage struct {
	Role       string     `json:"role"`
	Content    any        `json:"content"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
}

type Tool struct {
	Type     string       `json:"type,omitempty"`
	Function ToolFunction `json:"function,omitempty"`
}

type ToolFunction struct {
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"`
	Strict      *bool  `json:"strict,omitempty"`
}

type ToolCall struct {
	ID       string           `json:"id,omitempty"`
	Type     string           `json:"type,omitempty"`
	Function ToolCallFunction `json:"function,omitempty"`
}

type ToolCallFunction struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

func (m ChatMessage) text() string {
	switch v := m.Content.(type) {
	case string:
		return v
	case nil:
		return ""
	case []any:
		var parts []string
		for _, it := range v {
			if mm, ok := it.(map[string]any); ok {
				if t, _ := mm["type"].(string); t == "text" {
					if s, _ := mm["text"].(string); s != "" {
						parts = append(parts, s)
					}
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

// ChatCompletionsHandler serves OpenAI-compatible chat through direct Windsurf
// cloud RPCs. The LS-backed client remains legacy-only.
func ChatCompletionsHandler(_ *config.Config, am *account.Manager, dc directChatClient, rp *reusepool.Pool, access *modelaccess.Manager, pp ...any) http.HandlerFunc {
	var proxyPool *proxypool.Manager
	var usageMgr *usagepkg.Manager
	if len(pp) > 0 {
		for _, item := range pp {
			switch v := item.(type) {
			case *proxypool.Manager:
				proxyPool = v
			case *usagepkg.Manager:
				usageMgr = v
			}
		}
	}
	return func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		setNoStore(w)
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		var req ChatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid request: "+err.Error())
			return
		}
		if len(req.Messages) == 0 {
			writeJSONError(w, http.StatusBadRequest, "messages required")
			return
		}

		model := models.ResolveModelForRequest(req.Model, openAIReasoningEffort(req))
		if model == nil {
			writeJSONError(w, http.StatusBadRequest, "unknown model: "+req.Model)
			return
		}
		if !model.DirectSupported {
			writeJSONError(w, http.StatusBadRequest, "model unsupported on direct backend: "+modelUnsupportedReason(model))
			return
		}
		if ok, reason := modelAllowed(access, model.ID); !ok {
			writeOpenAIError(w, http.StatusForbidden, "model blocked: "+reason, "model_blocked")
			return
		}
		displayModelID := responseModelID(req.Model, model)
		setOpenAIHeaders(w, displayModelID, 0)

		wMsgs := make([]windsurf.ChatMessage, 0, len(req.Messages))
		for _, m := range req.Messages {
			wMsgs = append(wMsgs, windsurf.ChatMessage{
				Role:       m.Role,
				Content:    m.text(),
				ToolCallID: m.ToolCallID,
				ToolCalls:  toWindsurfToolCalls(m.ToolCalls),
			})
		}
		if hint := responseFormatHint(req.ResponseFormat); hint != "" {
			wMsgs = prependSystemHint(wMsgs, hint)
		}
		tools, err := toDirectTools(req.Tools)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		choice, err := toDirectToolChoice(req.ToolChoice)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		choice = pruneDirectToolChoice(choice, tools)
		if shouldSuppressToolsForContinuation(req.Messages, choice) {
			tools = nil
			choice = nil
		}
		callerKey := callerKeyForBody(r, req)

		strictReuse := strictReuseRequested(r)
		if req.StrictReuse != nil {
			strictReuse = *req.StrictReuse
		}
		inputTokenEstimate := estimateInputTokens(wMsgs, tools)
		result, status, err := executeOpenAIChat(r, dc, am, rp, model, displayModelID, wMsgs, callerKey, req.Stream, w, tools, choice, req.ParallelToolCalls, strictReuse, proxyPool, usageMgr, openAIReasoningPrompt(req), inputTokenEstimate)
		if err != nil {
			writeJSONError(w, status, err.Error())
			return
		}
		if req.Stream {
			return
		}
		setOpenAIHeaders(w, displayModelID, time.Since(started))
		writeUnaryResponse(w, displayModelID, result, inputTokenEstimate)
	}
}

func testAccountIDsFromRequest(r *http.Request) []int {
	if r == nil || !isLocalRequest(r) {
		return nil
	}
	raw := strings.TrimSpace(r.Header.Get("X-Windsurf-Test-Account-IDs"))
	if raw == "" {
		return nil
	}
	seen := map[int]bool{}
	var out []int
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.Atoi(part)
		if err != nil || id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func setNoStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
}

func setOpenAIHeaders(w http.ResponseWriter, modelID string, processing time.Duration) {
	if modelID != "" {
		w.Header().Set("OpenAI-Model", modelID)
	}
	w.Header().Set("OpenAI-Version", "2020-10-01")
	w.Header().Set("OpenAI-Organization", "org-windsurf-proxy")
	if processing > 0 {
		w.Header().Set("OpenAI-Processing-Ms", fmt.Sprintf("%d", processing.Milliseconds()))
	}
}

func setAnthropicHeaders(w http.ResponseWriter, modelID string) {
	reqID := w.Header().Get("X-Request-ID")
	if reqID == "" {
		reqID = w.Header().Get("Request-Id")
	}
	if reqID != "" {
		w.Header().Set("Request-Id", reqID)
		w.Header().Set("X-Request-ID", reqID)
	}
	if modelID != "" {
		w.Header().Set("Anthropic-Model", modelID)
	}
}

func toDirectTools(tools []Tool) ([]direct.ToolDefinition, error) {
	out := make([]direct.ToolDefinition, 0, len(tools))
	for i, tool := range tools {
		if tool.Type != "" && tool.Type != "function" {
			return nil, fmt.Errorf("unsupported tool type at index %d: %s", i, tool.Type)
		}
		if strings.TrimSpace(tool.Function.Name) == "" {
			return nil, fmt.Errorf("tool function name required at index %d", i)
		}
		schema := "{}"
		if tool.Function.Parameters != nil {
			raw, err := json.Marshal(tool.Function.Parameters)
			if err != nil {
				return nil, fmt.Errorf("tool parameters at index %d are not JSON-serializable: %w", i, err)
			}
			schema = string(raw)
		}
		strict := false
		if tool.Function.Strict != nil {
			strict = *tool.Function.Strict
		}
		out = append(out, direct.ToolDefinition{
			Name:        tool.Function.Name,
			Description: tool.Function.Description,
			SchemaJSON:  schema,
			Strict:      strict,
		})
	}
	return out, nil
}

func toDirectToolChoice(choice any) (*direct.ToolChoice, error) {
	return toDirectToolChoiceWithKind(choice, "tool_choice")
}

func toDirectToolChoiceWithKind(choice any, fieldName string) (*direct.ToolChoice, error) {
	if choice == nil {
		return nil, nil
	}
	switch v := choice.(type) {
	case string:
		v = strings.TrimSpace(v)
		if v == "" || v == "auto" {
			return nil, nil
		}
		if v == "any" || v == "required" {
			return &direct.ToolChoice{OptionName: "required"}, nil
		}
		return &direct.ToolChoice{OptionName: v}, nil
	case map[string]any:
		t, _ := v["type"].(string)
		if t == "auto" {
			return nil, nil
		}
		if t == "none" || t == "any" || t == "required" {
			if t == "any" {
				t = "required"
			}
			return &direct.ToolChoice{OptionName: t}, nil
		}
		if t != "" && t != "function" && t != "tool" {
			return nil, fmt.Errorf("unsupported tool_choice type: %s", t)
		}
		fn, _ := v["function"].(map[string]any)
		name, _ := fn["name"].(string)
		if name == "" {
			name, _ = v["name"].(string)
		}
		if name == "" {
			if tool, _ := v["tool"].(map[string]any); tool != nil {
				name, _ = tool["name"].(string)
			}
		}
		if strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("%s function/name required", fieldName)
		}
		return &direct.ToolChoice{ToolName: name}, nil
	default:
		return nil, fmt.Errorf("unsupported %s shape", fieldName)
	}
}

func pruneDirectToolChoice(choice *direct.ToolChoice, tools []direct.ToolDefinition) *direct.ToolChoice {
	if choice == nil {
		return nil
	}
	if len(tools) == 0 {
		return nil
	}
	if strings.TrimSpace(choice.ToolName) == "" {
		return choice
	}
	for _, tool := range tools {
		if tool.Name == choice.ToolName {
			return choice
		}
	}
	return nil
}

func responseFormatHint(format any) string {
	m, ok := format.(map[string]any)
	if !ok || m == nil {
		return ""
	}
	typ := strings.TrimSpace(fmt.Sprint(m["type"]))
	if typ != "json_object" && typ != "json_schema" {
		return ""
	}
	hint := "Respond with valid JSON only. No markdown, no code fences, no explanation. Output must be parseable by JSON.parse(). Preserve the exact JSON field names requested by the user, and do not add extra fields when an exact key set is requested. If tool results contain the requested values, put only those values into JSON fields rather than describing them in prose or copying the full tool result."
	if typ == "json_schema" {
		if schema := responseFormatSchema(m); schema != nil {
			raw, err := json.Marshal(schema)
			if err == nil && len(raw) > 0 {
				hint += " Conform to this JSON Schema:\n" + string(raw)
			}
		}
	}
	return hint
}

func responseFormatSchema(format map[string]any) any {
	if schema := format["schema"]; schema != nil {
		return schema
	}
	if nested, ok := format["json_schema"].(map[string]any); ok {
		return nested["schema"]
	}
	return nil
}

func openAIReasoningPrompt(req ChatCompletionRequest) string {
	effort := openAIReasoningEffort(req)
	if effort == "" {
		return ""
	}
	switch effort {
	case "none", "off", "disabled", "false":
		return ""
	case "minimal", "low":
		return "OpenAI-compatible reasoning is requested with low effort. Use private reasoning as needed and return any upstream thinking in reasoning_content when available."
	case "medium":
		return "OpenAI-compatible reasoning is requested with medium effort. Use private reasoning as needed and return any upstream thinking in reasoning_content when available."
	case "high", "xhigh", "max":
		return "OpenAI-compatible reasoning is requested with high effort. Use private reasoning as needed and return any upstream thinking in reasoning_content when available."
	default:
		return "OpenAI-compatible reasoning is requested. Use private reasoning as needed and return any upstream thinking in reasoning_content when available."
	}
}

func openAIReasoningEffort(req ChatCompletionRequest) string {
	effort := strings.ToLower(strings.TrimSpace(req.ReasoningEffort))
	if effort == "" {
		if m, ok := req.Reasoning.(map[string]any); ok && m != nil {
			effort = strings.ToLower(strings.TrimSpace(stringValue(m["effort"])))
		}
	}
	return effort
}

func prependSystemHint(messages []windsurf.ChatMessage, hint string) []windsurf.ChatMessage {
	hint = strings.TrimSpace(hint)
	if hint == "" {
		return messages
	}
	out := make([]windsurf.ChatMessage, 0, len(messages)+1)
	out = append(out, windsurf.ChatMessage{Role: "system", Content: hint})
	out = append(out, messages...)
	return out
}

func toWindsurfToolCalls(calls []ToolCall) []windsurf.ToolCall {
	out := make([]windsurf.ToolCall, 0, len(calls))
	for _, call := range calls {
		out = append(out, windsurf.ToolCall{
			ID:            call.ID,
			Name:          call.Function.Name,
			ArgumentsJSON: call.Function.Arguments,
		})
	}
	return out
}

func shouldSuppressToolsForContinuation(messages []ChatMessage, choice *direct.ToolChoice) bool {
	if choice != nil && strings.TrimSpace(choice.ToolName) != "" {
		return false
	}
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if strings.TrimSpace(msg.text()) == "" && msg.ToolCallID == "" && len(msg.ToolCalls) == 0 {
			continue
		}
		return msg.Role == "tool"
	}
	return false
}

func executeOpenAIChat(r *http.Request, dc directChatClient, am *account.Manager, rp *reusepool.Pool, model *models.Model, displayModelID string, msgs []windsurf.ChatMessage, callerKey string, stream bool, w http.ResponseWriter, tools []direct.ToolDefinition, choice *direct.ToolChoice, parallelToolCalls *bool, strictReuse bool, proxyPool *proxypool.Manager, usageMgr *usagepkg.Manager, thinking string, inputTokenEstimate uint64) (*windsurf.ChatResult, int, error) {
	var sw *sse.Writer
	var responseUsage usagepkg.Usage
	if strings.TrimSpace(displayModelID) == "" && model != nil {
		displayModelID = model.ID
	}
	params := directChatParams{
		Model:              model,
		DisplayModelID:     displayModelID,
		Messages:           msgs,
		CallerKey:          callerKey,
		Route:              "chat_completions",
		Stream:             stream,
		HTTPWriter:         w,
		ProxyPool:          proxyPool,
		Thinking:           thinking,
		Tools:              tools,
		ToolChoice:         choice,
		ParallelToolCalls:  parallelToolCalls,
		StrictReuse:        strictReuse,
		TestAccountIDs:     testAccountIDsFromRequest(r),
		InputTokenEstimate: inputTokenEstimate,
		VirtualUsage:       usageMgr,
		UsageOut:           &responseUsage,
	}
	if stream {
		params.OnStreamStart = func() error {
			var err error
			sw, err = sse.NewWriter(w, displayModelID)
			if err == nil {
				_ = sw.Role()
			}
			return err
		}
		params.OnTextDelta = func(delta string) error {
			if sw == nil {
				return fmt.Errorf("stream writer unavailable")
			}
			return sw.Delta(delta)
		}
		params.OnThinkingDelta = func(delta string) error {
			if sw == nil {
				return fmt.Errorf("stream writer unavailable")
			}
			return sw.ReasoningDelta(delta)
		}
		params.OnToolCallDelta = func(index int, delta windsurf.ToolCall) error {
			if sw == nil {
				return fmt.Errorf("stream writer unavailable")
			}
			return sw.ToolCallDelta(index, delta.ID, delta.Name, delta.ArgumentsJSON)
		}
		params.OnStreamError = func(class account.ErrorClass, err error) error {
			if sw == nil {
				return fmt.Errorf("stream writer unavailable")
			}
			return sw.Error(class, err)
		}
		params.OnStreamFinish = func(result *windsurf.ChatResult) error {
			if sw == nil {
				var err error
				sw, err = sse.NewWriter(w, displayModelID)
				if err != nil {
					return err
				}
				_ = sw.Role()
			}
			return sw.Finish(result.FinishReason, usageToSSE(responseUsage))
		}
	}
	return executeDirectChat(r, dc, am, rp, params)
}

func executeDirectChat(r *http.Request, dc directChatClient, am *account.Manager, rp *reusepool.Pool, params directChatParams) (*windsurf.ChatResult, int, error) {
	start := time.Now()
	var tried []int
	var lastErr error
	var lastClass account.ErrorClass
	maxAttempts := fastSwitchMaxAttempts + 1
	var emitted bool
	reuseHit := false
	reqID := requestID(r)
	callerHash := shortHash(params.CallerKey)
	finalAttempts := 0
	reuseTTL := params.ReuseTTL
	if reuseTTL <= 0 {
		reuseTTL = reusepool.DefaultTTL
	}
	route := params.Route
	if route == "" {
		route = "chat"
	}
	if params.Model != nil {
		log.Printf("req_id=%s route=%s event=direct_chat_shape model=%s tool_mode=%s %s", reqID, route, params.Model.ID, direct.ToolModeForRequest(params.Model, params.Tools, params.ToolChoice, params.Messages), directRequestShape(params))
	}
	shape := directRequestShape(params)
	toolsDigest := directToolsDigest(params.Tools)
	toolChoiceDigest := directToolChoiceDigest(params.ToolChoice)
	exactFingerprint := ""
	checkoutFingerprint := ""
	reuseMissReason := "disabled"
	var reuseEntry *reusepool.Entry
	if rp != nil {
		exactFingerprint = reusepool.FingerprintWithOptions(params.Model.ID, params.CallerKey, route, toolsDigest, toolChoiceDigest, params.Messages)
		checkoutFingerprint = reusepool.FingerprintBeforeWithOptions(params.Model.ID, params.CallerKey, route, toolsDigest, toolChoiceDigest, params.Messages)
		if checkoutFingerprint == "" {
			checkoutFingerprint = exactFingerprint
		}
		if entry, ok := rp.Checkout(checkoutFingerprint, params.CallerKey, params.Model.ID); ok {
			reuseHit = true
			reuseEntry = entry
			reuseMissReason = ""
		} else if exactFingerprint != "" && exactFingerprint != checkoutFingerprint {
			if entry, ok := rp.Checkout(exactFingerprint, params.CallerKey, params.Model.ID); ok {
				reuseHit = true
				reuseEntry = entry
				checkoutFingerprint = exactFingerprint
				reuseMissReason = ""
			} else {
				reuseMissReason = "miss"
			}
		} else {
			reuseMissReason = "miss"
		}
	}

	if len(params.TestAccountIDs) > 0 && len(params.TestAccountIDs) < maxAttempts {
		maxAttempts = len(params.TestAccountIDs)
	} else if accounts, err := am.GetEnabledAccounts(); err == nil && len(accounts) > 0 && len(accounts) < maxAttempts {
		maxAttempts = len(accounts)
	}

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		finalAttempts = attempt
		var res *account.Reservation
		var err error
		if reuseEntry != nil && attempt == 1 {
			res, err = am.ReserveAccount(r.Context(), params.Model.ID, reuseEntry.AccountID)
			if err != nil {
				rp.Checkin(checkoutFingerprint, reuseEntry, reuseTTL)
				reuseHit = false
				reuseMissReason = "sticky_account_unavailable"
				if params.StrictReuse {
					wrapped := fmt.Errorf("strict reuse account unavailable: %w", err)
					recordRequestEvent(RequestEvent{RequestID: reqID, Route: route, Model: params.Model.ID, CallerKeyHash: callerHash, AccountID: reuseEntry.AccountID, Attempt: finalAttempts, Status: "error", HTTPStatus: http.StatusTooManyRequests, ErrorClass: account.ErrorRateLimit, Error: wrapped.Error(), Retry: false, Stream: params.Stream, LatencyMS: time.Since(start).Milliseconds(), ToolCallCount: len(params.Tools), ReuseHit: false, ReuseMissReason: reuseMissReason})
					return nil, http.StatusTooManyRequests, wrapped
				}
				reuseEntry = nil
				res, err = am.ReserveFrom(r.Context(), params.Model.ID, params.TestAccountIDs, tried)
			}
		} else {
			res, err = am.ReserveFrom(r.Context(), params.Model.ID, params.TestAccountIDs, tried)
		}
		if err != nil {
			if lastErr != nil {
				recordRequestEvent(RequestEvent{RequestID: reqID, Route: route, Model: params.Model.ID, CallerKeyHash: callerHash, Attempt: finalAttempts, Status: "error", HTTPStatus: statusForClass(lastClass), ErrorClass: lastClass, Error: lastErr.Error(), Retry: len(tried) > 1, Stream: params.Stream, LatencyMS: time.Since(start).Milliseconds(), ToolCallCount: len(params.Tools), ReuseHit: reuseHit, ReuseMissReason: reuseMissReason})
				return nil, statusForClass(lastClass), lastErr
			}
			recordRequestEvent(RequestEvent{RequestID: reqID, Route: route, Model: params.Model.ID, CallerKeyHash: callerHash, Attempt: finalAttempts, Status: "error", HTTPStatus: http.StatusServiceUnavailable, Error: err.Error(), Retry: len(tried) > 0, Stream: params.Stream, LatencyMS: time.Since(start).Milliseconds(), ToolCallCount: len(params.Tools), ReuseHit: reuseHit, ReuseMissReason: reuseMissReason})
			return nil, http.StatusServiceUnavailable, err
		}
		sendStarted := time.Now()
		firstTextMS := int64(0)
		var result *windsurf.ChatResult
		tried = append(tried, res.Account.ID)
		if params.HTTPWriter != nil {
			params.HTTPWriter.Header().Set("X-Windsurf-Account-ID", fmt.Sprintf("%d", res.Account.ID))
			params.HTTPWriter.Header().Set("X-Windsurf-Attempt", fmt.Sprintf("%d", attempt))
			params.HTTPWriter.Header().Set("X-Windsurf-Tool-Mode", string(direct.ToolModeForRequest(params.Model, params.Tools, params.ToolChoice, params.Messages)))
		}
		proxyRes := proxypool.Reservation{ProxyURL: res.Account.ProxyURL}
		if params.ProxyPool != nil {
			proxyRes = params.ProxyPool.ReserveForAccount(res.Account.ID, res.Account.ProxyURL)
		}

		req := direct.ChatRequest{
			APIKey:       res.Account.FirebaseToken,
			ProxyURL:     proxyRes.ProxyURL,
			Model:        params.Model,
			ReportedName: params.DisplayModelID,
			Messages:     params.Messages,
			Thinking:     params.Thinking,
			Tools:        params.Tools,
			ToolChoice:   params.ToolChoice,
		}
		if params.ParallelToolCalls != nil && !*params.ParallelToolCalls {
			req.DisableParallelToolCalls = true
		}
		if params.Stream {
			var firstDeltaAt time.Time
			req.OnFirstDelta = func() {
				if emitted {
					return
				}
				firstDeltaAt = time.Now()
				if params.OnStreamStart != nil {
					_ = params.OnStreamStart()
				}
				emitted = true
				firstTextMS = firstDeltaAt.Sub(sendStarted).Milliseconds()
				log.Printf("req_id=%s route=%s event=first_delta model=%s account_id=%d attempt=%d first_text_ms=%d reuse_hit=%v tool_call_count=%d", reqID, route, params.Model.ID, res.Account.ID, attempt, firstDeltaAt.Sub(sendStarted).Milliseconds(), reuseHit, len(params.Tools))
			}
			req.OnDelta = func(delta string) error {
				if params.OnTextDelta == nil {
					return nil
				}
				return params.OnTextDelta(delta)
			}
			req.OnThinkingDelta = func(delta string) error {
				if params.OnThinkingDelta == nil {
					return nil
				}
				return params.OnThinkingDelta(delta)
			}
			req.OnToolCallDelta = func(index int, delta windsurf.ToolCall) error {
				if params.OnToolCallDelta == nil {
					return nil
				}
				return params.OnToolCallDelta(index, delta)
			}
		}

		result, err = dc.Chat(r.Context(), req)
		if err == nil {
			if params.ProxyPool != nil {
				params.ProxyPool.RecordSuccess(proxyRes)
				params.ProxyPool.Release(proxyRes)
			}
			am.RecordSuccess(res, result.Usage)
			am.Release(res)
			checkinFingerprints := []string{}
			if rp != nil {
				for _, fp := range []string{
					exactFingerprint,
					reusepool.FingerprintAfterWithOptions(params.Model.ID, params.CallerKey, route, toolsDigest, toolChoiceDigest, params.Messages, result),
					reusepool.FingerprintAfterWithOptions(params.Model.ID, params.CallerKey, route, "", "", params.Messages, result),
					checkoutFingerprint,
				} {
					if fp != "" && !containsString(checkinFingerprints, fp) {
						checkinFingerprints = append(checkinFingerprints, fp)
					}
				}
			}
			if rp != nil && len(checkinFingerprints) > 0 {
				entry := reuseEntry
				if entry == nil {
					entry = &reusepool.Entry{
						AccountID:  res.Account.ID,
						APIKeyHash: reusepool.APIKeyHash(res.Account.FirebaseToken),
						ModelID:    params.Model.ID,
						CallerKey:  params.CallerKey,
					}
				} else {
					entry.AccountID = res.Account.ID
					entry.APIKeyHash = reusepool.APIKeyHash(res.Account.FirebaseToken)
					entry.ModelID = params.Model.ID
					entry.CallerKey = params.CallerKey
				}
				for _, fp := range checkinFingerprints {
					cp := *entry
					rp.Checkin(fp, &cp, reuseTTL)
				}
			}
			log.Printf("req_id=%s route=%s event=direct_chat_ok model=%s tool_mode=%s account_id=%d attempt=%d total_ms=%d send_ms=%d reuse_hit=%v usage_in=%d usage_out=%d cache_read=%d tool_call_count=%d",
				reqID, route, params.Model.ID, direct.ToolModeForRequest(params.Model, params.Tools, params.ToolChoice, params.Messages), res.Account.ID, attempt, time.Since(start).Milliseconds(), time.Since(sendStarted).Milliseconds(), reuseHit,
				usageIn(result), usageOut(result), usageCacheRead(result), len(result.ToolCalls))
			responseUsage := buildResponseUsage(params, res.Account.ID, result)
			if params.UsageOut != nil {
				*params.UsageOut = responseUsage
			}
			recordRequestEvent(RequestEvent{
				RequestID:       reqID,
				Route:           route,
				Model:           params.Model.ID,
				CallerKeyHash:   callerHash,
				AccountID:       res.Account.ID,
				Attempt:         attempt,
				Status:          "ok",
				HTTPStatus:      http.StatusOK,
				Retry:           attempt > 1,
				Stream:          params.Stream,
				LatencyMS:       time.Since(start).Milliseconds(),
				SendMS:          time.Since(sendStarted).Milliseconds(),
				FirstTextMS:     firstTextMS,
				UsageInput:      responseUsage.InputTokens,
				UsageOutput:     responseUsage.OutputTokens,
				UsageCacheRead:  responseUsage.CacheReadInputTokens,
				ToolCallCount:   len(result.ToolCalls),
				ReuseHit:        reuseHit,
				ReuseMissReason: reuseMissReason,
			})
			if params.Stream {
				if params.OnStreamFinish != nil {
					_ = params.OnStreamFinish(result)
				}
			}
			return result, http.StatusOK, nil
		}

		class := classifyError(err)
		if params.ProxyPool != nil && proxyFailureClass(class) {
			params.ProxyPool.RecordFailure(proxyRes, err)
		}
		if params.ProxyPool != nil {
			params.ProxyPool.Release(proxyRes)
		}
		lastErr, lastClass = err, class
		am.RecordFailure(res, class, err)
		switch class {
		case account.ErrorRateLimit, account.ErrorModelNotAvailable:
			_ = am.MarkCooldown(res.Account.ID, params.Model.ID, cooldownUntilForError(time.Now(), class, err), err.Error())
		case account.ErrorBanSignal:
			_ = am.MarkBanned(res.Account.ID)
		case account.ErrorPolicyBlocked:
			am.Release(res)
			if rp != nil && reuseEntry != nil && checkoutFingerprint != "" {
				rp.Checkin(checkoutFingerprint, reuseEntry, reuseTTL)
			}
			recordRequestEvent(RequestEvent{RequestID: reqID, Route: route, Model: params.Model.ID, CallerKeyHash: callerHash, AccountID: res.Account.ID, Attempt: attempt, Status: "error", HTTPStatus: statusForClass(class), ErrorClass: class, Error: err.Error(), Retry: attempt > 1, Stream: params.Stream, LatencyMS: time.Since(start).Milliseconds(), SendMS: time.Since(sendStarted).Milliseconds(), FirstTextMS: firstTextMS, ToolCallCount: len(params.Tools), ReuseHit: reuseHit, ReuseMissReason: reuseMissReason})
			return nil, statusForClass(class), err
		}
		am.Release(res)
		if params.Stream && emitted {
			if params.OnStreamError != nil {
				_ = params.OnStreamError(class, err)
			}
			if params.OnStreamFinish != nil {
				_ = params.OnStreamFinish(&windsurf.ChatResult{FinishReason: "stop"})
			}
			recordRequestEvent(RequestEvent{RequestID: reqID, Route: route, Model: params.Model.ID, CallerKeyHash: callerHash, AccountID: res.Account.ID, Attempt: attempt, Status: "stream_error_after_delta", HTTPStatus: http.StatusOK, ErrorClass: class, Error: err.Error(), Retry: attempt > 1, Stream: true, LatencyMS: time.Since(start).Milliseconds(), SendMS: time.Since(sendStarted).Milliseconds(), FirstTextMS: firstTextMS, ToolCallCount: len(params.Tools), ReuseHit: reuseHit, ReuseMissReason: reuseMissReason})
			return nil, http.StatusOK, nil
		}
		if !retryableClass(class) || time.Since(start) >= fastSwitchBudget {
			log.Printf("req_id=%s route=%s event=direct_chat_fail model=%s tool_mode=%s account_id=%d attempt=%d class=%s retry=false total_ms=%d err=%s %s", reqID, route, params.Model.ID, direct.ToolModeForRequest(params.Model, params.Tools, params.ToolChoice, params.Messages), res.Account.ID, attempt, class, time.Since(start).Milliseconds(), redact.Text(err.Error()), shape)
			recordRequestEvent(RequestEvent{RequestID: reqID, Route: route, Model: params.Model.ID, CallerKeyHash: callerHash, AccountID: res.Account.ID, Attempt: attempt, Status: "error", HTTPStatus: statusForClass(class), ErrorClass: class, Error: err.Error(), Retry: attempt > 1, Stream: params.Stream, LatencyMS: time.Since(start).Milliseconds(), SendMS: time.Since(sendStarted).Milliseconds(), ToolCallCount: len(params.Tools), ReuseHit: reuseHit, ReuseMissReason: reuseMissReason})
			return nil, statusForClass(class), err
		}
		log.Printf("req_id=%s route=%s event=direct_chat_retry model=%s tool_mode=%s account_id=%d attempt=%d class=%s retry=true total_ms=%d err=%s %s", reqID, route, params.Model.ID, direct.ToolModeForRequest(params.Model, params.Tools, params.ToolChoice, params.Messages), res.Account.ID, attempt, class, time.Since(start).Milliseconds(), redact.Text(err.Error()), shape)
	}
	return nil, statusForClass(lastClass), lastErr
}

func directRequestShape(params directChatParams) string {
	var systemBytes, userBytes, assistantBytes, toolResultBytes, toolHistory int
	for _, msg := range params.Messages {
		n := len(msg.Content)
		switch strings.ToLower(strings.TrimSpace(msg.Role)) {
		case "system":
			systemBytes += n
		case "assistant":
			assistantBytes += n
			toolHistory += len(msg.ToolCalls)
		case "tool":
			toolResultBytes += n
			if strings.TrimSpace(msg.ToolCallID) != "" {
				toolHistory++
			}
		default:
			userBytes += n
		}
	}
	choice := "none"
	if params.ToolChoice != nil {
		switch {
		case strings.TrimSpace(params.ToolChoice.ToolName) != "":
			choice = "tool:" + sanitize.Text(params.ToolChoice.ToolName)
		case strings.TrimSpace(params.ToolChoice.OptionName) != "":
			choice = sanitize.Text(params.ToolChoice.OptionName)
		}
	}
	return fmt.Sprintf("shape_turns=%d system_bytes=%d user_bytes=%d assistant_bytes=%d tool_result_bytes=%d tools=%d tool_history=%d tool_choice=%s thinking_bytes=%d stream=%v",
		len(params.Messages), systemBytes, userBytes, assistantBytes, toolResultBytes, len(params.Tools), toolHistory, choice, len(params.Thinking), params.Stream)
}

func writeUnaryResponse(w http.ResponseWriter, modelID string, result *windsurf.ChatResult, inputTokenEstimate ...uint64) {
	result = sanitizeChatResult(result)
	message := map[string]any{"role": "assistant", "content": result.Text}
	if strings.TrimSpace(result.Thinking) != "" {
		message["reasoning_content"] = result.Thinking
	}
	if len(result.ToolCalls) > 0 {
		message["content"] = nil
		message["tool_calls"] = openAIToolCalls(result.ToolCalls)
	}
	resp := map[string]any{
		"id":      fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   modelID,
		"choices": []any{map[string]any{
			"index":         0,
			"message":       message,
			"finish_reason": finishReason(result),
		}},
		"usage": usageMap(usagepkg.FromUpstream(result.Usage, firstUint64(inputTokenEstimate))),
	}
	setNoStore(w)
	w.Header().Set("Content-Type", "application/json")
	writeJSONNoEscape(w, resp)
}

func openAIToolCalls(calls []windsurf.ToolCall) []any {
	out := make([]any, 0, len(calls))
	for i, call := range calls {
		call = sanitize.ToolCall(call)
		id := call.ID
		if id == "" {
			id = fmt.Sprintf("call_%d", i)
		}
		out = append(out, map[string]any{
			"id":    id,
			"type":  "function",
			"index": i,
			"function": map[string]any{
				"name":      call.Name,
				"arguments": call.ArgumentsJSON,
			},
		})
	}
	return out
}

func sanitizeChatResult(result *windsurf.ChatResult) *windsurf.ChatResult {
	if result == nil {
		return &windsurf.ChatResult{FinishReason: "stop"}
	}
	cp := *result
	cp.Text = sanitize.Text(cp.Text)
	cp.Thinking = sanitize.Text(cp.Thinking)
	for i := range cp.ToolCalls {
		cp.ToolCalls[i] = sanitize.ToolCall(cp.ToolCalls[i])
	}
	return &cp
}

func writeJSONError(w http.ResponseWriter, code int, msg string) {
	writeOpenAIError(w, code, msg, "windsurf_error")
}

func writeOpenAIError(w http.ResponseWriter, code int, msg, typ string) {
	msg = redact.Text(msg)
	setNoStore(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if strings.TrimSpace(typ) == "" {
		typ = "windsurf_error"
	}
	writeJSONNoEscape(w, map[string]any{
		"error": map[string]any{"message": msg, "type": typ},
	})
}

func writeJSONNoEscape(w http.ResponseWriter, body any) {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(body)
}

func callerKey(r *http.Request) string {
	return callerKeyForBody(r, nil)
}

func callerKeyForBody(r *http.Request, body any) string {
	if v := strings.TrimSpace(r.Header.Get("X-Caller-Key")); v != "" {
		if sub := extractBodyCallerSubKey(body); sub != "" {
			return v + ":user:" + sub
		}
		return v
	}
	if token := extractAPIKey(r); token != "" {
		base := "api:" + hashPrefix(token, 32)
		if sub := extractBodyCallerSubKey(body); sub != "" {
			return base + ":user:" + sub
		}
		if fp := clientFingerprint(r); fp != "" {
			return base + ":client:" + fp
		}
		return base
	}
	if sessionID := strings.TrimSpace(r.Header.Get("X-Session-ID")); sessionID != "" {
		base := "session:" + hashPrefix(sessionID, 32)
		if sub := extractBodyCallerSubKey(body); sub != "" {
			return base + ":user:" + sub
		}
		return base
	}
	base := "client:" + hashPrefix(clientIP(r)+"\x00"+r.UserAgent(), 32)
	if sub := extractBodyCallerSubKey(body); sub != "" {
		return base + ":user:" + sub
	}
	return base
}

func shortHash(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return hashPrefix(value, 12)
}

func hashPrefix(value string, n int) string {
	sum := sha256.Sum256([]byte(value))
	hexed := hex.EncodeToString(sum[:])
	if n <= 0 || n > len(hexed) {
		n = len(hexed)
	}
	return hexed[:n]
}

func clientFingerprint(r *http.Request) string {
	if r == nil {
		return ""
	}
	raw := clientIP(r) + "\x00" + r.UserAgent()
	if strings.Trim(raw, "\x00 ") == "" {
		return ""
	}
	return hashPrefix(raw, 16)
}

func extractBodyCallerSubKey(body any) string {
	m := bodyToMap(body)
	if len(m) == 0 {
		return ""
	}
	candidates := []string{}
	for _, key := range []string{"user", "conversation", "previous_response_id"} {
		if s := stringMapValue(m, key); s != "" {
			candidates = append(candidates, s)
		}
	}
	if metadata, ok := m["metadata"].(map[string]any); ok {
		for _, key := range []string{"conversation_id", "session_id"} {
			if s := stringMapValue(metadata, key); s != "" {
				candidates = append(candidates, s)
			}
		}
		if sub := anthropicUserIDSubKey(metadata["user_id"]); sub != "" {
			candidates = append(candidates, sub)
		}
	}
	if len(candidates) == 0 {
		return ""
	}
	return hashPrefix(strings.Join(candidates, "|"), 16)
}

func bodyToMap(body any) map[string]any {
	switch v := body.(type) {
	case nil:
		return nil
	case map[string]any:
		return v
	default:
		raw, err := json.Marshal(v)
		if err != nil {
			return nil
		}
		var out map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil
		}
		return out
	}
}

func stringMapValue(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if s, ok := m[key].(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func anthropicUserIDSubKey(value any) string {
	raw, ok := value.(string)
	if !ok {
		return ""
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err == nil {
		for _, key := range []string{"device_id", "deviceId", "session_id", "sessionId", "account_uuid", "accountUuid"} {
			if s := stringMapValue(parsed, key); s != "" {
				return s
			}
		}
	}
	return raw
}

func directToolsDigest(tools []direct.ToolDefinition) string {
	if len(tools) == 0 {
		return ""
	}
	raw, _ := json.Marshal(tools)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])[:16]
}

func directToolChoiceDigest(choice *direct.ToolChoice) string {
	if choice == nil {
		return ""
	}
	raw, _ := json.Marshal(choice)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])[:16]
}

func containsString(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}

func strictReuseRequested(r *http.Request) bool {
	if r == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(r.Header.Get("X-Windsurf-Strict-Reuse"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func modelAllowed(access *modelaccess.Manager, modelID string) (bool, string) {
	if access == nil {
		return true, ""
	}
	return access.IsEnabled(modelID)
}

func modelUnsupportedReason(model *models.Model) string {
	if model == nil {
		return "unknown model"
	}
	if strings.TrimSpace(model.UnsupportedReason) != "" {
		return model.UnsupportedReason
	}
	if model.Deprecated {
		return "deprecated model"
	}
	return "model is cataloged but not enabled for the direct backend"
}

func finishReason(result *windsurf.ChatResult) string {
	if result == nil || result.FinishReason == "" {
		return "stop"
	}
	return result.FinishReason
}

func usageMap(u usagepkg.Usage) map[string]any {
	return usagepkg.OpenAIMap(u)
}

func usageToSSE(u usagepkg.Usage) *sse.Usage {
	if u.InputTokens == 0 && u.OutputTokens == 0 && u.CacheReadInputTokens == 0 && u.CacheCreationInputTokens == 0 {
		return nil
	}
	return &sse.Usage{
		PromptTokens:      u.InputTokens,
		CompletionTokens:  u.OutputTokens,
		TotalTokens:       u.InputTokens + u.OutputTokens,
		CachedInputTokens: u.CacheReadInputTokens,
	}
}

func buildResponseUsage(params directChatParams, accountID int, result *windsurf.ChatResult) usagepkg.Usage {
	input := usagepkg.Input{
		AccountID:             accountID,
		Model:                 "",
		CallerKeyHash:         shortHash(params.CallerKey),
		Route:                 params.Route,
		EstimatedInputTokens:  params.InputTokenEstimate,
		EstimatedUserDeltaTok: params.InputTokenEstimate,
	}
	if params.Model != nil {
		input.Model = params.Model.ID
	}
	if result != nil && result.Usage != nil {
		input.ObservedInputTokens = result.Usage.InputTokens
		input.OutputTokens = result.Usage.OutputTokens
	}
	if params.VirtualUsage != nil {
		return params.VirtualUsage.Build(input)
	}
	if result == nil {
		return usagepkg.FromUpstream(nil, params.InputTokenEstimate)
	}
	return usagepkg.FromUpstream(result.Usage, params.InputTokenEstimate)
}

func responseModelID(requested string, resolved *models.Model) string {
	raw := cleanModelID(requested)
	if raw == "" {
		if resolved != nil {
			return resolved.ID
		}
		return ""
	}
	normalized := models.NormalizeModelID(raw)
	for _, publicID := range models.PublicModelIDs {
		if raw == publicID {
			return publicID
		}
		if normalized != "" && normalized == models.ResolveModelIDForRequest(publicID, "") {
			return publicID
		}
	}
	return raw
}

func cleanModelID(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		}
	}
	return b.String()
}

func estimateInputTokens(messages []windsurf.ChatMessage, tools []direct.ToolDefinition) uint64 {
	chars := 0
	for _, msg := range messages {
		chars += len(msg.Role) + len(msg.Content) + 12
		if msg.ToolCallID != "" {
			chars += len(msg.ToolCallID)
		}
		for _, call := range msg.ToolCalls {
			chars += len(call.ID) + len(call.Name) + len(call.ArgumentsJSON) + 12
		}
	}
	for _, tool := range tools {
		chars += len(tool.Name) + len(tool.Description) + len(tool.SchemaJSON) + 64
	}
	if chars <= 0 {
		return 0
	}
	tokens := uint64((chars + 3) / 4)
	if tokens == 0 {
		tokens = 1
	}
	return tokens
}

func firstUint64(values []uint64) uint64 {
	if len(values) == 0 {
		return 0
	}
	return values[0]
}

func usageIn(r *windsurf.ChatResult) uint64 {
	if r == nil || r.Usage == nil {
		return 0
	}
	return r.Usage.InputTokens
}

func usageOut(r *windsurf.ChatResult) uint64 {
	if r == nil || r.Usage == nil {
		return 0
	}
	return r.Usage.OutputTokens
}

func usageCacheRead(r *windsurf.ChatResult) uint64 {
	if r == nil || r.Usage == nil {
		return 0
	}
	return r.Usage.CacheReadTokens
}
