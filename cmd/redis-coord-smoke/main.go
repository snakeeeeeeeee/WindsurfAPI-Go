package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"path/filepath"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/zhangyu/windsurfapi-go/internal/account"
	"github.com/zhangyu/windsurfapi-go/internal/store"
)

func main() {
	redisAddr := flag.String("redis", "127.0.0.1:6380", "Redis address")
	dbPath := flag.String("db", filepath.Join("data", "redis-coord-smoke.db"), "SQLite DB path for smoke")
	accounts := flag.Int("accounts", 3, "accounts to seed when DB is empty")
	workers := flag.Int("workers", 24, "concurrent reservations split across two managers")
	maxInflight := flag.Int("max-inflight", 2, "Redis max inflight per account")
	hold := flag.Duration("hold", 25*time.Millisecond, "reservation hold duration")
	model := flag.String("model", "claude-sonnet-4.6", "model id")
	prefix := flag.String("prefix", "windsurfapi:redis-smoke", "Redis key prefix")
	flag.Parse()

	ctx := context.Background()
	rdb := redis.NewClient(&redis.Options{Addr: *redisAddr})
	defer rdb.Close()
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Fatalf("redis ping failed: %v", err)
	}
	if keys := keysForPrefix(ctx, rdb, *prefix); len(keys) > 0 {
		if err := rdb.Del(ctx, keys...).Err(); err != nil {
			log.Fatalf("redis cleanup failed: %v", err)
		}
	}

	sqliteStore, err := store.NewSQLiteStore(*dbPath)
	if err != nil {
		log.Fatalf("sqlite init failed: %v", err)
	}
	defer sqliteStore.Close()
	seedAccounts(sqliteStore, *accounts)

	cfg := account.RedisCoordinatorConfig{
		Prefix:                *prefix,
		MaxInflightPerAccount: *maxInflight,
		ReservationTTL:        30 * time.Second,
	}
	mgrA := account.NewManager(sqliteStore)
	mgrB := account.NewManager(sqliteStore)
	mgrA.SetCoordinator(account.NewRedisCoordinator(rdb, cfg))
	mgrB.SetCoordinator(account.NewRedisCoordinator(rdb, cfg))

	var wg sync.WaitGroup
	errs := make(chan error, *workers)
	for i := 0; i < *workers; i++ {
		wg.Add(1)
		mgr := mgrA
		if i%2 == 1 {
			mgr = mgrB
		}
		go func() {
			defer wg.Done()
			res, err := mgr.Reserve(ctx, *model, nil)
			if err != nil {
				errs <- err
				return
			}
			time.Sleep(*hold)
			mgr.Release(res)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		log.Fatalf("reserve failed: %v", err)
	}

	active, err := activeInflight(ctx, rdb, *prefix)
	if err != nil {
		log.Fatalf("read inflight failed: %v", err)
	}
	if active != 0 {
		log.Fatalf("redis inflight leaked: %d", active)
	}

	acc, err := firstAccount(sqliteStore)
	if err != nil {
		log.Fatalf("first account: %v", err)
	}
	until := time.Now().Add(2 * time.Minute)
	if err := mgrA.MarkCooldown(acc.ID, *model, until, "redis smoke"); err != nil {
		log.Fatalf("mark cooldown: %v", err)
	}
	if _, err := mgrB.ReserveAccount(ctx, *model, acc.ID); err == nil {
		log.Fatalf("expected Redis cooldown from manager A to block manager B for account %d", acc.ID)
	}

	fmt.Printf("redis_coord_smoke ok redis=%s db=%s accounts=%d workers=%d max_inflight_per_account=%d prefix=%s\n", *redisAddr, *dbPath, *accounts, *workers, *maxInflight, *prefix)
}

func seedAccounts(sqliteStore *store.SQLiteStore, n int) {
	mgr := account.NewManager(sqliteStore)
	existing, err := mgr.GetAllAccounts()
	if err == nil && len(existing) >= n {
		return
	}
	for i := len(existing); i < n; i++ {
		email := fmt.Sprintf("redis-smoke-%d@example.com", i+1)
		if _, err := mgr.AddAccount(email, fmt.Sprintf("tok-%d", i+1), fmt.Sprintf("u%d", i+1), "", ""); err != nil {
			log.Fatalf("seed account %s: %v", email, err)
		}
	}
}

func firstAccount(sqliteStore *store.SQLiteStore) (*account.Account, error) {
	mgr := account.NewManager(sqliteStore)
	accounts, err := mgr.GetEnabledAccounts()
	if err != nil {
		return nil, err
	}
	if len(accounts) == 0 {
		return nil, fmt.Errorf("no enabled accounts")
	}
	return &accounts[0], nil
}

func keysForPrefix(ctx context.Context, rdb *redis.Client, prefix string) []string {
	var keys []string
	iter := rdb.Scan(ctx, 0, prefix+":*", 100).Iterator()
	for iter.Next(ctx) {
		keys = append(keys, iter.Val())
	}
	return keys
}

func activeInflight(ctx context.Context, rdb *redis.Client, prefix string) (int, error) {
	keys := keysForPrefix(ctx, rdb, prefix)
	total := 0
	for _, key := range keys {
		if !hasSuffix(key, ":inflight") {
			continue
		}
		n, err := rdb.Get(ctx, key).Int()
		if err != nil && err != redis.Nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}

func hasSuffix(s, suffix string) bool {
	if len(s) < len(suffix) {
		return false
	}
	return s[len(s)-len(suffix):] == suffix
}
