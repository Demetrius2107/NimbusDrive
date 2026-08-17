// Package cache 封装 go-redis 客户端，提供秒传哈希池、会话、分享缓存、限流。
package cache

import (
	"context"
	"fmt"
	"time"

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

// SetShare 写入分享缓存（write-through：PG 写成功后同步写）。
// fields 为 Hash 字段；ttl 为过期时间，无过期时调用方应传一个默认上限（如 7 天）。
func (r *Redis) SetShare(ctx context.Context, id string, fields map[string]interface{}, ttl time.Duration) error {
	key := "share:" + id
	if err := r.Client.HSet(ctx, key, fields).Err(); err != nil {
		return fmt.Errorf("hset share cache: %w", err)
	}
	if err := r.Client.Expire(ctx, key, ttl).Err(); err != nil {
		return fmt.Errorf("expire share cache: %w", err)
	}
	return nil
}

// GetShare 读取分享缓存。返回 nil, nil 表示缓存未命中（miss 由调用方回源 PG）。
func (r *Redis) GetShare(ctx context.Context, id string) (map[string]string, error) {
	key := "share:" + id
	res, err := r.Client.HGetAll(ctx, key).Result()
	if err != nil {
		return nil, fmt.Errorf("hgetall share cache: %w", err)
	}
	if len(res) == 0 {
		return nil, nil
	}
	return res, nil
}

// DelShare 删除分享缓存（取消分享或发现过期时调用）。
func (r *Redis) DelShare(ctx context.Context, id string) error {
	key := "share:" + id
	if err := r.Client.Del(ctx, key).Err(); err != nil {
		return fmt.Errorf("del share cache: %w", err)
	}
	return nil
}

// IncrShareAccess 缓存中 access_count +1（PG 自增后同步缓存）。
func (r *Redis) IncrShareAccess(ctx context.Context, id string) error {
	key := "share:" + id
	if err := r.Client.HIncrBy(ctx, key, "access_count", 1).Err(); err != nil {
		return fmt.Errorf("hincr share access: %w", err)
	}
	return nil
}
