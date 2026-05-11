package account

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

type Coordinator interface {
	CanReserve(ctx context.Context, account Account, modelID string) (bool, string)
	Reserve(ctx context.Context, account Account, modelID string, ts time.Time) (func(), bool, string)
	Release(ctx context.Context, accountID int)
	Refund(ctx context.Context, accountID int, ts time.Time)
	MarkCooldown(ctx context.Context, accountID int, modelID string, until time.Time)
	ClearCooldown(ctx context.Context, accountID int, modelID string)
	Snapshot(ctx context.Context) map[string]any
}

type RedisCoordinatorConfig struct {
	Prefix                string
	MaxInflightPerAccount int
	ReservationTTL        time.Duration
}

type RedisCoordinator struct {
	client *redis.Client
	cfg    RedisCoordinatorConfig
}

const redisReserveScript = `
if redis.call("EXISTS", KEYS[3]) == 1 or redis.call("EXISTS", KEYS[4]) == 1 then
	return "redis_cooldown"
end
local max_inflight = tonumber(ARGV[1]) or 0
local current = tonumber(redis.call("GET", KEYS[1]) or "0")
if max_inflight > 0 and current >= max_inflight then
	return "redis_inflight_full"
end
redis.call("INCR", KEYS[1])
redis.call("EXPIRE", KEYS[1], tonumber(ARGV[2]))
redis.call("ZADD", KEYS[2], tonumber(ARGV[3]), ARGV[5])
redis.call("ZREMRANGEBYSCORE", KEYS[2], "0", tonumber(ARGV[4]))
redis.call("EXPIRE", KEYS[2], tonumber(ARGV[6]))
return "ok"
`

func NewRedisCoordinator(client *redis.Client, cfg RedisCoordinatorConfig) *RedisCoordinator {
	if cfg.Prefix == "" {
		cfg.Prefix = "windsurfapi:scheduler"
	}
	if cfg.ReservationTTL <= 0 {
		cfg.ReservationTTL = 3 * time.Minute
	}
	return &RedisCoordinator{client: client, cfg: cfg}
}

func (c *RedisCoordinator) CanReserve(ctx context.Context, a Account, modelID string) (bool, string) {
	if c == nil || c.client == nil {
		return true, ""
	}
	if c.cooldownActive(ctx, a.ID, "*") || c.cooldownActive(ctx, a.ID, modelID) {
		return false, "redis_cooldown"
	}
	if c.cfg.MaxInflightPerAccount > 0 {
		n, err := c.client.Get(ctx, c.inflightKey(a.ID)).Int()
		if err != nil && err != redis.Nil {
			return false, "redis_error:" + err.Error()
		}
		if n >= c.cfg.MaxInflightPerAccount {
			return false, "redis_inflight_full"
		}
	}
	return true, ""
}

func (c *RedisCoordinator) Reserve(ctx context.Context, a Account, modelID string, ts time.Time) (func(), bool, string) {
	if c == nil || c.client == nil {
		return func() {}, true, ""
	}
	inflight := c.inflightKey(a.ID)
	rpm := c.rpmKey(a.ID)
	result, err := c.client.Eval(ctx, redisReserveScript, []string{
		inflight,
		rpm,
		c.cooldownKey(a.ID, "*"),
		c.cooldownKey(a.ID, modelID),
	},
		c.cfg.MaxInflightPerAccount,
		int(c.cfg.ReservationTTL.Seconds()),
		ts.UnixMilli(),
		ts.Add(-rpmWindow).UnixMilli(),
		ts.UnixNano(),
		int((rpmWindow + c.cfg.ReservationTTL).Seconds()),
	).Text()
	if err != nil {
		return func() {}, false, "redis_error:" + err.Error()
	}
	if result != "ok" {
		return func() {}, false, result
	}
	return func() { c.Release(context.Background(), a.ID) }, true, ""
}

func (c *RedisCoordinator) Release(ctx context.Context, accountID int) {
	if c == nil || c.client == nil {
		return
	}
	key := c.inflightKey(accountID)
	_ = c.client.Eval(ctx, `local v=redis.call("GET", KEYS[1]); if not v or tonumber(v)<=0 then return 0 end; return redis.call("DECR", KEYS[1])`, []string{key}).Err()
}

func (c *RedisCoordinator) Refund(ctx context.Context, accountID int, ts time.Time) {
	if c == nil || c.client == nil {
		return
	}
	_ = c.client.ZRem(ctx, c.rpmKey(accountID), fmt.Sprintf("%d", ts.UnixNano())).Err()
	c.Release(ctx, accountID)
}

func (c *RedisCoordinator) MarkCooldown(ctx context.Context, accountID int, modelID string, until time.Time) {
	if c == nil || c.client == nil || !until.After(time.Now()) {
		return
	}
	if modelID == "" {
		modelID = "*"
	}
	_ = c.client.Set(ctx, c.cooldownKey(accountID, modelID), until.Format(time.RFC3339), time.Until(until)).Err()
}

func (c *RedisCoordinator) ClearCooldown(ctx context.Context, accountID int, modelID string) {
	if c == nil || c.client == nil {
		return
	}
	if modelID == "" {
		modelID = "*"
	}
	_ = c.client.Del(ctx, c.cooldownKey(accountID, modelID)).Err()
}

func (c *RedisCoordinator) Snapshot(ctx context.Context) map[string]any {
	if c == nil || c.client == nil {
		return map[string]any{"enabled": false}
	}
	return map[string]any{
		"enabled":                  true,
		"prefix":                   c.cfg.Prefix,
		"max_inflight_per_account": c.cfg.MaxInflightPerAccount,
		"reservation_ttl_seconds":  int(c.cfg.ReservationTTL.Seconds()),
	}
}

func (c *RedisCoordinator) cooldownActive(ctx context.Context, accountID int, modelID string) bool {
	n, err := c.client.Exists(ctx, c.cooldownKey(accountID, modelID)).Result()
	return err == nil && n > 0
}

func (c *RedisCoordinator) inflightKey(accountID int) string {
	return fmt.Sprintf("%s:acct:%d:inflight", c.cfg.Prefix, accountID)
}

func (c *RedisCoordinator) rpmKey(accountID int) string {
	return fmt.Sprintf("%s:acct:%d:rpm", c.cfg.Prefix, accountID)
}

func (c *RedisCoordinator) cooldownKey(accountID int, modelID string) string {
	return fmt.Sprintf("%s:acct:%d:cooldown:%s", c.cfg.Prefix, accountID, modelID)
}
