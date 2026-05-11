package sanitize

import (
	"strings"
	"testing"

	"github.com/zhangyu/windsurfapi-go/internal/windsurf"
)

func TestTextRedactsWindsurfWorkspacePaths(t *testing.T) {
	cases := map[string]string{
		"/tmp/windsurf-workspace/src/index.js":                          WorkspaceMarker,
		"/tmp/windsurf-workspace":                                       WorkspaceMarker,
		"/home/user/projects/workspace-abc12345/package.json":           WorkspaceMarker,
		`C:\home\user\projects\workspace-devinxse\src\index.js`:         WorkspaceMarker,
		`\home\user\projects\workspace-skxwsx01\src\index.js`:           WorkspaceMarker,
		`C:\home/user/projects/workspace-devinxse/src/index.js`:         WorkspaceMarker,
		`d:\home\user\projects\workspace-x12345\file.txt`:               WorkspaceMarker,
		"/opt/windsurf/language_server":                                 WorkspaceMarker,
		"normal /Users/zhangyu/code/myProject/WindsurfAPI-Go path text": "normal /Users/zhangyu/code/myProject/WindsurfAPI-Go path text",
	}
	for input, want := range cases {
		if got := Text(input); got != want {
			t.Fatalf("Text(%q)=%q want %q", input, got, want)
		}
	}
}

func TestTextStripsWorkspaceMetadataBlocks(t *testing.T) {
	got := Text("a<workspace_information>secret /home/user/projects/workspace-x/a</workspace_information>b<workspace_layout>tree</workspace_layout>c<user_information>user</user_information>d")
	if got != "abcd" {
		t.Fatalf("block sanitize=%q", got)
	}
}

func TestToolCallSanitizesArgumentsJSON(t *testing.T) {
	call := ToolCall(windsurf.ToolCall{
		ID:            "call_1",
		Name:          "Read",
		ArgumentsJSON: `{"path":"/tmp/windsurf-workspace/f.js","nested":{"cwd":"C:\\home\\user\\projects\\workspace-devinxse\\src"}}`,
	})
	if strings.Contains(call.ArgumentsJSON, "windsurf-workspace") || strings.Contains(call.ArgumentsJSON, "workspace-devinxse") {
		t.Fatalf("path leaked in arguments: %s", call.ArgumentsJSON)
	}
	if !strings.Contains(call.ArgumentsJSON, WorkspaceMarker) {
		t.Fatalf("missing marker: %s", call.ArgumentsJSON)
	}
}

func TestMessagesSanitizesContentAndToolCalls(t *testing.T) {
	msgs := Messages([]windsurf.ChatMessage{{
		Role:    "assistant",
		Content: "reading /home/user/projects/workspace-abc12345/a.txt",
		ToolCalls: []windsurf.ToolCall{{
			ID:            "call_1",
			Name:          "Read",
			ArgumentsJSON: `{"file_path":"/home/user/projects/workspace-abc12345/a.txt"}`,
		}},
	}})
	if strings.Contains(msgs[0].Content, "workspace-abc12345") || strings.Contains(msgs[0].ToolCalls[0].ArgumentsJSON, "workspace-abc12345") {
		t.Fatalf("workspace path leaked: %+v", msgs[0])
	}
}
