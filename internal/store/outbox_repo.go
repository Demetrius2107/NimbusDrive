package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Demetrius2107/NimbusDrive/internal/domain"
	"github.com/jmoiron/sqlx"
)

// OutboxRepo 封装 outbox 表的数据访问（事务型事件投递）。
//
// 解决 dual-write 问题：业务事务（如 completeTransaction）与事件写入同一事务，
// OutboxRelay 轮询 published_at IS NULL 的行 XAdd 后回写。
// at-least-once 投递：消费端已有 Redis SETNX(eventID) 幂等兜底。
// 多实例 relay：FetchPending 用 FOR UPDATE SKIP LOCKED 无主并发。
type OutboxRepo struct {
	db *sqlx.DB
}

// OutboxMessage 对应 outbox 表的一行（relay 投递用）。
type OutboxMessage struct {
	ID           string                 `db:"id" json:"id"`
	EventType    string                 `db:"event_type" json:"event_type"`
	Payload      []byte                 `db:"payload" json:"payload"` // JSONB，relay 解码为 map[string]any
	TraceContext []byte                 `db:"trace_context" json:"trace_context,omitempty"`
	OccurredAt   string                 `db:"occurred_at" json:"occurred_at"`
	Attempt      int                    `db:"attempt" json:"attempt"`
}

// Enqueue 在事务内写入一条 outbox 消息。ext 接受 *sqlx.DB 或 *sqlx.Tx。
// payload 是事件载荷（map[string]any），traceCtx 是 W3C trace context（可为空）。
// 返回生成的 event ID（UUID）。
func (r *OutboxRepo) Enqueue(ctx context.Context, ext sqlx.ExtContext, eventType string, payload map[string]any, traceCtx map[string]string) (string, error) {
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal outbox payload: %w", err)
	}

	var traceJSON []byte
	if len(traceCtx) > 0 {
		traceJSON, err = json.Marshal(traceCtx)
		if err != nil {
			return "", fmt.Errorf("marshal outbox trace_context: %w", err)
		}
	}

	var id string
	const q = `
		INSERT INTO outbox (event_type, payload, trace_context)
		VALUES ($1, $2, $3)
		RETURNING id`
	if err := sqlx.GetContext(ctx, ext, &id, q, eventType, payloadJSON, traceJSON); err != nil {
		return "", fmt.Errorf("insert outbox: %w", err)
	}
	return id, nil
}

// FetchPending 取未投递的消息（必须在事务内调用，配合 FOR UPDATE SKIP LOCKED）。
// ext 必须是 *sqlx.Tx（FOR UPDATE 需事务），按 occurred_at 顺序取 limit 条。
// 不同 relay 实例并发时，SKIP LOCKED 让各实例取不同行，无主并发。
func (r *OutboxRepo) FetchPending(ctx context.Context, ext sqlx.ExtContext, limit int) ([]OutboxMessage, error) {
	const q = `
		SELECT id, event_type, payload, trace_context, occurred_at, attempt
		FROM outbox
		WHERE published_at IS NULL
		ORDER BY occurred_at
		LIMIT $1
		FOR UPDATE SKIP LOCKED`
	var msgs []OutboxMessage
	if err := sqlx.SelectContext(ctx, ext, &msgs, q, limit); err != nil {
		return nil, fmt.Errorf("fetch pending outbox: %w", err)
	}
	return msgs, nil
}

// MarkPublished 标记消息已投递（published_at=now()）。ext 接受 *sqlx.DB 或 *sqlx.Tx。
func (r *OutboxRepo) MarkPublished(ctx context.Context, ext sqlx.ExtContext, id string) error {
	const q = `UPDATE outbox SET published_at = now() WHERE id = $1`
	if _, err := ext.ExecContext(ctx, q, id); err != nil {
		return fmt.Errorf("mark outbox published: %w", err)
	}
	return nil
}

// IncAttempt 递增投递尝试次数（失败时调）。ext 接受 *sqlx.DB 或 *sqlx.Tx。
func (r *OutboxRepo) IncAttempt(ctx context.Context, ext sqlx.ExtContext, id string) error {
	const q = `UPDATE outbox SET attempt = attempt + 1 WHERE id = $1`
	if _, err := ext.ExecContext(ctx, q, id); err != nil {
		return fmt.Errorf("inc outbox attempt: %w", err)
	}
	return nil
}

// CountPending 返回未投递消息数（供 metrics scrape-time gauge）。
func (r *OutboxRepo) CountPending(ctx context.Context) (int64, error) {
	const q = `SELECT count(*) FROM outbox WHERE published_at IS NULL`
	var count int64
	if err := r.db.GetContext(ctx, &count, q); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, fmt.Errorf("count pending outbox: %w", err)
	}
	return count, nil
}

// ToEvent 把 outbox 消息转为 domain.Event（relay 投递用）。
// ID 用 outbox 行的 UUID（消费端幂等去重的 key）。
func (m *OutboxMessage) ToEvent() (*domain.Event, error) {
	var payload map[string]any
	if err := json.Unmarshal(m.Payload, &payload); err != nil {
		return nil, fmt.Errorf("unmarshal outbox payload: %w", err)
	}

	var traceCtx map[string]string
	if len(m.TraceContext) > 0 {
		if err := json.Unmarshal(m.TraceContext, &traceCtx); err != nil {
			return nil, fmt.Errorf("unmarshal outbox trace_context: %w", err)
		}
	}

	return &domain.Event{
		ID:           m.ID,
		Type:         m.EventType,
		OccurredAt:   parseTimestamp(m.OccurredAt),
		Payload:      payload,
		TraceContext: traceCtx,
	}, nil
}

// parseTimestamp 解析 PG timestamptz 字符串。容错：失败返回零值（relay 不依赖此字段）。
func parseTimestamp(s string) (t time.Time) {
	_ = t.UnmarshalText([]byte(s)) // 容错，失败返回零值
	return
}
