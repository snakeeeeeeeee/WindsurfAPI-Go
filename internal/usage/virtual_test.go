package usage

import "testing"

func TestVirtualCacheLedgerCreatesThenReads(t *testing.T) {
	mgr := NewManager(VirtualCacheConfig{
		Enabled:             true,
		Mode:                "conservative",
		DefaultTTL:          "5m",
		UncachedInputTokens: 10,
		MinInputTokens:      1,
		MaxInputTokens:      100,
		MaxCreationTokens:   1000,
	})
	first := mgr.Build(Input{AccountID: 1, Model: "claude-sonnet-4.6", CallerKeyHash: "caller", Route: "messages", ObservedInputTokens: 100, OutputTokens: 5})
	if !first.Virtual || first.InputTokens != 10 || first.CacheCreationInputTokens != 90 || first.CacheReadInputTokens != 0 {
		t.Fatalf("first=%+v", first)
	}
	second := mgr.Build(Input{AccountID: 1, Model: "claude-sonnet-4.6", CallerKeyHash: "caller", Route: "messages", ObservedInputTokens: 120, OutputTokens: 7})
	if second.CacheReadInputTokens != 90 || second.InputTokens != 10 || second.CacheCreationInputTokens != 20 {
		t.Fatalf("second=%+v", second)
	}
}

func TestVirtualCacheLedgerIsScoped(t *testing.T) {
	mgr := NewManager(VirtualCacheConfig{Enabled: true, UncachedInputTokens: 10, MaxCreationTokens: 1000})
	_ = mgr.Build(Input{AccountID: 1, Model: "m", CallerKeyHash: "a", Route: "messages", ObservedInputTokens: 100})
	got := mgr.Build(Input{AccountID: 2, Model: "m", CallerKeyHash: "a", Route: "messages", ObservedInputTokens: 100})
	if got.CacheReadInputTokens != 0 {
		t.Fatalf("different account should not read prior cache: %+v", got)
	}
	got = mgr.Build(Input{AccountID: 1, Model: "m", CallerKeyHash: "b", Route: "messages", ObservedInputTokens: 100})
	if got.CacheReadInputTokens != 0 {
		t.Fatalf("different caller should not read prior cache: %+v", got)
	}
}

func TestDisabledVirtualCacheReturnsBaseUsage(t *testing.T) {
	mgr := NewManager(VirtualCacheConfig{Enabled: false})
	got := mgr.Build(Input{ObservedInputTokens: 0, EstimatedInputTokens: 8, OutputTokens: 3})
	if got.Virtual || got.InputTokens != 8 || got.OutputTokens != 3 || got.CacheCreationInputTokens != 0 {
		t.Fatalf("got=%+v", got)
	}
}
