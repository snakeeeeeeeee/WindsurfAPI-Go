package reuse

import (
	"testing"
	"time"

	"github.com/zhangyu/windsurfapi-go/internal/windsurf"
)

func TestCheckoutCheckinAndInvalidateLS(t *testing.T) {
	p := NewPool()
	msgs := []windsurf.ChatMessage{{Role: "user", Content: "hi"}}
	fp := Fingerprint("m", "caller", msgs)
	p.Checkin(fp, &Entry{AccountID: 1, LSPort: 42100, LSGeneration: "g1", CascadeID: "c1", ModelID: "m", CallerKey: "caller"}, time.Minute)

	e, ok := p.Checkout(fp, "caller", "m")
	if !ok || e.CascadeID != "c1" {
		t.Fatalf("checkout failed: %+v ok=%v", e, ok)
	}
	if _, ok := p.Checkout(fp, "caller", "m"); ok {
		t.Fatal("entry should be checked out and unavailable")
	}

	p.Checkin(fp, e, time.Minute)
	p.InvalidateLS(42100, "g1")
	if _, ok := p.Checkout(fp, "caller", "m"); ok {
		t.Fatal("entry should be invalidated by LS generation")
	}
}

func TestClearRemovesEntries(t *testing.T) {
	p := NewPool()
	fp := Fingerprint("m", "caller", []windsurf.ChatMessage{{Role: "user", Content: "hi"}})
	p.Checkin(fp, &Entry{AccountID: 1, ModelID: "m", CallerKey: "caller"}, time.Minute)
	if got := len(p.Snapshot()); got != 1 {
		t.Fatalf("entries before clear=%d", got)
	}
	if cleared := p.Clear(); cleared != 1 {
		t.Fatalf("cleared=%d", cleared)
	}
	if got := len(p.Snapshot()); got != 0 {
		t.Fatalf("entries after clear=%d", got)
	}
	if stats := p.Stats(); stats.Evictions != 1 {
		t.Fatalf("stats=%+v", stats)
	}
}

func TestFingerprintNormalizesDynamicContent(t *testing.T) {
	a := Fingerprint("m", "caller", []windsurf.ChatMessage{{Role: "user", Content: "Today 2026-05-10\nWorking directory: /tmp/a\nhi"}})
	b := Fingerprint("m", "caller", []windsurf.ChatMessage{{Role: "user", Content: "Today 2026-05-11\nWorking directory: /tmp/b\nhi"}})
	if a != b {
		t.Fatalf("dynamic content should normalize: %s != %s", a, b)
	}
}

func TestFingerprintWithOptionsSeparatesRoutesAndTools(t *testing.T) {
	msgs := []windsurf.ChatMessage{{Role: "user", Content: "hi"}}
	chat := FingerprintWithOptions("m", "caller", "chat", "toolsA", "auto", msgs)
	messages := FingerprintWithOptions("m", "caller", "messages", "toolsA", "auto", msgs)
	otherTools := FingerprintWithOptions("m", "caller", "chat", "toolsB", "auto", msgs)
	if chat == messages {
		t.Fatal("route should affect fingerprint")
	}
	if chat == otherTools {
		t.Fatal("tools digest should affect fingerprint")
	}
}

func TestFingerprintBeforeAfterToolContinuationMatch(t *testing.T) {
	history := []windsurf.ChatMessage{{Role: "user", Content: "call two tools"}}
	result := &windsurf.ChatResult{FinishReason: "tool_calls", ToolCalls: []windsurf.ToolCall{
		{ID: "toolu_1", Name: "read_file", ArgumentsJSON: `{"path":"a.txt"}`},
		{ID: "toolu_2", Name: "read_file", ArgumentsJSON: `{"path":"b.txt"}`},
	}}
	after := FingerprintAfterWithOptions("m", "caller", "chat", "toolsA", "auto", history, result)
	next := append(append([]windsurf.ChatMessage(nil), history...),
		windsurf.ChatMessage{Role: "assistant", Content: "I will inspect the files", ToolCalls: result.ToolCalls},
		windsurf.ChatMessage{Role: "tool", ToolCallID: "toolu_1", Content: "a"},
		windsurf.ChatMessage{Role: "tool", ToolCallID: "toolu_2", Content: "b"},
	)
	before := FingerprintBeforeWithOptions("m", "caller", "chat", "toolsA", "auto", next)
	if after == "" || before == "" || after != before {
		t.Fatalf("tool continuation fingerprints should match after=%q before=%q", after, before)
	}

	replayed := append(append([]windsurf.ChatMessage(nil), history...),
		windsurf.ChatMessage{Role: "assistant", Content: "", ToolCalls: []windsurf.ToolCall{
			{ID: "toolu_1", Name: "read_file", ArgumentsJSON: "{\n  \"path\": \"a.txt\"\n}"},
			{ID: "toolu_2", Name: "read_file", ArgumentsJSON: `{"path":"b.txt"}`},
		}},
		windsurf.ChatMessage{Role: "tool", ToolCallID: "toolu_1", Content: "a"},
	)
	if got := FingerprintBeforeWithOptions("m", "caller", "chat", "toolsA", "auto", replayed); got != after {
		t.Fatalf("assistant narration/json formatting should not drift fingerprint got=%q want=%q", got, after)
	}
}

func TestFingerprintBeforeDropsLatestUserTurn(t *testing.T) {
	history := []windsurf.ChatMessage{
		{Role: "system", Content: "be brief"},
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello"},
	}
	after := FingerprintWithOptions("m", "caller", "messages", "", "", history)
	before := FingerprintBeforeWithOptions("m", "caller", "messages", "", "", append(history, windsurf.ChatMessage{Role: "user", Content: "next"}))
	if before != after {
		t.Fatalf("latest user turn should be excluded before=%q after=%q", before, after)
	}
}

func TestFingerprintToolContinuationDoesNotCrossRoutes(t *testing.T) {
	history := []windsurf.ChatMessage{{Role: "user", Content: "call tool"}}
	result := &windsurf.ChatResult{FinishReason: "tool_calls", ToolCalls: []windsurf.ToolCall{{
		ID:            "toolu_1",
		Name:          "read_file",
		ArgumentsJSON: `{"path":"a.txt"}`,
	}}}
	chatAfter := FingerprintAfterWithOptions("m", "caller", "chat_completions", "tools", "", history, result)
	messagesAfter := FingerprintAfterWithOptions("m", "caller", "messages", "tools", "", history, result)
	responsesAfter := FingerprintAfterWithOptions("m", "caller", "responses", "tools", "", history, result)
	if chatAfter == messagesAfter || chatAfter == responsesAfter || messagesAfter == responsesAfter {
		t.Fatalf("route must isolate reuse keys chat=%q messages=%q responses=%q", chatAfter, messagesAfter, responsesAfter)
	}
}
