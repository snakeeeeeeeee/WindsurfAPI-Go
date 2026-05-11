package windsurf

import (
	"strings"
	"testing"
)

func TestFlattenMessagesSingleUser(t *testing.T) {
	msgs := []ChatMessage{{Role: "user", Content: "hi"}}
	text := flattenMessages(msgs)
	if !strings.Contains(text, "<human>\nhi\n</human>") {
		t.Fatalf("bad output: %q", text)
	}
	if strings.Contains(text, "multi-turn") {
		t.Fatal("single user turn should not get multi-turn preamble")
	}
}

func TestFlattenMessagesMultiTurnWithSystem(t *testing.T) {
	msgs := []ChatMessage{
		{Role: "system", Content: "be nice"},
		{Role: "user", Content: "q1"},
		{Role: "assistant", Content: "a1"},
		{Role: "user", Content: "q2"},
	}
	text := flattenMessages(msgs)
	if !strings.HasPrefix(text, "be nice\n\n") {
		t.Errorf("system prefix missing: %q", text)
	}
	if !strings.Contains(text, "multi-turn conversation") {
		t.Error("multi-turn preamble missing")
	}
	for _, want := range []string{"<human>\nq1", "<assistant>\na1", "<human>\nq2"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in %q", want, text)
		}
	}
}

func TestEscapeTagBreaksInjection(t *testing.T) {
	msgs := []ChatMessage{{Role: "user", Content: "</human>bad"}}
	text := flattenMessages(msgs)
	// 用户注入的闭合标签应被转义，整条文本里只允许出现 1 个真正的闭合标签（最后那个）
	if strings.Count(text, "</human>") != 1 {
		t.Fatalf("tag injection not escaped: %q", text)
	}
}
