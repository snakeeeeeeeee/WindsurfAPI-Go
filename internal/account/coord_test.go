package account

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/zhangyu/windsurfapi-go/internal/store"
)

type testCoordinator struct {
	allow    bool
	released int
	refunded int
	cooldown int
}

func (c *testCoordinator) CanReserve(ctx context.Context, account Account, modelID string) (bool, string) {
	if !c.allow {
		return false, "blocked"
	}
	return true, ""
}

func (c *testCoordinator) Reserve(ctx context.Context, account Account, modelID string, ts time.Time) (func(), bool, string) {
	if !c.allow {
		return func() {}, false, "blocked"
	}
	return func() { c.released++ }, true, ""
}

func (c *testCoordinator) Release(ctx context.Context, accountID int) {
	c.released++
}

func (c *testCoordinator) Refund(ctx context.Context, accountID int, ts time.Time) {
	c.refunded++
}

func (c *testCoordinator) MarkCooldown(ctx context.Context, accountID int, modelID string, until time.Time) {
	c.cooldown++
}

func (c *testCoordinator) ClearCooldown(ctx context.Context, accountID int, modelID string) {}

func (c *testCoordinator) Snapshot(ctx context.Context) map[string]any {
	return map[string]any{"enabled": true}
}

type sharedCoordinator struct {
	mu          sync.Mutex
	maxInflight int
	inflight    map[int]int
	maxObserved map[int]int
	released    int
	refunded    int
	cooldowns   int
}

func newSharedCoordinator(maxInflight int) *sharedCoordinator {
	return &sharedCoordinator{
		maxInflight: maxInflight,
		inflight:    map[int]int{},
		maxObserved: map[int]int{},
	}
}

func (c *sharedCoordinator) CanReserve(ctx context.Context, account Account, modelID string) (bool, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.maxInflight > 0 && c.inflight[account.ID] >= c.maxInflight {
		return false, "shared_inflight_full"
	}
	return true, ""
}

func (c *sharedCoordinator) Reserve(ctx context.Context, account Account, modelID string, ts time.Time) (func(), bool, string) {
	c.mu.Lock()
	if c.maxInflight > 0 && c.inflight[account.ID] >= c.maxInflight {
		c.mu.Unlock()
		return func() {}, false, "shared_inflight_full"
	}
	c.inflight[account.ID]++
	if c.inflight[account.ID] > c.maxObserved[account.ID] {
		c.maxObserved[account.ID] = c.inflight[account.ID]
	}
	c.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() { c.Release(context.Background(), account.ID) })
	}, true, ""
}

func (c *sharedCoordinator) Release(ctx context.Context, accountID int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.inflight[accountID] > 0 {
		c.inflight[accountID]--
	}
	c.released++
}

func (c *sharedCoordinator) Refund(ctx context.Context, accountID int, ts time.Time) {
	c.mu.Lock()
	c.refunded++
	c.mu.Unlock()
	c.Release(ctx, accountID)
}

func (c *sharedCoordinator) MarkCooldown(ctx context.Context, accountID int, modelID string, until time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cooldowns++
}

func (c *sharedCoordinator) ClearCooldown(ctx context.Context, accountID int, modelID string) {}

func (c *sharedCoordinator) Snapshot(ctx context.Context) map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return map[string]any{
		"enabled":      true,
		"max_observed": copyIntMap(c.maxObserved),
	}
}

func TestCoordinatorBlocksReservation(t *testing.T) {
	mgr := testManager(t)
	_, _ = mgr.AddAccount("coord@example.com", "tok", "u", "", "")
	mgr.SetCoordinator(&testCoordinator{allow: false})
	if _, err := mgr.Reserve(context.Background(), "m", nil); err == nil {
		t.Fatal("expected blocked coordinator to prevent reservation")
	}
}

func TestCoordinatorSnapshotAndCooldown(t *testing.T) {
	mgr := testManager(t)
	id, _ := mgr.AddAccount("coord-ok@example.com", "tok", "u", "", "")
	coord := &testCoordinator{allow: true}
	mgr.SetCoordinator(coord)
	res, err := mgr.Reserve(context.Background(), "m", nil)
	if err != nil {
		t.Fatal(err)
	}
	mgr.Release(res)
	if coord.released == 0 {
		t.Fatal("coordinator release was not called")
	}
	if err := mgr.MarkCooldown(int(id), "m", time.Now().Add(time.Minute), "test"); err != nil {
		t.Fatal(err)
	}
	if coord.cooldown == 0 {
		t.Fatal("coordinator cooldown was not called")
	}
	if enabled, _ := mgr.Snapshot().Coordinator["enabled"].(bool); !enabled {
		t.Fatalf("coordinator snapshot missing: %+v", mgr.Snapshot().Coordinator)
	}
}

func TestSharedCoordinatorPreventsMultiInstanceOveruse(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "windsurf.db")
	sqliteStore, err := store.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer sqliteStore.Close()
	seed := NewManager(sqliteStore)
	for i := 0; i < 4; i++ {
		if _, err := seed.AddAccount(fmt.Sprintf("shared-%d@example.com", i), "tok", "u", "", ""); err != nil {
			t.Fatal(err)
		}
	}

	coord := newSharedCoordinator(2)
	mgrA := NewManager(sqliteStore)
	mgrB := NewManager(sqliteStore)
	mgrA.SetCoordinator(coord)
	mgrB.SetCoordinator(coord)

	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for i := 0; i < 40; i++ {
		wg.Add(1)
		mgr := mgrA
		if i%2 == 1 {
			mgr = mgrB
		}
		go func(m *Manager) {
			defer wg.Done()
			res, err := m.Reserve(context.Background(), "claude-sonnet-4.6", nil)
			if err != nil {
				errs <- err
				return
			}
			time.Sleep(time.Millisecond)
			m.Release(res)
		}(mgr)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("reserve failed: %v", err)
	}

	coord.mu.Lock()
	defer coord.mu.Unlock()
	if len(coord.maxObserved) < 2 {
		t.Fatalf("multi-instance reservations concentrated too narrowly: %+v", coord.maxObserved)
	}
	for id, n := range coord.maxObserved {
		if n > coord.maxInflight {
			t.Fatalf("account %d exceeded shared max inflight: max=%d observed=%d all=%+v", id, coord.maxInflight, n, coord.maxObserved)
		}
	}
	for id, n := range coord.inflight {
		if n != 0 {
			t.Fatalf("account %d leaked shared inflight=%d", id, n)
		}
	}
}

func copyIntMap(in map[int]int) map[int]int {
	out := make(map[int]int, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
