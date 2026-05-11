package models

import "testing"

func TestGetModelByIDClaudeAliases(t *testing.T) {
	tests := []struct {
		input string
		want  string
		uid   string
	}{
		{"claude-4.5-haiku", "claude-4.5-haiku", "MODEL_PRIVATE_11"},
		{"claude-haiku-4.5", "claude-4.5-haiku", "MODEL_PRIVATE_11"},
		{"claude-sonnet-4.5", "claude-4.5-sonnet", "MODEL_PRIVATE_2"},
		{"claude-opus-4.7", "claude-opus-4-7-medium", "claude-opus-4-7-medium"},
		{"claude-opus-4.7-high", "claude-opus-4-7-high", "claude-opus-4-7-high"},
		{"claude-4.6", "claude-sonnet-4.6", "claude-sonnet-4-6"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := GetModelByID(tt.input)
			if got == nil {
				t.Fatalf("GetModelByID(%q) returned nil", tt.input)
			}
			if got.ID != tt.want {
				t.Fatalf("ID = %q, want %q", got.ID, tt.want)
			}
			if got.ModelUID != tt.uid {
				t.Fatalf("ModelUID = %q, want %q", got.ModelUID, tt.uid)
			}
		})
	}
}

func TestCatalogContainsNodeModelsButOpenAIListIsDirectOnly(t *testing.T) {
	for _, m := range AllModels() {
		if m.ModelUID == "" && m.ModelEnum == 0 {
			t.Fatalf("model %q has neither ModelUID nor ModelEnum", m.ID)
		}
	}

	if got := GetModelByID("gpt-5"); got == nil || got.DirectSupported {
		t.Fatalf("gpt-5 should resolve as cataloged but unsupported: %#v", got)
	}

	list := ToOpenAIModelList()
	if len(list.Data) != len(SupportedModels) {
		t.Fatalf("direct model list length = %d, want %d", len(list.Data), len(SupportedModels))
	}
	for _, m := range list.Data {
		if m.ID == "gpt-5" || m.OwnedBy != "windsurf" {
			t.Fatalf("unexpected model in OpenAI list: %#v", m)
		}
	}

	dashboard := ToDashboardModelList()
	if len(dashboard) <= len(SupportedModels) {
		t.Fatalf("dashboard catalog did not include Node models: got %d", len(dashboard))
	}
	var foundGPT bool
	for _, m := range dashboard {
		if m.ID == "gpt-5" {
			foundGPT = true
			if m.Credit == 0 || m.DirectSupported || m.Supported {
				t.Fatalf("gpt-5 dashboard metadata wrong: %#v", m)
			}
		}
	}
	if !foundGPT {
		t.Fatal("gpt-5 missing from dashboard catalog")
	}
}

func TestTierAccessSnapshotMatchesNodeCompatibilityShape(t *testing.T) {
	snap := TierAccessSnapshot()
	if len(snap.Pro) != len(AllModels()) || len(snap.Unknown) != len(snap.Pro) || len(snap.AllModels) != len(snap.Pro) {
		t.Fatalf("tier lengths wrong: %+v", snap)
	}
	if len(snap.Free) != 1 || snap.Free[0] != "gemini-2.5-flash" {
		t.Fatalf("free tier=%v", snap.Free)
	}
	if len(snap.Expired) != 0 {
		t.Fatalf("expired tier should be empty: %v", snap.Expired)
	}
	hasSonnet, hasGPT := false, false
	for _, id := range snap.DirectSupported {
		if id == "claude-sonnet-4.6" {
			hasSonnet = true
		}
	}
	for _, id := range snap.Unsupported {
		if id == "gpt-5" {
			hasGPT = true
		}
	}
	if !hasSonnet || !hasGPT {
		t.Fatalf("direct/unsupported split wrong direct=%v unsupported=%v", snap.DirectSupported, snap.Unsupported)
	}
}
