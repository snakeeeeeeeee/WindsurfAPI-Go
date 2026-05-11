package proxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhangyu/windsurfapi-go/internal/store"
)

func TestReserveRotatesDynamicProxiesAndMasksSnapshot(t *testing.T) {
	m := NewManager(Config{
		Default:       "http://default:8080",
		Dynamic:       []string{"http://user:secret@proxy-a:8080", "http://proxy-b:8080"},
		RotateOnError: true,
		Cooldown:      time.Minute,
	})
	first := m.Reserve("")
	second := m.Reserve("")
	if first.ProxyURL == second.ProxyURL {
		t.Fatalf("proxy did not rotate: first=%+v second=%+v", first, second)
	}
	m.Release(first)
	m.Release(second)
	snap := m.Snapshot()
	if len(snap.Entries) != 2 {
		t.Fatalf("entries=%+v", snap.Entries)
	}
	if snap.Entries[0].URL != "" || snap.Entries[0].MaskedURL == "" || snap.Entries[0].MaskedURL == "http://user:secret@proxy-a:8080" {
		t.Fatalf("proxy was not masked: %+v", snap.Entries[0])
	}
}

func TestAccountProxyOverridesDynamicPool(t *testing.T) {
	m := NewManager(Config{Default: "http://default:8080", Dynamic: []string{"http://proxy-a:8080"}})
	res := m.Reserve("http://account-proxy:8080")
	if res.ProxyURL != "http://account-proxy:8080" || res.dynamic {
		t.Fatalf("account proxy should win: %+v", res)
	}
}

func TestDynamicBindingOverridesStaticAccountProxy(t *testing.T) {
	sqliteStore, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "windsurf.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sqliteStore.Close()

	verify := newProxyVerifyServer(t)
	m := NewManager(Config{
		DB:               sqliteStore.DB,
		AccountBinding:   true,
		TestURL:          verify.URL,
		AllowPrivate:     true,
		Provider:         "novproxy",
		Protocol:         "http",
		Host:             "127.0.0.1",
		Port:             1000,
		UsernameTemplate: "user-sid-{sid}-ttl-{ttl}",
		Password:         "secret",
		TTLMinutes:       60,
	})
	result, err := m.BindAccount(context.Background(), 7, true)
	if err != nil {
		t.Fatal(err)
	}
	if result.Binding == nil || result.Binding.Status != BindingActive || result.Binding.EgressIP != "203.0.113.10" {
		t.Fatalf("binding=%+v", result.Binding)
	}
	res := m.ReserveForAccount(7, "http://static-account-proxy:8080")
	if res.ProxyURL == "http://static-account-proxy:8080" || !strings.Contains(res.ProxyURL, "127.0.0.1:1000") || !res.binding {
		t.Fatalf("dynamic binding should win: %+v", res)
	}
	snap := m.Snapshot()
	if len(snap.Bindings) != 1 || strings.Contains(snap.Bindings[0].MaskedURL, "secret") || snap.Summary.Bound != 1 {
		t.Fatalf("snapshot=%+v", snap)
	}
}

func TestExpiredBindingMarksExpiredAndFallsBack(t *testing.T) {
	sqliteStore, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "windsurf.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sqliteStore.Close()

	m := NewManager(Config{DB: sqliteStore.DB, Default: "http://default:8080", AllowPrivate: true})
	if err := m.saveBinding(&AccountBinding{
		AccountID: 1,
		Provider:  "novproxy",
		Protocol:  "http",
		Host:      "expired.proxy.test",
		Port:      1000,
		Username:  "user-sid-old-ttl-1",
		Password:  "secret",
		SessionID: "old",
		Status:    BindingActive,
		ExpiresAt: time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	if active := m.ActiveBinding(1); active != nil {
		t.Fatalf("expired binding should not be active: %+v", active)
	}
	b, err := m.Binding(1)
	if err != nil {
		t.Fatal(err)
	}
	if b == nil || b.Status != BindingExpired || b.VerifyError != "binding_expired" {
		t.Fatalf("binding not marked expired: %+v", b)
	}
	res := m.ReserveForAccount(1, "")
	if res.ProxyURL != "http://default:8080" || res.binding {
		t.Fatalf("expected default fallback: %+v", res)
	}
}

func TestRecordFailureCoolsDynamicProxy(t *testing.T) {
	m := NewManager(Config{Dynamic: []string{"http://proxy-a:8080", "http://proxy-b:8080"}, RotateOnError: true, Cooldown: time.Hour})
	failed := m.Reserve("")
	m.RecordFailure(failed, errors.New("dial timeout"))
	m.Release(failed)
	next := m.Reserve("")
	if next.ID == failed.ID {
		t.Fatalf("cooled proxy was selected again: failed=%+v next=%+v", failed, next)
	}
	snap := m.Snapshot()
	var found Entry
	for _, item := range snap.Entries {
		if item.ID == failed.ID {
			found = item
		}
	}
	if found.Failures != 1 || found.LastError == "" || !found.CooldownUntil.After(time.Now()) {
		t.Fatalf("failure state missing: %+v", found)
	}
}

func TestRecordFailureRedactsLastError(t *testing.T) {
	m := NewManager(Config{Dynamic: []string{"http://proxy-a.example.com:8080"}, RotateOnError: true, Cooldown: time.Hour})
	res := m.Reserve("")
	m.RecordFailure(res, errors.New("proxy failed with Authorization: Bearer sk-test_abcdefghijklmnopqrstuvwxyz user@example.com"))
	snap := m.Snapshot()
	if len(snap.Entries) != 1 {
		t.Fatalf("entries=%+v", snap.Entries)
	}
	errText := snap.Entries[0].LastError
	if strings.Contains(errText, "sk-test_abcdefghijklmnopqrstuvwxyz") || strings.Contains(errText, "user@example.com") {
		t.Fatalf("proxy error leaked secret: %s", errText)
	}
}

func TestMarkBindingFailureSetsFailedAndAutoRotates(t *testing.T) {
	sqliteStore, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "windsurf.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sqliteStore.Close()

	verify := newProxyVerifyServer(t)
	m := NewManager(Config{
		DB:               sqliteStore.DB,
		AccountBinding:   true,
		RotateOnError:    true,
		TestURL:          verify.URL,
		AllowPrivate:     true,
		Host:             "127.0.0.1",
		Port:             1000,
		UsernameTemplate: "user-sid-{sid}-ttl-{ttl}",
		Password:         "secret",
	})
	if _, err := m.BindAccount(context.Background(), 4, true); err != nil {
		t.Fatal(err)
	}
	before, _ := m.Binding(4)
	failed, err := m.MarkBindingFailure(4, errors.New("proxy failed Authorization: Bearer sk-secret_abcdefghijklmnopqrstuvwxyz"), false)
	if err != nil {
		t.Fatal(err)
	}
	if failed == nil || failed.Status != BindingFailed || failed.FailCount != 1 || strings.Contains(failed.VerifyError, "sk-secret_abcdefghijklmnopqrstuvwxyz") {
		t.Fatalf("failed binding=%+v", failed)
	}
	after, _ := m.Binding(4)
	if after.SessionID != before.SessionID || after.Status != BindingFailed {
		t.Fatalf("auto rebind should be disabled, before=%+v after=%+v", before, after)
	}
}

func TestWorkerPlanPriorities(t *testing.T) {
	sqliteStore, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "windsurf.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sqliteStore.Close()

	now := time.Now()
	m := NewManager(Config{DB: sqliteStore.DB, AccountBinding: true, AutoBindNew: true, WorkerBatchSize: 10, RenewBefore: 15 * time.Minute, AllowPrivate: true})
	for _, binding := range []*AccountBinding{
		{AccountID: 1, Provider: "novproxy", Protocol: "http", Host: "a.test", Port: 1000, Status: BindingFailed},
		{AccountID: 2, Provider: "novproxy", Protocol: "http", Host: "b.test", Port: 1000, Status: BindingExpired},
		{AccountID: 3, Provider: "novproxy", Protocol: "http", Host: "c.test", Port: 1000, Status: BindingActive, ExpiresAt: now.Add(5 * time.Minute)},
		{AccountID: 4, Provider: "novproxy", Protocol: "http", Host: "d.test", Port: 1000, Status: BindingActive, ExpiresAt: now.Add(time.Hour)},
		{AccountID: 6, Provider: "novproxy", Protocol: "http", Host: "e.test", Port: 1000, Status: BindingSuspended},
	} {
		if err := m.saveBinding(binding); err != nil {
			t.Fatal(err)
		}
	}
	plan, err := m.WorkerPlan([]AccountRef{
		{ID: 1, Active: true}, {ID: 2, Active: true}, {ID: 3, Active: true}, {ID: 4, Active: true}, {ID: 5, Active: true}, {ID: 6, Active: true}, {ID: 7, Enabled: true, Banned: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(plan))
	for _, item := range plan {
		got = append(got, fmt.Sprintf("%d:%s:%d", item.AccountID, item.Reason, item.Priority))
	}
	want := []string{"1:failed:1", "2:expired:1", "3:expiring_soon:2", "5:unbound:3"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("plan got=%v want=%v", got, want)
	}
}

func TestRunMaintenanceHonorsConcurrency(t *testing.T) {
	sqliteStore, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "windsurf.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sqliteStore.Close()

	var current int32
	var maxSeen int32
	verify := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&current, 1)
		for {
			old := atomic.LoadInt32(&maxSeen)
			if n <= old || atomic.CompareAndSwapInt32(&maxSeen, old, n) {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
		atomic.AddInt32(&current, -1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ip":"203.0.113.10","country":"US","region":"NJ","city":"Newark","org":"AS test"}`))
	}))
	defer verify.Close()

	m := NewManager(Config{
		DB:                sqliteStore.DB,
		AccountBinding:    true,
		AutoBindNew:       true,
		WorkerBatchSize:   4,
		WorkerConcurrency: 2,
		TestURL:           verify.URL,
		AllowPrivate:      true,
		Host:              "127.0.0.1",
		Port:              1000,
		UsernameTemplate:  "user-sid-{sid}-ttl-{ttl}",
	})
	result, err := m.RunMaintenance(context.Background(), []AccountRef{{ID: 1, Active: true}, {ID: 2, Active: true}, {ID: 3, Active: true}, {ID: 4, Active: true}})
	if err != nil {
		t.Fatal(err)
	}
	if result["processed"] != 4 || result["failed"] != 0 || result["concurrency"] != 2 {
		t.Fatalf("result=%+v", result)
	}
	if atomic.LoadInt32(&maxSeen) > 2 {
		t.Fatalf("maintenance exceeded configured concurrency: %d", maxSeen)
	}
	if m.Snapshot().Summary.Bound != 4 {
		t.Fatalf("snapshot=%+v", m.Snapshot())
	}
}

func TestPatchAndDeleteProxy(t *testing.T) {
	m := NewManager(Config{Dynamic: []string{"http://proxy-a:8080"}})
	res := m.Reserve("")
	disabled := false
	cooldown := 30
	item, err := m.Patch(res.ID, &disabled, &cooldown)
	if err != nil {
		t.Fatal(err)
	}
	if item.Enabled || !item.CooldownUntil.After(time.Now()) {
		t.Fatalf("patch item=%+v", item)
	}
	deleted, err := m.Delete(res.ID)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != true || len(m.Snapshot().Entries) != 0 {
		t.Fatalf("delete failed snapshot=%+v", m.Snapshot())
	}
}

func TestGenerateNovproxyDynamicProxy(t *testing.T) {
	m := NewManager(Config{
		Provider:         "novproxy",
		Protocol:         "http",
		Host:             "proxy.vendor.test",
		Port:             1000,
		UsernameTemplate: "user-region-{region}-state-{state}-sid-{sid}-ttl-{ttl}",
		Password:         "secret",
		Region:           "US",
		State:            "New Jersey",
		TTLMinutes:       60,
	})
	item, raw, err := m.GenerateProviderProxy()
	if err != nil {
		t.Fatal(err)
	}
	if item == nil || item.URL != "" || item.MaskedURL == "" || strings.Contains(item.MaskedURL, "secret") {
		t.Fatalf("generated item not masked: item=%+v raw=%s", item, raw)
	}
	if !strings.HasPrefix(raw, "http://") || !strings.Contains(raw, "proxy.vendor.test:1000") || !strings.Contains(raw, "ttl-60") {
		t.Fatalf("generated raw proxy wrong: %s", raw)
	}
	snap := m.Snapshot()
	if snap.Provider != "novproxy" || !snap.PasswordSet || len(snap.Entries) != 1 {
		t.Fatalf("snapshot=%+v", snap)
	}
}

func TestGenerateProviderProxyRejectsUnsupportedProvider(t *testing.T) {
	m := NewManager(Config{Provider: "other"})
	if _, _, err := m.GenerateProviderProxy(); err == nil {
		t.Fatal("expected unsupported provider error")
	}
}

func TestRejectsInvalidProxy(t *testing.T) {
	m := NewManager(Config{})
	if _, err := m.Add("not a url"); err == nil {
		t.Fatal("expected invalid proxy")
	}
}

func TestRejectsPrivateProxyHostsByDefault(t *testing.T) {
	for _, raw := range []string{
		"http://127.0.0.1:8080",
		"http://10.0.0.1:3128",
		"http://169.254.169.254:80",
		"http://[::1]:8080",
		"http://localhost:8080",
		"http://[::ffff:127.0.0.1]:8080",
	} {
		if err := ValidateURL(raw); err == nil {
			t.Fatalf("expected private proxy rejection for %s", raw)
		}
	}
	if err := ValidateURL("http://8.8.8.8:8080"); err != nil {
		t.Fatalf("public proxy rejected: %v", err)
	}
}

func TestAllowPrivateProxyEscapeHatch(t *testing.T) {
	if err := ValidateURLWithPrivate("http://127.0.0.1:8080", true); err != nil {
		t.Fatalf("allow private should accept loopback: %v", err)
	}
	m := NewManager(Config{AllowPrivate: true})
	if _, err := m.Add("http://127.0.0.1:8080"); err != nil {
		t.Fatalf("manager allow private rejected loopback: %v", err)
	}
}

func TestRejectsPrivateProxyTestTarget(t *testing.T) {
	if err := testProxy(t.Context(), time.Millisecond, "http://127.0.0.1:1234", "http://8.8.8.8:8080"); err == nil {
		t.Fatal("expected private test target rejection")
	}
}

func TestPersistentDynamicProxyPool(t *testing.T) {
	sqliteStore, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "windsurf.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sqliteStore.Close()

	first := NewManager(Config{DB: sqliteStore.DB, RotateOnError: true, Cooldown: time.Hour})
	item, err := first.Add("http://user:secret@proxy-a:8080")
	if err != nil {
		t.Fatal(err)
	}
	first.RecordFailure(Reservation{ID: item.ID, ProxyURL: "http://user:secret@proxy-a:8080", dynamic: true}, errors.New("dial timeout"))

	second := NewManager(Config{DB: sqliteStore.DB, RotateOnError: true, Cooldown: time.Hour})
	snap := second.Snapshot()
	if !snap.Persistent || len(snap.Entries) != 1 {
		t.Fatalf("snapshot=%+v", snap)
	}
	loaded := snap.Entries[0]
	if loaded.ID != item.ID || loaded.URL != "" || loaded.MaskedURL != "http://user:%2A%2A%2A@proxy-a:8080" {
		t.Fatalf("loaded proxy was not persisted/masked correctly: %+v", loaded)
	}
	if loaded.Failures != 1 || loaded.LastError == "" || !loaded.CooldownUntil.After(time.Now()) {
		t.Fatalf("failure state was not persisted: %+v", loaded)
	}

	deleted, err := second.Delete(item.ID)
	if err != nil || !deleted {
		t.Fatalf("delete deleted=%v err=%v", deleted, err)
	}
	third := NewManager(Config{DB: sqliteStore.DB})
	if len(third.Snapshot().Entries) != 0 {
		t.Fatalf("delete was not persisted: %+v", third.Snapshot())
	}
}

func newProxyVerifyServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ip":"203.0.113.10","country":"US","region":"NJ","city":"Newark","org":"AS test"}`))
	}))
}
