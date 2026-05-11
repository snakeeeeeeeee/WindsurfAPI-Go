package health

import "testing"

func TestTierFromPlanMatchesNodeRules(t *testing.T) {
	tests := map[string]string{
		"Pro":          "pro",
		"Teams":        "pro",
		"Enterprise":   "pro",
		"Trial":        "pro",
		"INDIVIDUAL":   "pro",
		"Premium Plan": "pro",
		"paid":         "pro",
		"Free":         "free",
		"Unknown":      "unknown",
		"":             "unknown",
	}
	for plan, want := range tests {
		if got := TierFromPlan(plan); got != want {
			t.Fatalf("TierFromPlan(%q)=%q want %q", plan, got, want)
		}
	}
}
