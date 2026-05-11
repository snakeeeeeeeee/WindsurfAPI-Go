package store

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// RedisStore 封装 Redis 连接
type RedisStore struct {
	Client *redis.Client
}

// NewRedisStore 创建 Redis 存储
func NewRedisStore(addr, password string, db int) (*RedisStore, error) {
	client := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       db,
	})

	ctx := context.Background()
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("ping redis: %w", err)
	}

	return &RedisStore{Client: client}, nil
}

// Close 关闭 Redis 连接
func (r *RedisStore) Close() error {
	return r.Client.Close()
}
