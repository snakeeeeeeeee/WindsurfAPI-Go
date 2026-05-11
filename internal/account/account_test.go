package account

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zhangyu/windsurfapi-go/internal/store"
)

func TestSelectAccountReturnsRawFirebaseToken(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "windsurf.db")
	sqliteStore, err := store.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer sqliteStore.Close()

	const rawToken = "devin-session-token$test-token"
	mgr := NewManager(sqliteStore)
	if _, err := mgr.AddAccount("test@example.com", rawToken, "test-user", "", ""); err != nil {
		t.Fatalf("AddAccount: %v", err)
	}

	acc, err := mgr.SelectAccount()
	if err != nil {
		t.Fatalf("SelectAccount: %v", err)
	}
	if acc.FirebaseToken != rawToken {
		t.Fatalf("FirebaseToken = %q, want raw token", acc.FirebaseToken)
	}
}

func TestReserveFiltersCooldownAndUsesOtherModel(t *testing.T) {
	mgr := testManager(t)
	id1, _ := mgr.AddAccount("a@example.com", "tok-a", "u1", "", "")
	_, _ = mgr.AddAccount("b@example.com", "tok-b", "u2", "", "")
	if err := mgr.MarkCooldown(int(id1), "claude-4.5-haiku", time.Now().Add(time.Minute), "test"); err != nil {
		t.Fatal(err)
	}

	res, err := mgr.Reserve(context.Background(), "claude-4.5-haiku", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Release(res)
	if res.Account.Email != "b@example.com" {
		t.Fatalf("reserved %s, want b@example.com", res.Account.Email)
	}

	other, err := mgr.Reserve(context.Background(), "claude-opus-4-7-high", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Release(other)
	if other.Account.Email != "a@example.com" {
		t.Fatalf("model-specific cooldown blocked unrelated model, got %s", other.Account.Email)
	}
}

func TestReserveFiltersBlockedModel(t *testing.T) {
	mgr := testManager(t)
	id1, _ := mgr.AddAccount("blocked@example.com", "tok-a", "u1", "", "")
	_, _ = mgr.AddAccount("open@example.com", "tok-b", "u2", "", "")
	if err := mgr.SetBlockedModels(int(id1), []string{"claude-sonnet-4.6"}); err != nil {
		t.Fatal(err)
	}

	res, err := mgr.Reserve(context.Background(), "claude-sonnet-4.6", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Release(res)
	if res.Account.Email != "open@example.com" {
		t.Fatalf("reserved %s, want open@example.com", res.Account.Email)
	}

	other, err := mgr.Reserve(context.Background(), "claude-opus-4.6", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Release(other)
	if other.Account.Email != "blocked@example.com" {
		t.Fatalf("model block leaked to unrelated model, got %s", other.Account.Email)
	}

	blocked, err := mgr.GetBlockedModels(int(id1))
	if err != nil {
		t.Fatal(err)
	}
	if len(blocked) != 1 || blocked[0] != "claude-sonnet-4.6" {
		t.Fatalf("blocked=%v", blocked)
	}
}

func TestReserveFromLimitsCandidateSet(t *testing.T) {
	mgr := testManager(t)
	id1, _ := mgr.AddAccount("first@example.com", "tok-a", "u1", "", "")
	id2, _ := mgr.AddAccount("second@example.com", "tok-b", "u2", "", "")
	_ = id1
	_ = mgr.UpdateAccount(int(id1), map[string]interface{}{"quota_daily_percent": 100, "quota_weekly_percent": 100})
	_ = mgr.UpdateAccount(int(id2), map[string]interface{}{"quota_daily_percent": 10, "quota_weekly_percent": 10})

	res, err := mgr.ReserveFrom(context.Background(), "claude-sonnet-4.6", []int{int(id2)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Release(res)
	if res.Account.ID != int(id2) {
		t.Fatalf("reserved account %d, want %d", res.Account.ID, id2)
	}
	if _, err := mgr.ReserveFrom(context.Background(), "claude-sonnet-4.6", []int{9999}, nil); err == nil {
		t.Fatal("expected unavailable when allow-list has no usable accounts")
	}
}

func TestSnapshotMasksProxyCredentials(t *testing.T) {
	mgr := testManager(t)
	id, err := mgr.AddAccount("proxy-mask@example.com", "devin-session-token$abc", "u", "http://user:secret@proxy.local:8080", "")
	if err != nil {
		t.Fatal(err)
	}
	snap := mgr.Snapshot()
	for _, row := range snap.Accounts {
		if row.ID != int(id) {
			continue
		}
		if row.ProxyURL != "http://user:%2A%2A%2A@proxy.local:8080" {
			t.Fatalf("proxy not masked: %q", row.ProxyURL)
		}
		return
	}
	t.Fatalf("account %d not found in snapshot", id)
}

func TestReserveAccountHonorsCooldownAndBlockedModel(t *testing.T) {
	mgr := testManager(t)
	id, _ := mgr.AddAccount("sticky@example.com", "tok-a", "u1", "", "")
	if _, err := mgr.ReserveAccount(context.Background(), "claude-sonnet-4.6", int(id)); err != nil {
		t.Fatalf("ReserveAccount initial: %v", err)
	}
	if err := mgr.MarkCooldown(int(id), "claude-sonnet-4.6", time.Now().Add(time.Minute), "rate limited"); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.ReserveAccount(context.Background(), "claude-sonnet-4.6", int(id)); err == nil {
		t.Fatal("expected cooldown to block sticky account")
	}

	id2, _ := mgr.AddAccount("blocked-sticky@example.com", "tok-b", "u2", "", "")
	if err := mgr.SetBlockedModels(int(id2), []string{"claude-sonnet-4.6"}); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.ReserveAccount(context.Background(), "claude-sonnet-4.6", int(id2)); err == nil {
		t.Fatal("expected account-level blocked model to block sticky account")
	}
}

func TestSnapshotShowsPersistedModelCooldownAfterManagerRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "windsurf.db")
	sqliteStore, err := store.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	mgr := NewManager(sqliteStore)
	id, _ := mgr.AddAccount("cooldown-restart@example.com", "tok-a", "u1", "", "")
	until := time.Now().Add(30 * time.Minute).UTC().Truncate(time.Second)
	if err := mgr.MarkCooldown(int(id), "claude-sonnet-4.6", until, "rate limited"); err != nil {
		t.Fatal(err)
	}
	_ = sqliteStore.Close()

	reopened, err := store.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer reopened.Close()
	restarted := NewManager(reopened)
	snap := restarted.Snapshot()
	for _, row := range snap.Accounts {
		if row.ID != int(id) {
			continue
		}
		if row.ModelCooldowns == nil || row.ModelCooldowns["claude-sonnet-4.6"] == "" {
			t.Fatalf("persisted cooldown missing from snapshot: %+v", row)
		}
		if _, err := restarted.ReserveAccount(context.Background(), "claude-sonnet-4.6", int(id)); err == nil {
			t.Fatal("persisted cooldown should block reservation after manager restart")
		}
		return
	}
	t.Fatalf("account %d not found in snapshot", id)
}

func TestReservePrefersInflightThenQuota(t *testing.T) {
	mgr := testManager(t)
	idLow, _ := mgr.AddAccount("low@example.com", "tok-low", "u1", "", "")
	idHigh, _ := mgr.AddAccount("high@example.com", "tok-high", "u2", "", "")
	_ = mgr.UpdateAccount(int(idLow), map[string]interface{}{"quota_daily_percent": 10, "quota_weekly_percent": 10})
	_ = mgr.UpdateAccount(int(idHigh), map[string]interface{}{"quota_daily_percent": 90, "quota_weekly_percent": 90})

	first, err := mgr.Reserve(context.Background(), "m", nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.Account.Email != "high@example.com" {
		t.Fatalf("quota sort picked %s", first.Account.Email)
	}
	second, err := mgr.Reserve(context.Background(), "m", nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.Account.Email != "low@example.com" {
		t.Fatalf("inflight sort picked %s", second.Account.Email)
	}
	mgr.Release(first)
	mgr.Release(second)
}

func TestReserveHonorsLocalMaxInflightPerAccount(t *testing.T) {
	mgr := testManager(t)
	mgr.SetMaxInflightPerAccount(1)
	id1, _ := mgr.AddAccount("one@example.com", "tok-one", "u1", "", "")
	id2, _ := mgr.AddAccount("two@example.com", "tok-two", "u2", "", "")

	first, err := mgr.Reserve(context.Background(), "m", nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := mgr.Reserve(context.Background(), "m", nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.Account.ID == second.Account.ID {
		t.Fatalf("local max inflight was ignored: first=%d second=%d", first.Account.ID, second.Account.ID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if _, err := mgr.Reserve(ctx, "m", nil); err == nil {
		t.Fatal("expected no available account while both accounts are at local inflight limit")
	}
	mgr.Release(first)
	mgr.Release(second)
	again1, err := mgr.ReserveAccount(context.Background(), "m", int(id1))
	if err != nil {
		t.Fatalf("released account should be reservable again: %v", err)
	}
	mgr.Release(again1)
	again2, err := mgr.ReserveAccount(context.Background(), "m", int(id2))
	if err != nil {
		t.Fatalf("second released account should be reservable again: %v", err)
	}
	mgr.Release(again2)
}

func TestReservePenalizesDroughtAccount(t *testing.T) {
	mgr := testManager(t)
	idLow, _ := mgr.AddAccount("drought@example.com", "tok-low", "u1", "", "")
	idHigh, _ := mgr.AddAccount("healthy@example.com", "tok-high", "u2", "", "")
	_ = mgr.UpdateAccount(int(idLow), map[string]interface{}{"quota_daily_percent": 5, "quota_weekly_percent": 5})
	_ = mgr.UpdateAccount(int(idHigh), map[string]interface{}{"quota_daily_percent": 90, "quota_weekly_percent": 90})

	first, err := mgr.Reserve(context.Background(), "m", nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.Account.Email != "healthy@example.com" {
		t.Fatalf("first reserved %s, want healthy@example.com", first.Account.Email)
	}
	second, err := mgr.Reserve(context.Background(), "m", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Release(first)
	defer mgr.Release(second)
	if second.Account.Email != "healthy@example.com" {
		t.Fatalf("drought account was not penalized enough, second=%s", second.Account.Email)
	}

	snap := mgr.Snapshot()
	var drought DebugAccount
	for _, row := range snap.Accounts {
		if row.Email == "drought@example.com" {
			drought = row
		}
	}
	if !drought.Drought || drought.DroughtPenalty == 0 {
		t.Fatalf("drought state missing from snapshot: %+v", drought)
	}
}

func TestRecordFailureOpensModelBreaker(t *testing.T) {
	mgr := testManager(t)
	id1, _ := mgr.AddAccount("flaky@example.com", "tok-a", "u1", "", "")
	_, _ = mgr.AddAccount("backup@example.com", "tok-b", "u2", "", "")
	model := "claude-sonnet-4.6"

	for i := 0; i < modelBreakerThreshold; i++ {
		res := &Reservation{Account: &Account{ID: int(id1), Email: "flaky@example.com"}, ModelID: model}
		mgr.RecordFailure(res, ErrorTransport, context.DeadlineExceeded)
	}

	res, err := mgr.Reserve(context.Background(), model, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Release(res)
	if res.Account.Email != "backup@example.com" {
		t.Fatalf("breaker did not skip flaky account, got %s", res.Account.Email)
	}

	snap := mgr.Snapshot()
	var flaky DebugAccount
	for _, row := range snap.Accounts {
		if row.Email == "flaky@example.com" {
			flaky = row
		}
	}
	if flaky.ModelBreakers == nil || flaky.ModelBreakers[model] == "" {
		t.Fatalf("model breaker missing from snapshot: %+v", flaky)
	}
	if flaky.RecentErrors == nil || flaky.RecentErrors[model] != modelBreakerThreshold {
		t.Fatalf("recent errors missing from snapshot: %+v", flaky)
	}
}

func TestSnapshotRedactsSchedulerErrors(t *testing.T) {
	mgr := testManager(t)
	id, err := mgr.AddAccount("secret@example.com", "tok", "u", "", "")
	if err != nil {
		t.Fatal(err)
	}
	res := &Reservation{Account: &Account{ID: int(id), Email: "secret@example.com"}, ModelID: "claude-sonnet-4.6"}
	mgr.RecordFailure(res, ErrorTransport, context.Canceled)
	if err := mgr.MarkCooldown(int(id), "claude-sonnet-4.6", time.Now().Add(time.Minute), "Authorization: Bearer sk-test_abcdefghijklmnopqrstuvwxyz user@example.com"); err != nil {
		t.Fatal(err)
	}
	snap := mgr.Snapshot()
	raw, _ := json.Marshal(snap.Events)
	text := string(raw)
	if strings.Contains(text, "sk-test_abcdefghijklmnopqrstuvwxyz") || strings.Contains(text, "user@example.com") {
		t.Fatalf("scheduler snapshot leaked secret: %s", text)
	}
}

func TestRecordSuccessClearsRecentFailures(t *testing.T) {
	mgr := testManager(t)
	id, _ := mgr.AddAccount("recover@example.com", "tok", "u", "", "")
	model := "claude-sonnet-4.6"
	res := &Reservation{Account: &Account{ID: int(id), Email: "recover@example.com"}, ModelID: model}

	mgr.RecordFailure(res, ErrorTransport, context.DeadlineExceeded)
	mgr.RecordSuccess(res, nil)

	snap := mgr.Snapshot()
	for _, row := range snap.Accounts {
		if row.Email == "recover@example.com" && row.RecentErrors != nil {
			t.Fatalf("recent errors were not cleared after success: %+v", row)
		}
	}
}

func TestResetAccountErrorsClearsRecentFailuresAndBreakers(t *testing.T) {
	mgr := testManager(t)
	id, _ := mgr.AddAccount("reset@example.com", "tok", "u", "", "")
	model := "claude-sonnet-4.6"
	res := &Reservation{Account: &Account{ID: int(id), Email: "reset@example.com"}, ModelID: model}
	for i := 0; i < modelBreakerThreshold; i++ {
		mgr.RecordFailure(res, ErrorTransport, context.DeadlineExceeded)
	}
	mgr.ResetAccountErrors(int(id))
	snap := mgr.Snapshot()
	for _, row := range snap.Accounts {
		if row.Email != "reset@example.com" {
			continue
		}
		if row.RecentErrors != nil || row.ModelBreakers != nil {
			t.Fatalf("errors were not reset: %+v", row)
		}
		return
	}
	t.Fatal("reset account not found")
}

func TestReserveConcurrentSpreadsInflightAndReleases(t *testing.T) {
	mgr := testManager(t)
	for i := 0; i < 8; i++ {
		_, _ = mgr.AddAccount("c"+string(rune('a'+i))+"@example.com", "tok", "u", "", "")
	}

	var wg sync.WaitGroup
	var countsMu sync.Mutex
	counts := map[int]int{}
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := mgr.Reserve(context.Background(), "m", nil)
			if err == nil {
				countsMu.Lock()
				counts[res.Account.ID]++
				countsMu.Unlock()
				time.Sleep(time.Millisecond)
				mgr.Release(res)
			}
		}()
	}
	wg.Wait()
	if len(counts) < 6 {
		t.Fatalf("reservations concentrated on too few accounts: %+v", counts)
	}
	maxHits := 0
	for _, n := range counts {
		if n > maxHits {
			maxHits = n
		}
	}
	if maxHits > 35 {
		t.Fatalf("reservations over-concentrated on one account: %+v", counts)
	}
	snap := mgr.Snapshot()
	for _, a := range snap.Accounts {
		if a.Inflight != 0 {
			t.Fatalf("account %d inflight = %d", a.ID, a.Inflight)
		}
	}
}

func TestUpdateHealthPersistsQuotaAndRateLimit(t *testing.T) {
	mgr := testManager(t)
	id, _ := mgr.AddAccount("health@example.com", "tok", "u", "", "")
	daily, weekly := 55.0, 66.0
	until := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	if err := mgr.UpdateHealth(int(id), "Trial", &daily, &weekly, &until, "health ok"); err != nil {
		t.Fatal(err)
	}
	acc, err := mgr.GetAccount(int(id))
	if err != nil {
		t.Fatal(err)
	}
	if acc.Tier != "trial" && acc.Tier != "free" {
		t.Fatalf("tier=%q", acc.Tier)
	}
	if acc.QuotaDailyPercent == nil || *acc.QuotaDailyPercent != daily {
		t.Fatalf("daily=%v", acc.QuotaDailyPercent)
	}
	if acc.RateLimitedUntil == nil || acc.RateLimitedUntil.Before(time.Now()) {
		t.Fatalf("rate_limited_until=%v", acc.RateLimitedUntil)
	}
	snap := mgr.Snapshot()
	if snap.Accounts[0].RateLimitedUntil == "" {
		t.Fatalf("debug rate_limited_until missing: %+v", snap.Accounts[0])
	}
}

func testManager(t *testing.T) *Manager {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "windsurf.db")
	sqliteStore, err := store.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = sqliteStore.Close() })
	return NewManager(sqliteStore)
}
