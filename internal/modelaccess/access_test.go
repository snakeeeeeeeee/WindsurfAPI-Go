package modelaccess

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhangyu/windsurfapi-go/internal/store"
)

func TestManagerDefaultsAllowKnownModels(t *testing.T) {
	mgr := testManager(t)
	access, err := mgr.Get("claude-sonnet-4-6")
	if err != nil {
		t.Fatal(err)
	}
	if access.ModelID != "claude-sonnet-4.6" || !access.Visible || !access.Enabled {
		t.Fatalf("access=%+v", access)
	}
}

func TestManagerConfigPersistsModeAndList(t *testing.T) {
	mgr := testManager(t)
	if got := mgr.Config(); got.Mode != "all" || len(got.List) != 0 {
		t.Fatalf("default config=%+v", got)
	}
	if err := mgr.SetConfig(Config{Mode: "allow", List: []string{"claude-sonnet-4-6", "claude-sonnet-4.6"}}); err != nil {
		t.Fatal(err)
	}
	got := mgr.Config()
	if got.Mode != "allowlist" || len(got.List) != 1 || got.List[0] != "claude-sonnet-4.6" {
		t.Fatalf("config=%+v", got)
	}
	if err := mgr.SetConfig(Config{Mode: "blocklist", List: []string{"definitely-not-a-model"}}); err == nil {
		t.Fatal("expected unknown model error")
	}
}

func TestManagerUpsertAndReset(t *testing.T) {
	mgr := testManager(t)
	visible, enabled, deprecated := false, false, true
	access, err := mgr.Upsert("claude-sonnet-4.6", Patch{
		Visible:           &visible,
		Enabled:           &enabled,
		Deprecated:        &deprecated,
		UnsupportedReason: "quota disabled",
		Notes:             "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if access.Visible || access.Enabled || !access.Deprecated || access.UnsupportedReason != "quota disabled" {
		t.Fatalf("access=%+v", access)
	}
	if ok, reason := mgr.IsEnabled("claude-sonnet-4.6"); ok || reason != "quota disabled" {
		t.Fatalf("enabled=%v reason=%q", ok, reason)
	}
	if mgr.IsVisible("claude-sonnet-4.6") {
		t.Fatal("model should be hidden")
	}
	if err := mgr.Reset("claude-sonnet-4.6"); err != nil {
		t.Fatal(err)
	}
	if ok, reason := mgr.IsEnabled("claude-sonnet-4.6"); !ok || reason != "" {
		t.Fatalf("after reset enabled=%v reason=%q", ok, reason)
	}
}

func TestManagerHiddenModelIsNotEnabled(t *testing.T) {
	mgr := testManager(t)
	visible := false
	if _, err := mgr.Upsert("claude-sonnet-4.6", Patch{Visible: &visible}); err != nil {
		t.Fatal(err)
	}
	if ok, reason := mgr.IsEnabled("claude-sonnet-4.6"); ok || !strings.Contains(reason, "hidden") {
		t.Fatalf("hidden model enabled=%v reason=%q", ok, reason)
	}
}

func TestManagerRejectsUnknownModel(t *testing.T) {
	mgr := testManager(t)
	enabled := false
	if _, err := mgr.Upsert("definitely-not-a-model", Patch{Enabled: &enabled}); err == nil {
		t.Fatal("expected unknown model error")
	}
}

func TestManagerDefaultsDisableUnsupportedCatalogModel(t *testing.T) {
	mgr := testManager(t)
	access, err := mgr.Get("gpt-5")
	if err != nil {
		t.Fatal(err)
	}
	if access.Enabled || access.UnsupportedReason == "" {
		t.Fatalf("gpt-5 access=%+v, want disabled with reason", access)
	}
}

func testManager(t *testing.T) *Manager {
	t.Helper()
	sqliteStore, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "windsurf.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqliteStore.Close() })
	return NewManager(sqliteStore)
}
