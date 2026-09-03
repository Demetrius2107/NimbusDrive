package cron

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/Demetrius2107/NimbusDrive/internal/domain"
	"github.com/Demetrius2107/NimbusDrive/internal/store"
	"github.com/jmoiron/sqlx"
)

// fakeOutboxFetcher 记录 OutboxRepo 调用。
type fakeOutboxFetcher struct {
	mu            sync.Mutex
	pending       []store.OutboxMessage
	published     []string
	attempts      []string
	failMarkOn    string // 该 ID 的 MarkPublished 返回错误
	failFetch     bool
}

func (f *fakeOutboxFetcher) FetchPending(_ context.Context, _ sqlx.ExtContext, _ int) ([]store.OutboxMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failFetch {
		return nil, errors.New("simulated fetch error")
	}
	return f.pending, nil
}

func (f *fakeOutboxFetcher) MarkPublished(_ context.Context, _ sqlx.ExtContext, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id == f.failMarkOn {
		return errors.New("simulated mark error")
	}
	f.published = append(f.published, id)
	return nil
}

func (f *fakeOutboxFetcher) IncAttempt(_ context.Context, _ sqlx.ExtContext, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempts = append(f.attempts, id)
	return nil
}

func (f *fakeOutboxFetcher) CountPending(_ context.Context) (int64, error) {
	return int64(len(f.pending)), nil
}

// fakeEventPublisher 记录 Publish 调用，可按 event ID 模拟失败。
type fakeEventPublisher struct {
	mu          sync.Mutex
	published   []*domain.Event
	failOnID    string
}

func (f *fakeEventPublisher) Publish(_ context.Context, evt *domain.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if evt.ID == f.failOnID {
		return errors.New("simulated publish error")
	}
	f.published = append(f.published, evt)
	return nil
}

// makeOutboxMsg 构造一条 outbox 消息（payload 为有效 JSON）。
func makeOutboxMsg(id, eventType string) store.OutboxMessage {
	payload, _ := json.Marshal(map[string]any{"file_id": 1})
	return store.OutboxMessage{
		ID:        id,
		EventType: eventType,
		Payload:   payload,
	}
}

// TestOutboxRelay_relayBatch_Publishes 验证正常投递路径。
func TestOutboxRelay_relayBatch_Publishes(t *testing.T) {
	fo := &fakeOutboxFetcher{
		pending: []store.OutboxMessage{
			makeOutboxMsg("m1", "file.uploaded"),
			makeOutboxMsg("m2", "share.created"),
		},
	}
	fp := &fakeEventPublisher{}
	c := &OutboxRelay{
		outbox:    fo,
		emitter:   fp,
		batchSize: 10,
	}
	published, failed, scanned := c.relayBatch(context.Background(), nil)

	if published != 2 || failed != 0 || scanned != 2 {
		t.Fatalf("expected 2 published/0 failed/2 scanned, got %d/%d/%d", published, failed, scanned)
	}
	if len(fo.published) != 2 {
		t.Fatalf("expected 2 marked published, got %d", len(fo.published))
	}
}

// TestOutboxRelay_relayBatch_PublishFailureRetries 验证 Publish 失败时递增 attempt 不回写。
func TestOutboxRelay_relayBatch_PublishFailureRetries(t *testing.T) {
	fo := &fakeOutboxFetcher{
		pending: []store.OutboxMessage{
			makeOutboxMsg("m1", "file.uploaded"),
			makeOutboxMsg("m2", "share.created"),
		},
	}
	fp := &fakeEventPublisher{failOnID: "m1"} // m1 投递失败
	c := &OutboxRelay{
		outbox:    fo,
		emitter:   fp,
		batchSize: 10,
	}
	published, failed, scanned := c.relayBatch(context.Background(), nil)

	// m1 失败（IncAttempt），m2 成功
	if published != 1 || failed != 1 || scanned != 2 {
		t.Fatalf("expected 1 published/1 failed/2 scanned, got %d/%d/%d", published, failed, scanned)
	}
	if len(fo.attempts) != 1 || fo.attempts[0] != "m1" {
		t.Fatalf("expected m1 attempt incremented, got %v", fo.attempts)
	}
	if len(fo.published) != 1 || fo.published[0] != "m2" {
		t.Fatalf("expected only m2 marked published, got %v", fo.published)
	}
}

// TestOutboxRelay_relayBatch_MarkFailureRedelivers 验证 MarkPublished 失败时不计入成功（下轮重发）。
func TestOutboxRelay_relayBatch_MarkFailureRedelivers(t *testing.T) {
	fo := &fakeOutboxFetcher{
		pending: []store.OutboxMessage{
			makeOutboxMsg("m1", "file.uploaded"),
		},
		failMarkOn: "m1", // XAdd 成功但回写失败
	}
	fp := &fakeEventPublisher{}
	c := &OutboxRelay{
		outbox:    fo,
		emitter:   fp,
		batchSize: 10,
	}
	published, failed, scanned := c.relayBatch(context.Background(), nil)

	// XAdd 成功（fp.published=1）但 MarkPublished 失败 → 不计入 published
	if published != 0 || failed != 1 || scanned != 1 {
		t.Fatalf("expected 0 published/1 failed/1 scanned, got %d/%d/%d", published, failed, scanned)
	}
	if len(fp.published) != 1 {
		t.Fatalf("expected XAdd attempted, got %d", len(fp.published))
	}
	if len(fo.published) != 0 {
		t.Fatalf("expected 0 marked published (mark failed), got %d", len(fo.published))
	}
}

// TestOutboxRelay_relayBatch_EmptyPending 验证无待投递消息时返回 0。
func TestOutboxRelay_relayBatch_EmptyPending(t *testing.T) {
	fo := &fakeOutboxFetcher{pending: nil}
	fp := &fakeEventPublisher{}
	c := &OutboxRelay{
		outbox:    fo,
		emitter:   fp,
		batchSize: 10,
	}
	published, failed, scanned := c.relayBatch(context.Background(), nil)

	if published != 0 || failed != 0 || scanned != 0 {
		t.Fatalf("expected all 0 for empty pending, got %d/%d/%d", published, failed, scanned)
	}
}
