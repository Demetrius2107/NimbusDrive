// Package quota 实现配额配置变更的实时推送。
//
// 推送链路：
//  管理员改配额 → Notifier.NotifyChange → Redis PUBLISH（实时扇出）+ XADD（持久化补发）
//  → 各 APIServer 实例的 SSE 连接订阅 → 推给在线客户端
//
// 可靠性：
//  - 版本号：每条变更带递增 version（Redis INCR），SSE event id = version
//  - Last-Event-ID 补发：客户端断线重连时浏览器自动带 Last-Event-ID 头，
//    服务端从 Redis Stream 补发 version > lastID 的变更（SSE 协议内置"至少一次"语义）
//  - 降级：Redis 不可用时 SSE 返回 503，前端退化为轮询
package quota

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-redis/redis/v8"
)

// Redis 键/频道常量。
const (
	ChannelQuotaChanges = "nimbus:quota:changes" // pub/sub 频道（实时推）
	StreamQuotaEvents   = "nimbus:quota:events"  // Stream（持久化补发）
	KeyQuotaVersion     = "nimbus:quota:version" // INCR 版本号计数器
	StreamMaxLen        = int64(10000)           // Stream 近似上限
)

// ChangeType 配额变更类型。
type ChangeType string

const (
	ChangeResetAll        ChangeType = "reset_all"           // 批量重置所有用户
	ChangeUserQuota       ChangeType = "user_quota_updated"  // 单用户配额变更
	ChangeUserStatus      ChangeType = "user_status_updated" // 单用户状态变更
)

// QuotaChangeEvent 配额变更事件。PUBLISH 到频道 + XADD 到 Stream。
type QuotaChangeEvent struct {
	Version      int64          `json:"version"`              // 递增版本号（= SSE event id）
	TargetUserID int64          `json:"target_user_id"`       // 0 = 广播给所有用户
	Type         ChangeType     `json:"type"`
	Payload      map[string]any `json:"payload"`
	Timestamp    time.Time      `json:"timestamp"`
}

// Notifier 发布配额变更到 Redis（pub/sub 实时推 + Stream 持久化补发）。
type Notifier struct {
	client *redis.Client
}

// NewNotifier 构造。client 为 nil 时 NotifyChange 返回 nil（降级，不推送）。
func NewNotifier(client *redis.Client) *Notifier {
	return &Notifier{client: client}
}

// NotifyChange 发布一条配额变更。
// targetUserID=0 表示广播给所有用户。返回分配的版本号。
// 双写：PUBLISH（实时推在线连接）+ XADD（持久化供 Last-Event-ID 补发）。
func (n *Notifier) NotifyChange(ctx context.Context, targetUserID int64, changeType ChangeType, payload map[string]any) (int64, error) {
	if n.client == nil {
		return 0, nil // Redis 不可用，降级不推送
	}

	// 原子递增版本号
	version, err := n.client.Incr(ctx, KeyQuotaVersion).Result()
	if err != nil {
		return 0, fmt.Errorf("incr quota version: %w", err)
	}

	evt := QuotaChangeEvent{
		Version:      version,
		TargetUserID: targetUserID,
		Type:         changeType,
		Payload:      payload,
		Timestamp:    time.Now().UTC(),
	}
	data, err := json.Marshal(evt)
	if err != nil {
		return version, fmt.Errorf("marshal quota change event: %w", err)
	}

	// XADD 持久化（供 Last-Event-ID 补发）
	if err := n.client.XAdd(ctx, &redis.XAddArgs{
		Stream: StreamQuotaEvents,
		Values: map[string]interface{}{
			"version": version,
			"data":    string(data),
		},
		MaxLen: StreamMaxLen,
		Approx: true,
	}).Err(); err != nil {
		return version, fmt.Errorf("xadd quota event: %w", err)
	}

	// PUBLISH 实时推送
	if err := n.client.Publish(ctx, ChannelQuotaChanges, data).Err(); err != nil {
		return version, fmt.Errorf("publish quota change: %w", err)
	}

	return version, nil
}

// ReplaySince 从 Redis Stream 读取 version > sinceVersion 的变更（Last-Event-ID 补发）。
// 只返回 targetUserID=0（广播）或 targetUserID 匹配的事件。
func (n *Notifier) ReplaySince(ctx context.Context, sinceVersion int64, userID int64) ([]QuotaChangeEvent, error) {
	if n.client == nil {
		return nil, nil
	}

	// XRANGE 读取所有消息，过滤 version > sinceVersion
	// Stream ID 是自动生成的，与 version 不同，需读取后按 version 过滤
	msgs, err := n.client.XRange(ctx, StreamQuotaEvents, "-", "+").Result()
	if err != nil {
		return nil, fmt.Errorf("xrange quota events: %w", err)
	}

	var events []QuotaChangeEvent
	for _, msg := range msgs {
		dataStr, ok := msg.Values["data"].(string)
		if !ok {
			continue
		}
		var evt QuotaChangeEvent
		if err := json.Unmarshal([]byte(dataStr), &evt); err != nil {
			continue
		}
		if evt.Version <= sinceVersion {
			continue
		}
		// 过滤：广播或目标用户匹配
		if evt.TargetUserID != 0 && evt.TargetUserID != userID {
			continue
		}
		events = append(events, evt)
	}
	return events, nil
}
