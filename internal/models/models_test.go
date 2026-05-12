package models

import (
	"strings"
	"testing"
)

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
		{"opus-4.6", "claude-opus-4.6", "claude-opus-4-6"},
		{"opus-4.7-thinking", "claude-opus-4-7-medium-thinking", "claude-opus-4-7-medium-thinking"},
		{"claude-opus-4.7-low", "claude-opus-4-7-low", "claude-opus-4-7-low"},
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

func TestModelFallbackAndPublicAliasCompatibility(t *testing.T) {
	if got := PickRateLimitFallback("claude-opus-4.7-max"); got != "claude-opus-4-7-xhigh" {
		t.Fatalf("fallback max=%q", got)
	}
	if got := PickRateLimitFallback("claude-opus-4.7-high"); got != "claude-opus-4-7-medium" {
		t.Fatalf("fallback high=%q", got)
	}
	if got := PickRateLimitFallback("claude-sonnet-4.6-1m"); got != "claude-sonnet-4.6" {
		t.Fatalf("fallback 1m=%q", got)
	}
	if got := PickRateLimitFallback("claude-opus-4.6-thinking"); got != "" {
		t.Fatalf("thinking fallback should be empty, got %q", got)
	}
}

func TestResolveModelForRequestPublicNamesAndEffort(t *testing.T) {
	tests := []struct {
		name   string
		model  string
		effort string
		want   string
		uid    string
	}{
		{"opus47Default", "claude-opus-4-7", "", "claude-opus-4-7-medium", "claude-opus-4-7-medium"},
		{"opus47Low", "claude-opus-4-7", "low", "claude-opus-4-7-low", "claude-opus-4-7-low"},
		{"opus47XHigh", "claude-opus-4-7", "xhigh", "claude-opus-4-7-xhigh", "claude-opus-4-7-xhigh"},
		{"opus47Max", "claude-opus-4-7", "max", "claude-opus-4-7-max", "claude-opus-4-7-max"},
		{"opus47ThinkingDefault", "claude-opus-4-7-thinking", "", "claude-opus-4-7-medium-thinking", "claude-opus-4-7-medium-thinking"},
		{"opus47ThinkingHigh", "claude-opus-4-7-thinking", "high", "claude-opus-4-7-high-thinking", "claude-opus-4-7-high-thinking"},
		{"opus47ThinkingMaxFallsToXHigh", "claude-opus-4-7-thinking", "max", "claude-opus-4-7-xhigh-thinking", "claude-opus-4-7-xhigh-thinking"},
		{"opus46", "claude-opus-4-6", "", "claude-opus-4.6", "claude-opus-4-6"},
		{"opus46Thinking", "claude-opus-4-6-thinking", "", "claude-opus-4.6-thinking", "claude-opus-4-6-thinking"},
		{"opus46UnsupportedEffortIgnored", "claude-opus-4-6", "high", "claude-opus-4.6", "claude-opus-4-6"},
		{"sonnet46", "claude-sonnet-4-6", "", "claude-sonnet-4.6", "claude-sonnet-4-6"},
		{"sonnet46UnsupportedEffortIgnored", "claude-sonnet-4-6", "xhigh", "claude-sonnet-4.6", "claude-sonnet-4-6"},
		{"haiku45", "claude-haiku-4-5", "", "claude-4.5-haiku", "MODEL_PRIVATE_11"},
		{"haiku45Dated", "claude-haiku-4-5-20251001", "", "claude-4.5-haiku", "MODEL_PRIVATE_11"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveModelForRequest(tt.model, tt.effort)
			if got == nil {
				t.Fatalf("ResolveModelForRequest(%q, %q) returned nil", tt.model, tt.effort)
			}
			if got.ID != tt.want || got.ModelUID != tt.uid {
				t.Fatalf("resolved ID=%q uid=%q, want ID=%q uid=%q", got.ID, got.ModelUID, tt.want, tt.uid)
			}
		})
	}
}

func TestResolveModelForRequestDefaultEffortOption(t *testing.T) {
	got := ResolveModelForRequestWithOptions("claude-opus-4-7", "", ResolverOptions{DefaultEffort: "high"})
	if got == nil || got.ID != "claude-opus-4-7-high" {
		t.Fatalf("default high resolved to %#v", got)
	}
	got = ResolveModelForRequestWithOptions("claude-opus-4.7", "", ResolverOptions{DefaultEffort: "low"})
	if got == nil || got.ID != "claude-opus-4-7-low" {
		t.Fatalf("default low resolved to %#v", got)
	}
	got = ResolveModelForRequestWithOptions("claude-opus-4.7-medium", "", ResolverOptions{DefaultEffort: "high"})
	if got == nil || got.ID != "claude-opus-4-7-medium" {
		t.Fatalf("explicit medium should not be overridden: %#v", got)
	}
	got = ResolveModelForRequestWithOptions("claude-opus-4-7", "xhigh", ResolverOptions{DefaultEffort: "high"})
	if got == nil || got.ID != "claude-opus-4-7-xhigh" {
		t.Fatalf("explicit effort should win: %#v", got)
	}
	got = ResolveModelForRequestWithOptions("claude-opus-4-7-thinking", "", ResolverOptions{DefaultEffort: "high"})
	if got == nil || got.ID != "claude-opus-4-7-high-thinking" {
		t.Fatalf("thinking default high resolved to %#v", got)
	}
	got = ResolveModelForRequestWithOptions("gpt-5.5", "", ResolverOptions{DefaultEffort: "xhigh"})
	if got == nil || got.ID != "gpt-5.5-xhigh" {
		t.Fatalf("generic default should route gpt level family: %#v", got)
	}
	got = ResolveModelForRequestWithOptions("gpt-5.5-low", "", ResolverOptions{DefaultEffort: "xhigh"})
	if got == nil || got.ID != "gpt-5.5-low" {
		t.Fatalf("explicit low should not be overridden: %#v", got)
	}
	got = ResolveModelForRequestWithOptions("claude-sonnet-4-6", "", ResolverOptions{DefaultEffort: "high"})
	if got == nil || got.ID != "claude-sonnet-4.6" {
		t.Fatalf("non-level model should not be overridden: %#v", got)
	}
	got = ResolveModelForRequestWithOptions("claude-opus-4-7", "", ResolverOptions{DefaultOpus47Effort: "high"})
	if got == nil || got.ID != "claude-opus-4-7-high" {
		t.Fatalf("deprecated opus default should still work: %#v", got)
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
	if len(list.Data) != len(PublicModelIDs) {
		t.Fatalf("public model list length = %d, want %d", len(list.Data), len(PublicModelIDs))
	}
	wantPublic := map[string]bool{}
	for _, id := range PublicModelIDs {
		wantPublic[id] = true
	}
	for _, m := range list.Data {
		if !wantPublic[m.ID] || strings.Contains(m.ID, "-medium") || strings.Contains(m.ID, "-xhigh") || strings.Contains(m.ID, "-max") {
			t.Fatalf("unexpected model in OpenAI list: %#v", m)
		}
	}

	dashboard := ToDashboardModelList()
	if len(dashboard) <= len(SupportedModels) {
		t.Fatalf("dashboard catalog did not include Node models: got %d", len(dashboard))
	}
	publicDashboard := ToPublicDashboardModelList()
	if len(publicDashboard) != len(PublicModelIDs) {
		t.Fatalf("public dashboard list length=%d want=%d", len(publicDashboard), len(PublicModelIDs))
	}
	for _, m := range publicDashboard {
		if !wantPublic[m.ID] {
			t.Fatalf("unexpected public dashboard model=%#v", m)
		}
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
