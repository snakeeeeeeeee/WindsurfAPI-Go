package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/zhangyu/windsurfapi-go/internal/account"
	"github.com/zhangyu/windsurfapi-go/internal/modelaccess"
	"github.com/zhangyu/windsurfapi-go/internal/models"
	proxypool "github.com/zhangyu/windsurfapi-go/internal/proxy"
	"github.com/zhangyu/windsurfapi-go/internal/redact"
	reusepool "github.com/zhangyu/windsurfapi-go/internal/reuse"
	"github.com/zhangyu/windsurfapi-go/internal/sanitize"
	"github.com/zhangyu/windsurfapi-go/internal/windsurf"
	"github.com/zhangyu/windsurfapi-go/internal/windsurf/direct"
)

type ResponsesRequest struct {
	Model            string  `json:"model"`
	Input            any     `json:"input"`
	Instructions     string  `json:"instructions,omitempty"`
	Tools            []any   `json:"tools,omitempty"`
	ToolChoice       any     `json:"tool_choice,omitempty"`
	Text             any     `json:"text,omitempty"`
	MaxOutputTokens  int     `json:"max_output_tokens,omitempty"`
	Temperature      float64 `json:"temperature,omitempty"`
	TopP             float64 `json:"top_p,omitempty"`
	Reasoning        any     `json:"reasoning,omitempty"`
	Metadata         any     `json:"metadata,omitempty"`
	User             string  `json:"user,omitempty"`
	Conversation     string  `json:"conversation,omitempty"`
	PreviousResponse string  `json:"previous_response_id,omitempty"`
	Stream           bool    `json:"stream,omitempty"`
	StrictReuse      *bool   `json:"strict_reuse,omitempty"`
}

type responsesStreamWriter struct {
	w                    http.ResponseWriter
	flusher              http.Flusher
	id                   string
	model                string
	textItemID           string
	textOutputIndex      int
	reasoningItemID      string
	reasoningOutputIndex int
	reasoningText        string
	reasoningStarted     bool
	reasoningDone        bool
	closed               bool
	textStarted          bool
	toolStarted          map[int]bool
	toolPendingArgsIndex map[int]int
	toolOutputIndex      map[int]int
	nextOutputIndex      int
	toolMeta             map[string]responseToolMeta
}

type responseToolMeta struct {
	Type         string
	Namespace    string
	OriginalName string
}

func ResponsesHandler(am *account.Manager, dc directChatClient, rp *reusepool.Pool, access *modelaccess.Manager, pp ...*proxypool.Manager) http.HandlerFunc {
	var proxyPool *proxypool.Manager
	if len(pp) > 0 {
		proxyPool = pp[0]
	}
	return func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		setNoStore(w)
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var req ResponsesRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid request: "+err.Error())
			return
		}
		model := models.ResolveModelForRequest(req.Model, responsesReasoningEffort(req.Reasoning))
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
		setOpenAIHeaders(w, model.ID, 0)
		msgs, err := responsesInputToMessages(req)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if hint := responsesTextFormatHint(req.Text); hint != "" {
			msgs = prependSystemHint(msgs, hint)
		}
		tools, toolMeta, err := responsesToolsToDirectWithMeta(req.Tools)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		choice, err := responsesToolChoiceToDirect(req.ToolChoice)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		choice = pruneDirectToolChoice(choice, tools)
		openMsgs := windsurfToOpenAIMessages(msgs)
		if shouldSuppressToolsForContinuation(openMsgs, choice) || isCompactionRequest(msgs) {
			tools = nil
			choice = nil
		}
		thinking := responsesReasoningPrompt(req.Reasoning)

		var stream *responsesStreamWriter
		params := directChatParams{
			Model:          model,
			Messages:       msgs,
			CallerKey:      callerKeyForBody(r, req),
			Route:          "responses",
			Stream:         req.Stream,
			HTTPWriter:     w,
			ProxyPool:      proxyPool,
			Thinking:       thinking,
			Tools:          tools,
			ToolChoice:     choice,
			TestAccountIDs: testAccountIDsFromRequest(r),
			StrictReuse: func() bool {
				if req.StrictReuse != nil {
					return *req.StrictReuse
				}
				return strictReuseRequested(r)
			}(),
		}
		if req.Stream {
			params.OnStreamStart = func() error {
				var err error
				stream, err = newResponsesStreamWriter(w, model.ID, toolMeta)
				return err
			}
			params.OnTextDelta = func(delta string) error {
				return stream.TextDelta(delta)
			}
			params.OnThinkingDelta = func(delta string) error {
				return stream.ThinkingDelta(delta)
			}
			params.OnToolCallDelta = func(index int, call windsurf.ToolCall) error {
				return stream.ToolCallDelta(index, call)
			}
			params.OnStreamError = func(class account.ErrorClass, err error) error {
				if stream == nil {
					return fmt.Errorf("stream writer unavailable")
				}
				return stream.Error(class, err)
			}
			params.OnStreamFinish = func(result *windsurf.ChatResult) error {
				if stream == nil {
					var err error
					stream, err = newResponsesStreamWriter(w, model.ID, toolMeta)
					if err != nil {
						return err
					}
				}
				return stream.Finish(result)
			}
		}

		result, status, err := executeDirectChat(r, dc, am, rp, params)
		if err != nil {
			writeJSONError(w, status, err.Error())
			return
		}
		if req.Stream {
			return
		}
		setOpenAIHeaders(w, model.ID, time.Since(started))
		writeResponsesResponseWithMeta(w, model.ID, result, toolMeta)
	}
}

func responsesInputToMessages(req ResponsesRequest) ([]windsurf.ChatMessage, error) {
	var out []windsurf.ChatMessage
	pending := responseToolCallBuffer{}
	flushPending := func() {
		if msg, ok := pending.Flush(); ok {
			out = append(out, msg)
		}
	}
	if strings.TrimSpace(req.Instructions) != "" {
		out = append(out, windsurf.ChatMessage{Role: "system", Content: req.Instructions})
	}
	switch v := req.Input.(type) {
	case string:
		flushPending()
		out = append(out, windsurf.ChatMessage{Role: "user", Content: v})
	case []any:
		for _, item := range v {
			msg, action, err := responseItemToMessage(item)
			if err != nil {
				return nil, err
			}
			switch action {
			case responseItemSkip:
				continue
			case responseItemToolCall:
				pending.Add(msg.ToolCalls...)
			case responseItemMessage:
				flushPending()
				out = append(out, msg)
			}
		}
		flushPending()
	default:
		if req.Input == nil {
			return nil, fmt.Errorf("input required")
		}
		raw, _ := json.Marshal(req.Input)
		flushPending()
		out = append(out, windsurf.ChatMessage{Role: "user", Content: string(raw)})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("input required")
	}
	return out, nil
}

type responseItemAction int

const (
	responseItemSkip responseItemAction = iota
	responseItemMessage
	responseItemToolCall
)

type responseToolCallBuffer struct {
	calls []windsurf.ToolCall
}

func (b *responseToolCallBuffer) Add(calls ...windsurf.ToolCall) {
	for _, call := range calls {
		b.calls = append(b.calls, call)
	}
}

func (b *responseToolCallBuffer) Flush() (windsurf.ChatMessage, bool) {
	if len(b.calls) == 0 {
		return windsurf.ChatMessage{}, false
	}
	calls := append([]windsurf.ToolCall(nil), b.calls...)
	b.calls = nil
	return windsurf.ChatMessage{Role: "assistant", ToolCalls: calls}, true
}

func responseItemToMessage(item any) (windsurf.ChatMessage, responseItemAction, error) {
	m, ok := item.(map[string]any)
	if !ok {
		raw, _ := json.Marshal(item)
		return windsurf.ChatMessage{Role: "user", Content: string(raw)}, responseItemMessage, nil
	}
	typ, _ := m["type"].(string)
	switch typ {
	case "message", "":
		role, _ := m["role"].(string)
		if role == "" {
			role = "user"
		}
		return windsurf.ChatMessage{Role: role, Content: responseContentText(m["content"])}, responseItemMessage, nil
	case "input_text":
		return windsurf.ChatMessage{Role: "user", Content: responseContentText(m)}, responseItemMessage, nil
	case "output_text":
		return windsurf.ChatMessage{Role: "assistant", Content: responseContentText(m)}, responseItemMessage, nil
	case "function_call":
		id, _ := m["call_id"].(string)
		if id == "" {
			id, _ = m["id"].(string)
		}
		name, _ := m["name"].(string)
		args := stringifyMaybe(m["arguments"])
		return windsurf.ChatMessage{Role: "assistant", ToolCalls: []windsurf.ToolCall{{ID: id, Name: name, ArgumentsJSON: args}}}, responseItemToolCall, nil
	case "function_call_output":
		id, _ := m["call_id"].(string)
		return windsurf.ChatMessage{Role: "tool", ToolCallID: id, Content: responseContentText(m["output"])}, responseItemMessage, nil
	case "custom_tool_call":
		id, _ := m["call_id"].(string)
		if id == "" {
			id, _ = m["id"].(string)
		}
		name, _ := m["name"].(string)
		input := responseContentText(m["input"])
		args, _ := json.Marshal(map[string]string{"input": input})
		return windsurf.ChatMessage{Role: "assistant", ToolCalls: []windsurf.ToolCall{{ID: id, Name: name, ArgumentsJSON: string(args)}}}, responseItemToolCall, nil
	case "custom_tool_call_output":
		id, _ := m["call_id"].(string)
		if id == "" {
			id, _ = m["id"].(string)
		}
		return windsurf.ChatMessage{Role: "tool", ToolCallID: id, Content: responseContentText(m["output"])}, responseItemMessage, nil
	default:
		return windsurf.ChatMessage{}, responseItemSkip, fmt.Errorf("unsupported response input item type: %s", typ)
	}
}

func responseContentText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case map[string]any:
		if s, _ := x["text"].(string); s != "" {
			return s
		}
		if s, _ := x["output"].(string); s != "" {
			return s
		}
		if s, _ := x["content"].(string); s != "" {
			return s
		}
		if c, ok := x["content"]; ok {
			return responseContentText(c)
		}
		raw, _ := json.Marshal(x)
		return string(raw)
	case []any:
		var parts []string
		for _, item := range x {
			if m, ok := item.(map[string]any); ok {
				typ, _ := m["type"].(string)
				if typ == "input_text" || typ == "output_text" || typ == "text" {
					if s, _ := m["text"].(string); s != "" {
						parts = append(parts, s)
					}
				}
			}
		}
		return strings.Join(parts, "\n")
	case nil:
		return ""
	default:
		raw, _ := json.Marshal(x)
		return string(raw)
	}
}

func responsesToolChoiceToDirect(choice any) (*direct.ToolChoice, error) {
	normalized := normalizeResponsesToolChoice(choice)
	return toDirectToolChoiceWithKind(normalized, "responses tool_choice")
}

func responsesTextFormatHint(text any) string {
	m, ok := text.(map[string]any)
	if !ok || m == nil {
		return ""
	}
	format, ok := m["format"].(map[string]any)
	if !ok || format == nil {
		return ""
	}
	typ, _ := format["type"].(string)
	switch typ {
	case "json_object":
		return responseFormatHint(map[string]any{"type": "json_object"})
	case "json_schema":
		nested := objectValue(format["json_schema"])
		name, _ := format["name"].(string)
		if strings.TrimSpace(name) == "" {
			name = firstNonEmpty(stringValue(nested["name"]), "response")
		}
		strict, ok := format["strict"].(bool)
		if !ok {
			if nestedStrict, nestedOK := nested["strict"].(bool); nestedOK {
				strict = nestedStrict
			} else {
				strict = false
			}
		}
		schema := format["schema"]
		if schema == nil {
			schema = nested["schema"]
		}
		return responseFormatHint(map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":   name,
				"schema": schema,
				"strict": strict,
			},
		})
	default:
		return ""
	}
}

func responsesReasoningPrompt(reasoning any) string {
	effort := responsesReasoningEffort(reasoning)
	if effort == "" {
		return ""
	}
	switch effort {
	case "none", "off", "disabled", "false":
		return ""
	case "low":
		return "Responses reasoning is requested with low effort. Use private reasoning as needed and return any upstream thinking in the reasoning output when available."
	case "medium":
		return "Responses reasoning is requested with medium effort. Use private reasoning as needed and return any upstream thinking in the reasoning output when available."
	case "high", "xhigh", "max":
		return "Responses reasoning is requested with high effort. Use private reasoning as needed and return any upstream thinking in the reasoning output when available."
	default:
		return "Responses reasoning is requested. Use private reasoning as needed and return any upstream thinking in the reasoning output when available."
	}
}

func responsesReasoningEffort(reasoning any) string {
	m, ok := reasoning.(map[string]any)
	if !ok || m == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(stringValue(m["effort"])))
}

func responsesToolsToDirect(tools []any) ([]direct.ToolDefinition, error) {
	out, _, err := responsesToolsToDirectWithMeta(tools)
	return out, err
}

func responsesToolsToDirectWithMeta(tools []any) ([]direct.ToolDefinition, map[string]responseToolMeta, error) {
	var openTools []Tool
	meta := map[string]responseToolMeta{}
	for i, item := range tools {
		converted, convertedMeta, err := flattenResponseTool(item, "")
		if err != nil {
			return nil, nil, fmt.Errorf("tool at index %d: %w", i, err)
		}
		openTools = append(openTools, converted...)
		for name, info := range convertedMeta {
			meta[name] = info
		}
	}
	directTools, err := toDirectTools(openTools)
	if err != nil {
		return nil, nil, err
	}
	return directTools, meta, nil
}

func flattenResponseTool(item any, namespace string) ([]Tool, map[string]responseToolMeta, error) {
	m, ok := item.(map[string]any)
	if !ok {
		return nil, nil, fmt.Errorf("must be an object")
	}
	typ, _ := m["type"].(string)
	if typ == "" {
		typ = "function"
	}
	switch typ {
	case "namespace":
		ns := firstNonEmpty(stringValue(m["name"]), stringValue(m["namespace"]), namespace)
		children := firstArrayValue(m, "tools", "children", "functions", "items")
		var out []Tool
		meta := map[string]responseToolMeta{}
		for _, child := range children {
			childTools, childMeta, err := flattenResponseTool(child, ns)
			if err != nil {
				return nil, nil, err
			}
			out = append(out, childTools...)
			for name, info := range childMeta {
				meta[name] = info
			}
		}
		return out, meta, nil
	case "function":
		base := objectValue(m["function"])
		if base == nil {
			base = m
		}
		original := firstNonEmpty(stringValue(base["name"]), stringValue(m["name"]), "unknown")
		name := encodeResponseToolName(original, namespace)
		params := base["parameters"]
		if params == nil {
			params = m["parameters"]
		}
		if params == nil {
			params = m["input_schema"]
		}
		tool := Tool{Type: "function", Function: ToolFunction{Name: name, Description: firstNonEmpty(stringValue(base["description"]), stringValue(m["description"])), Parameters: params}}
		meta := map[string]responseToolMeta{name: {Type: "function", Namespace: namespace, OriginalName: original}}
		if namespace != "" {
			meta[name] = responseToolMeta{Type: "namespace", Namespace: namespace, OriginalName: original}
		}
		return []Tool{tool}, meta, nil
	case "custom":
		base := objectValue(m["function"])
		if base == nil {
			base = m
		}
		original := firstNonEmpty(stringValue(base["name"]), stringValue(m["name"]))
		if original == "" {
			return nil, map[string]responseToolMeta{}, nil
		}
		name := encodeResponseToolName(original, namespace)
		schema := map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"input": map[string]any{"type": "string", "description": "Raw custom tool input."},
			},
			"required": []any{"input"},
		}
		tool := Tool{Type: "function", Function: ToolFunction{Name: name, Description: firstNonEmpty(stringValue(base["description"]), stringValue(m["description"])), Parameters: schema}}
		return []Tool{tool}, map[string]responseToolMeta{name: {Type: "custom", Namespace: namespace, OriginalName: original}}, nil
	case "web_search", "web_search_preview":
		name := encodeResponseToolName("web_search", namespace)
		schema := map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "Search query."},
			},
			"required": []any{"query"},
		}
		tool := Tool{Type: "function", Function: ToolFunction{Name: name, Description: firstNonEmpty(stringValue(m["description"]), "Search the web."), Parameters: schema}}
		return []Tool{tool}, map[string]responseToolMeta{name: {Type: "web_search", Namespace: namespace, OriginalName: "web_search"}}, nil
	case "tool_search":
		name := encodeResponseToolName("tool_search", namespace)
		schema := map[string]any{
			"type":                 "object",
			"additionalProperties": true,
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "Tool search query."},
			},
		}
		tool := Tool{Type: "function", Function: ToolFunction{Name: name, Description: firstNonEmpty(stringValue(m["description"]), "Search available tools."), Parameters: schema}}
		return []Tool{tool}, map[string]responseToolMeta{name: {Type: "tool_search", Namespace: namespace, OriginalName: "tool_search"}}, nil
	case "file_search", "computer_use_preview", "mcp":
		return nil, map[string]responseToolMeta{}, nil
	default:
		return nil, nil, fmt.Errorf("unsupported responses tool type %q", typ)
	}
}

func windsurfToOpenAIMessages(messages []windsurf.ChatMessage) []ChatMessage {
	out := make([]ChatMessage, 0, len(messages))
	for _, msg := range messages {
		out = append(out, ChatMessage{
			Role:       msg.Role,
			Content:    msg.Content,
			ToolCallID: msg.ToolCallID,
			ToolCalls:  fromWindsurfToolCalls(msg.ToolCalls),
		})
	}
	return out
}

func fromWindsurfToolCalls(calls []windsurf.ToolCall) []ToolCall {
	out := make([]ToolCall, 0, len(calls))
	for _, call := range calls {
		out = append(out, ToolCall{ID: call.ID, Type: "function", Function: ToolCallFunction{Name: call.Name, Arguments: call.ArgumentsJSON}})
	}
	return out
}

func writeResponsesResponse(w http.ResponseWriter, modelID string, result *windsurf.ChatResult) {
	writeResponsesResponseWithMeta(w, modelID, result, nil)
}

func writeResponsesResponseWithMeta(w http.ResponseWriter, modelID string, result *windsurf.ChatResult, meta map[string]responseToolMeta) {
	result = sanitizeChatResult(result)
	resp := map[string]any{
		"id":                  "resp_" + strconv.FormatInt(time.Now().UnixNano(), 36),
		"object":              "response",
		"created_at":          time.Now().Unix(),
		"model":               modelID,
		"status":              responsesStatus(result),
		"parallel_tool_calls": true,
		"output":              responsesOutputWithMeta(result, meta),
		"usage":               usageMap(result.Usage),
	}
	setNoStore(w)
	w.Header().Set("Content-Type", "application/json")
	writeJSONNoEscape(w, resp)
}

func responsesOutput(result *windsurf.ChatResult) []any {
	return responsesOutputWithMeta(result, nil)
}

func responsesOutputWithMeta(result *windsurf.ChatResult, meta map[string]responseToolMeta) []any {
	result = sanitizeChatResult(result)
	var out []any
	if result != nil && strings.TrimSpace(result.Thinking) != "" {
		out = append(out, responsesReasoningItem(result.Thinking))
	}
	if len(result.ToolCalls) > 0 {
		for _, call := range result.ToolCalls {
			out = append(out, responseToolOutputItem(call, meta))
		}
		return out
	}
	return append(out, map[string]any{
		"type":    "message",
		"id":      "msg_" + strconv.FormatInt(time.Now().UnixNano(), 36),
		"role":    "assistant",
		"status":  "completed",
		"content": []any{map[string]any{"type": "output_text", "text": result.Text}},
	})
}

func responsesReasoningItem(text string) map[string]any {
	return responsesReasoningItemWithID("rs_"+strconv.FormatInt(time.Now().UnixNano(), 36), text, "completed")
}

func responsesReasoningItemWithID(id, text, status string) map[string]any {
	text = sanitize.Text(text)
	return map[string]any{
		"type":   "reasoning",
		"id":     id,
		"status": status,
		"summary": []any{map[string]any{
			"type": "summary_text",
			"text": text,
		}},
	}
}

func newResponsesStreamWriter(w http.ResponseWriter, modelID string, meta ...map[string]responseToolMeta) (*responsesStreamWriter, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, fmt.Errorf("response writer does not support flushing")
	}
	setNoStore(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	var toolMeta map[string]responseToolMeta
	if len(meta) > 0 {
		toolMeta = meta[0]
	}
	s := &responsesStreamWriter{
		w:                    w,
		flusher:              flusher,
		id:                   "resp_" + strconv.FormatInt(time.Now().UnixNano(), 36),
		model:                modelID,
		textOutputIndex:      -1,
		reasoningOutputIndex: -1,
		toolStarted:          map[int]bool{},
		toolPendingArgsIndex: map[int]int{},
		toolOutputIndex:      map[int]int{},
		toolMeta:             toolMeta,
	}
	_ = s.event("response.created", map[string]any{"type": "response.created", "response": map[string]any{"id": s.id, "object": "response", "model": modelID, "status": "in_progress"}})
	return s, nil
}

func (s *responsesStreamWriter) event(name string, payload any) error {
	if s.closed {
		return nil
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", name, raw); err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

func (s *responsesStreamWriter) TextDelta(delta string) error {
	delta = sanitize.Text(delta)
	if delta == "" {
		return nil
	}
	if !s.textStarted {
		s.textStarted = true
		s.textOutputIndex = s.nextOutputIndex
		s.nextOutputIndex++
		s.textItemID = "msg_" + strconv.FormatInt(time.Now().UnixNano(), 36)
		if err := s.event("response.output_item.added", map[string]any{"type": "response.output_item.added", "output_index": s.textOutputIndex, "item": map[string]any{"id": s.textItemID, "type": "message", "role": "assistant", "status": "in_progress", "content": []any{}}}); err != nil {
			return err
		}
		if err := s.event("response.content_part.added", map[string]any{"type": "response.content_part.added", "output_index": s.textOutputIndex, "content_index": 0, "part": map[string]any{"type": "output_text", "text": ""}}); err != nil {
			return err
		}
	}
	return s.event("response.output_text.delta", map[string]any{"type": "response.output_text.delta", "delta": delta})
}

func (s *responsesStreamWriter) ThinkingDelta(delta string) error {
	delta = sanitize.Text(delta)
	if delta == "" {
		return nil
	}
	if !s.reasoningStarted {
		s.reasoningStarted = true
		s.reasoningOutputIndex = s.nextOutputIndex
		s.nextOutputIndex++
		s.reasoningItemID = "rs_" + strconv.FormatInt(time.Now().UnixNano(), 36)
		if err := s.event("response.output_item.added", map[string]any{"type": "response.output_item.added", "output_index": s.reasoningOutputIndex, "item": responsesReasoningItemWithID(s.reasoningItemID, "", "in_progress")}); err != nil {
			return err
		}
	}
	s.reasoningText += delta
	return s.event("response.reasoning_summary_text.delta", map[string]any{"type": "response.reasoning_summary_text.delta", "item_id": s.reasoningItemID, "output_index": s.reasoningOutputIndex, "delta": delta})
}

func (s *responsesStreamWriter) finishReasoning() error {
	if !s.reasoningStarted || s.reasoningDone {
		return nil
	}
	s.reasoningDone = true
	if err := s.event("response.reasoning_summary_text.done", map[string]any{"type": "response.reasoning_summary_text.done", "item_id": s.reasoningItemID, "output_index": s.reasoningOutputIndex, "text": s.reasoningText}); err != nil {
		return err
	}
	return s.event("response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": s.reasoningOutputIndex, "item": responsesReasoningItemWithID(s.reasoningItemID, s.reasoningText, "completed")})
}

func (s *responsesStreamWriter) ToolCallDelta(index int, call windsurf.ToolCall) error {
	call = sanitize.ToolCall(call)
	if s.toolStarted == nil {
		s.toolStarted = map[int]bool{}
	}
	if call.ID == "" && call.Name == "" && !s.toolStarted[index] {
		outputIndex, ok := s.toolPendingArgsIndex[index]
		if !ok {
			outputIndex = s.nextOutputIndex
			s.nextOutputIndex++
			s.toolPendingArgsIndex[index] = outputIndex
		}
		return s.event("response.function_call_arguments.delta", map[string]any{"type": "response.function_call_arguments.delta", "output_index": outputIndex, "delta": call.ArgumentsJSON})
	}
	if !s.toolStarted[index] {
		s.toolStarted[index] = true
		if outputIndex, ok := s.toolPendingArgsIndex[index]; ok {
			s.toolOutputIndex[index] = outputIndex
		} else {
			s.toolOutputIndex[index] = s.nextOutputIndex
			s.nextOutputIndex++
		}
		item := responseToolOutputItem(windsurf.ToolCall{ID: call.ID, Name: call.Name}, s.toolMeta)
		if err := s.event("response.output_item.added", map[string]any{"type": "response.output_item.added", "output_index": s.toolOutputIndex[index], "item": item}); err != nil {
			return err
		}
	}
	outputIndex := s.toolOutputIndex[index]
	item := map[string]any{"type": "response.function_call_arguments.delta", "output_index": outputIndex, "delta": call.ArgumentsJSON}
	if call.ID != "" || call.Name != "" {
		item["item"] = responseToolOutputItem(windsurf.ToolCall{ID: call.ID, Name: call.Name}, s.toolMeta)
	}
	return s.event("response.function_call_arguments.delta", item)
}

func (s *responsesStreamWriter) Error(class account.ErrorClass, err error) error {
	message := "upstream stream error"
	if err != nil && err.Error() != "" {
		message = redact.Text(err.Error())
	}
	typ := string(class)
	if typ == "" {
		typ = "upstream_error"
	}
	if err := s.event("response.error", map[string]any{"type": "response.error", "error": map[string]any{"type": typ, "message": message, "code": typ}}); err != nil {
		return err
	}
	s.closed = true
	return nil
}

func (s *responsesStreamWriter) Finish(result *windsurf.ChatResult) error {
	result = sanitizeChatResult(result)
	if strings.TrimSpace(result.Thinking) != "" && !s.reasoningStarted {
		if err := s.ThinkingDelta(result.Thinking); err != nil {
			return err
		}
	}
	if err := s.finishReasoning(); err != nil {
		return err
	}
	if s.textStarted {
		if err := s.event("response.output_text.done", map[string]any{"type": "response.output_text.done", "text": result.Text}); err != nil {
			return err
		}
		if err := s.event("response.content_part.done", map[string]any{"type": "response.content_part.done", "output_index": s.textOutputIndex, "content_index": 0, "part": map[string]any{"type": "output_text", "text": result.Text}}); err != nil {
			return err
		}
		if err := s.event("response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": s.textOutputIndex, "item": map[string]any{"id": s.textItemID, "type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": result.Text}}}}); err != nil {
			return err
		}
	}
	for i, call := range result.ToolCalls {
		outputIndex, ok := s.toolOutputIndex[i]
		if !ok {
			outputIndex = s.nextOutputIndex
			s.nextOutputIndex++
		}
		if err := s.event("response.function_call_arguments.done", map[string]any{"type": "response.function_call_arguments.done", "output_index": outputIndex, "arguments": call.ArgumentsJSON}); err != nil {
			return err
		}
		item := responseToolOutputItem(call, s.toolMeta)
		if m, ok := item.(map[string]any); ok {
			m["status"] = "completed"
		}
		if err := s.event("response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": outputIndex, "item": item}); err != nil {
			return err
		}
	}
	if err := s.event("response.completed", map[string]any{"type": "response.completed", "response": map[string]any{"id": s.id, "object": "response", "model": s.model, "status": responsesStatus(result), "output": responsesOutputWithMeta(result, s.toolMeta), "usage": usageMap(result.Usage)}}); err != nil {
		return err
	}
	s.closed = true
	return nil
}

func responseToolOutputItem(call windsurf.ToolCall, meta map[string]responseToolMeta) any {
	call = sanitize.ToolCall(call)
	info, ok := meta[call.Name]
	if !ok {
		return map[string]any{"type": "function_call", "id": call.ID, "call_id": call.ID, "name": call.Name, "arguments": call.ArgumentsJSON}
	}
	name := firstNonEmpty(info.OriginalName, call.Name)
	switch info.Type {
	case "custom":
		return map[string]any{"type": "custom_tool_call", "id": call.ID, "call_id": call.ID, "name": name, "input": responseToolInputString(call.ArgumentsJSON)}
	case "web_search":
		return map[string]any{"type": "web_search_call", "id": call.ID, "call_id": call.ID, "action": map[string]any{"query": responseToolArgumentString(call.ArgumentsJSON, "query")}}
	case "namespace":
		return map[string]any{"type": "function_call", "id": call.ID, "call_id": call.ID, "name": name, "namespace": info.Namespace, "arguments": call.ArgumentsJSON}
	default:
		return map[string]any{"type": "function_call", "id": call.ID, "call_id": call.ID, "name": name, "arguments": call.ArgumentsJSON}
	}
}

func responsesStatus(result *windsurf.ChatResult) string {
	if result != nil && len(result.ToolCalls) > 0 {
		return "incomplete"
	}
	return "completed"
}

func responseToolInputString(arguments string) string {
	if input := responseToolArgumentString(arguments, "input"); input != "" {
		return input
	}
	return arguments
}

func responseToolArgumentString(arguments, key string) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(arguments), &m); err == nil {
		if s := stringValue(m[key]); s != "" {
			return s
		}
	}
	return ""
}

func normalizeResponsesToolChoice(choice any) any {
	m, ok := choice.(map[string]any)
	if !ok || m == nil {
		return choice
	}
	typ, _ := m["type"].(string)
	switch typ {
	case "web_search", "tool_search":
		return "auto"
	case "custom", "namespace":
		name := firstNonEmpty(stringValue(m["name"]), stringValue(objectValue(m["function"])["name"]))
		if name == "" {
			return choice
		}
		return map[string]any{"type": "function", "function": map[string]any{"name": encodeResponseToolName(name, firstNonEmpty(stringValue(m["namespace"]), stringValue(objectValue(m["function"])["namespace"])))}}
	case "function":
		if fn := objectValue(m["function"]); fn != nil {
			if name := stringValue(fn["name"]); name != "" {
				return map[string]any{"type": "function", "function": map[string]any{"name": encodeResponseToolName(name, firstNonEmpty(stringValue(fn["namespace"]), stringValue(m["namespace"])))}}
			}
		}
	}
	return choice
}

func encodeResponseToolName(name, namespace string) string {
	name = firstNonEmpty(strings.TrimSpace(name), "unknown")
	namespace = strings.TrimSpace(namespace)
	if namespace == "" {
		return name
	}
	if strings.HasSuffix(namespace, "__") {
		return namespace + name
	}
	return namespace + "__" + name
}

func stringifyMaybe(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return ""
	default:
		raw, err := json.Marshal(x)
		if err != nil {
			return fmt.Sprint(x)
		}
		return string(raw)
	}
}

func objectValue(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func stringValue(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

func firstArrayValue(m map[string]any, keys ...string) []any {
	for _, key := range keys {
		if arr, ok := m[key].([]any); ok {
			return arr
		}
	}
	return nil
}
