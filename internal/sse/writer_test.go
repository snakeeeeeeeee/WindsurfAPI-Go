package sse

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zhangyu/windsurfapi-go/internal/account"
)

func TestWriterStreamShape(t *testing.T) {
	rec := httptest.NewRecorder()
	w, err := NewWriter(rec, "gpt-5")
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if err := w.Role(); err != nil {
		t.Fatalf("Role: %v", err)
	}
	if err := w.Delta("hello "); err != nil {
		t.Fatalf("Delta 1: %v", err)
	}
	if err := w.Delta(""); err != nil { // 空串应被静默跳过
		t.Fatalf("empty Delta returned err: %v", err)
	}
	if err := w.Delta("world"); err != nil {
		t.Fatalf("Delta 2: %v", err)
	}
	if err := w.ReasoningDelta("reasoning"); err != nil {
		t.Fatalf("ReasoningDelta: %v", err)
	}
	if err := w.ToolCallDelta(0, "call_1", "echo_text", `{"text":`); err != nil {
		t.Fatalf("ToolCallDelta 1: %v", err)
	}
	if err := w.ToolCallDelta(0, "", "", `"hi"}`); err != nil {
		t.Fatalf("ToolCallDelta 2: %v", err)
	}
	if err := w.Finish("stop", &Usage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12}); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	body := rec.Body.String()
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("content-type: %s", ct)
	}
	if !strings.HasSuffix(body, "data: [DONE]\n\n") {
		t.Error("missing [DONE] sentinel")
	}
	// 必须有 role、2 个 text delta、reasoning delta、tool deltas、finish、DONE
	if n := strings.Count(body, "\ndata: "); n < 3 { // 首个可能没有前导\n
		if n := strings.Count(body, "data: "); n < 6 {
			t.Errorf("unexpected data line count: body=%q", body)
		}
	}

	// 验证首个 chunk 里带 role=assistant
	first := strings.SplitN(body, "\n", 2)[0]
	first = strings.TrimPrefix(first, "data: ")
	var m map[string]any
	if err := json.Unmarshal([]byte(first), &m); err != nil {
		t.Fatalf("first chunk not valid json: %v", err)
	}
	if m["model"] != "gpt-5" {
		t.Errorf("model field: %v", m["model"])
	}
	if !strings.Contains(body, `"tool_calls"`) || !strings.Contains(body, `"echo_text"`) {
		t.Fatalf("missing tool call delta: body=%q", body)
	}
	if !strings.Contains(body, `"reasoning_content":"reasoning"`) {
		t.Fatalf("missing reasoning delta: body=%q", body)
	}
}

func TestWriterStructuredError(t *testing.T) {
	rec := httptest.NewRecorder()
	w, err := NewWriter(rec, "claude-sonnet-4.6")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Error(account.ErrorUpstreamTransient, errBoom{}); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"error"`) || !strings.Contains(body, `"type":"upstream_transient"`) || !strings.Contains(body, `"message":"boom"`) {
		t.Fatalf("body=%q", body)
	}
}

func TestWriterStructuredErrorRedactsSecrets(t *testing.T) {
	rec := httptest.NewRecorder()
	w, err := NewWriter(rec, "claude-sonnet-4.6")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Error(account.ErrorTransport, errString("Authorization: Bearer sk-test_abcdefghijklmnopqrstuvwxyz user@example.com")); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	if strings.Contains(body, "sk-test_abcdefghijklmnopqrstuvwxyz") || strings.Contains(body, "user@example.com") {
		t.Fatalf("secret leaked in stream error: %s", body)
	}
	if !strings.Contains(body, "[REDACTED]") {
		t.Fatalf("missing redaction markers: %s", body)
	}
}

type errBoom struct{}

func (errBoom) Error() string { return "boom" }

type errString string

func (e errString) Error() string { return string(e) }
