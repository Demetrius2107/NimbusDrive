// Package cache 封装 go-redis 客户端，提供秒传哈希池、会话、分享缓存、限流。
package cache

import (
	"context"
	"fmt"

	"github.com/go-redis/redis/v8"
)

// Redis 封装 redis 客户端。
type Redis struct {
	Client *redis.Client
}

// New 创建 redis 客户端。
func New(ctx context.Context, addr, password string, db int) (*Redis, error) {
	cli := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       db,
	})
	if err := cli.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("ping redis: %w", err)
	}
	return &Redis{Client: cli}, nil
}

// Close 关闭连接。
func (r *Redis) Close() error {
	if r.Client != nil {
		return r.Client.Close()
	}
	return nil
}
