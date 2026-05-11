package sanitize

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/zhangyu/windsurfapi-go/internal/windsurf"
)

const WorkspaceMarker = "<workspace>"

var pathPatterns = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`/tmp/windsurf-workspace(?:[/\\][^\s"'` + "`" + `<>)}\],*;]*)?`), WorkspaceMarker},
	{regexp.MustCompile(`(?:[A-Za-z]:)?[/\\]home[/\\]user[/\\]projects[/\\]workspace-[A-Za-z0-9]+(?:[/\\][^\s"'` + "`" + `<>)}\],*;]*)?`), WorkspaceMarker},
	{regexp.MustCompile(`/opt/windsurf(?:[/\\][^\s"'` + "`" + `<>)}\],*;]*)?`), WorkspaceMarker},
	{regexp.MustCompile(`(?is)<workspace_information>.*?</workspace_information>`), ""},
	{regexp.MustCompile(`(?is)<workspace_layout>.*?</workspace_layout>`), ""},
	{regexp.MustCompile(`(?is)<user_information>.*?</user_information>`), ""},
}

// Text strips server-internal Windsurf workspace paths from model-facing and
// client-facing text. It intentionally does not redact arbitrary user project
// paths such as /Users/name/project.
func Text(s string) string {
	if s == "" {
		return ""
	}
	out := s
	for _, item := range pathPatterns {
		out = item.re.ReplaceAllString(out, item.repl)
	}
	return out
}

func Message(m windsurf.ChatMessage) windsurf.ChatMessage {
	m.Content = Text(m.Content)
	for i := range m.ToolCalls {
		m.ToolCalls[i] = ToolCall(m.ToolCalls[i])
	}
	return m
}

func Messages(messages []windsurf.ChatMessage) []windsurf.ChatMessage {
	out := make([]windsurf.ChatMessage, 0, len(messages))
	for _, msg := range messages {
		out = append(out, Message(msg))
	}
	return out
}

func ToolCall(call windsurf.ToolCall) windsurf.ToolCall {
	call.ID = Text(call.ID)
	call.Name = Text(call.Name)
	call.ArgumentsJSON = sanitizeToolArguments(call.ArgumentsJSON)
	return call
}

func sanitizeToolArguments(raw string) string {
	raw = Text(raw)
	if strings.TrimSpace(raw) == "" {
		return raw
	}
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return raw
	}
	v = sanitizeJSONValue(v)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return raw
	}
	return strings.TrimSpace(buf.String())
}

func sanitizeJSONValue(v any) any {
	switch x := v.(type) {
	case string:
		return Text(x)
	case []any:
		for i := range x {
			x[i] = sanitizeJSONValue(x[i])
		}
		return x
	case map[string]any:
		for k, v := range x {
			x[k] = sanitizeJSONValue(v)
		}
		return x
	default:
		return v
	}
}
