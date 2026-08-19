package metrics

// MultiConsumer 聚合多个 ConsumerMetrics（如 APIServer 的 5 个消费者），
// 使 InfraCollector 可一次 scrape 所有消费者的合计指标。
// pending 按各 stream 独立保留 label（不合并），processed/error/dlq 求和。
type MultiConsumer struct {
	consumers []ConsumerMetrics
}

// NewMultiConsumer 构造聚合器。接受任意数量 ConsumerMetrics（可 nil，自动过滤）。
func NewMultiConsumer(cs ...ConsumerMetrics) *MultiConsumer {
	mc := &MultiConsumer{}
	for _, c := range cs {
		if c != nil {
			mc.consumers = append(mc.consumers, c)
		}
	}
	return mc
}

// PendingLength 对所有 consumer 的所有 stream 求 PEL 长度合计。
// 注意：不同 consumer 可能订阅同一 stream（同 group 不同 name），各自独立 PEL。
// 此处返回合计，InfraCollector 会按 stream label 分组——但多 consumer 同 stream
// 会在同一 label 下被加总，这对 lag 监控是合理的（总未处理数）。
// 为避免 label 冲突，实际 Streams() 返回去重后的 stream 列表。
func (mc *MultiConsumer) PendingLength(stream string) (int64, error) {
	var total int64
	for _, c := range mc.consumers {
		n, err := c.PendingLength(stream)
		if err != nil {
			continue // 某 consumer 查询失败不阻塞其他
		}
		total += n
	}
	return total, nil
}

// Streams 返回所有 consumer 订阅的 stream 去重后的列表。
func (mc *MultiConsumer) Streams() []string {
	seen := make(map[string]struct{})
	var out []string
	for _, c := range mc.consumers {
		for _, s := range c.Streams() {
			if _, ok := seen[s]; !ok {
				seen[s] = struct{}{}
				out = append(out, s)
			}
		}
	}
	return out
}

// ProcessedCount 求和。
func (mc *MultiConsumer) ProcessedCount() uint64 {
	var total uint64
	for _, c := range mc.consumers {
		total += c.ProcessedCount()
	}
	return total
}

// ErrorCount 求和。
func (mc *MultiConsumer) ErrorCount() uint64 {
	var total uint64
	for _, c := range mc.consumers {
		total += c.ErrorCount()
	}
	return total
}

// DLQCount 求和。
func (mc *MultiConsumer) DLQCount() uint64 {
	var total uint64
	for _, c := range mc.consumers {
		total += c.DLQCount()
	}
	return total
}
