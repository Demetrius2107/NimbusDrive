// Package cache 封装 go-redis 客户端，提供秒传哈希池、会话、分享缓存、限流。
package cache

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
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

// --- 分享下载能力令牌（capability token）---

// ShareDownloadToken 是分享下载能力令牌的载荷：APIServer 签发、TransferServer 兑换。
// 令牌只证明"曾被授权"，不携带文件内容；兑换时 TransferServer 再查 files 表补全元信息。
// TraceParent 携带 W3C traceparent 文本，使 TransferServer 兑换时续接 APIServer 的 trace。
type ShareDownloadToken struct {
	ShareID     string `json:"share_id"`
	FileID      int64  `json:"file_id"`
	IssuedAt    int64  `json:"issued_at"`     // unix 秒
	TraceParent string `json:"trace_parent,omitempty"` // W3C traceparent，跨客户端中介边界传播
}

// shareDownloadTokenKey 是令牌在 Redis 中的键。
func shareDownloadTokenKey(token string) string { return "share:dl:" + token }

// redeemShareDownloadTokenScript 原子 GET + DEL：单次消费，防并发重放。
var redeemShareDownloadTokenScript = redis.NewScript(`
local v = redis.call('GET', KEYS[1])
if v then redis.call('DEL', KEYS[1]) end
return v
`)

// IssueShareDownloadToken 签发一次性下载令牌：32 字节随机 → hex(64 字符)。
// 令牌存 Redis，TTL 由调用方控制（建议 5 min）。返回令牌字符串。
// traceParent 为 W3C traceparent 文本，由调用方从 ctx 提取，使兑换端续接 trace。
func (r *Redis) IssueShareDownloadToken(ctx context.Context, shareID string, fileID int64, ttl time.Duration, traceParent string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("read random: %w", err)
	}
	token := hex.EncodeToString(b)

	payload, err := json.Marshal(ShareDownloadToken{
		ShareID:     shareID,
		FileID:      fileID,
		IssuedAt:    time.Now().Unix(),
		TraceParent: traceParent,
	})
	if err != nil {
		return "", fmt.Errorf("marshal token: %w", err)
	}

	if err := r.Client.Set(ctx, shareDownloadTokenKey(token), payload, ttl).Err(); err != nil {
		return "", fmt.Errorf("set share download token: %w", err)
	}
	return token, nil
}

// RedeemShareDownloadToken 兑换令牌：原子 GET + DEL，单次消费。
// 返回令牌载荷与是否命中。未命中/已过期/已消费 → ok=false, nil err。
func (r *Redis) RedeemShareDownloadToken(ctx context.Context, token string) (ShareDownloadToken, bool, error) {
	var tok ShareDownloadToken
	v, err := redeemShareDownloadTokenScript.Run(ctx, r.Client, []string{shareDownloadTokenKey(token)}).Text()
	if err != nil {
		if err == redis.Nil {
			return tok, false, nil
		}
		return tok, false, fmt.Errorf("redeem share download token: %w", err)
	}
	if v == "" {
		return tok, false, nil
	}
	if err := json.Unmarshal([]byte(v), &tok); err != nil {
		return tok, false, fmt.Errorf("unmarshal token: %w", err)
	}
	return tok, true, nil
}
