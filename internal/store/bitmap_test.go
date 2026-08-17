package store

import (
	"testing"
)

// TestEnsureBitmapLen 验证位图扩容逻辑。
func TestEnsureBitmapLen(t *testing.T) {
	tests := []struct {
		name        string
		bitmap      []byte
		totalChunks int
		wantLen     int
	}{
		{"空位图 1 块", nil, 1, 1},
		{"空位图 8 块", nil, 8, 1},
		{"空位图 9 块跨字节", nil, 9, 2},
		{"空位图 16 块", nil, 16, 2},
		{"空位图 17 块跨字节", nil, 17, 3},
		{"已有 1 字节够用 8 块", make([]byte, 1), 8, 1},
		{"已有 1 字节不够 9 块", make([]byte, 1), 9, 2},
		{"已有 2 字节够用 16 块", make([]byte, 2), 16, 2},
		{"0 块不截断已有位图", make([]byte, 2), 0, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ensureBitmapLen(tt.bitmap, tt.totalChunks)
			if len(got) != tt.wantLen {
				t.Errorf("ensureBitmapLen len = %d, want %d", len(got), tt.wantLen)
			}
		})
	}
}

// TestEnsureBitmapLenPreservesData 验证扩容时保留已有位。
func TestEnsureBitmapLenPreservesData(t *testing.T) {
	original := []byte{0xFF}
	grown := ensureBitmapLen(original, 16)
	if grown[0] != 0xFF {
		t.Errorf("扩容后原有数据丢失: got %02X, want FF", grown[0])
	}
	if len(grown) != 2 {
		t.Fatalf("扩容后长度 = %d, want 2", len(grown))
	}
	if grown[1] != 0x00 {
		t.Errorf("新增字节应初始化为 0, got %02X", grown[1])
	}
}

// TestSetGetBitRoundTrip 验证 setBit → getBit 闭环。
func TestSetGetBitRoundTrip(t *testing.T) {
	bitmap := make([]byte, 2) // 16 位

	// 置位第 0、3、7、8、15 位
	indices := []int{0, 3, 7, 8, 15}
	for _, idx := range indices {
		setBit(bitmap, idx)
	}

	for i := 0; i < 16; i++ {
		got := getBit(bitmap, i)
		want := false
		for _, idx := range indices {
			if i == idx {
				want = true
				break
			}
		}
		if got != want {
			t.Errorf("getBit(%d) = %v, want %v", i, got, want)
		}
	}
}

// TestGetBitOutOfBounds 验证越界访问返回 false 而非 panic。
func TestGetBitOutOfBounds(t *testing.T) {
	bitmap := make([]byte, 1) // 仅 8 位
	if getBit(bitmap, 8) {
		t.Error("getBit(8) on 1-byte bitmap should be false")
	}
	if getBit(bitmap, 100) {
		t.Error("getBit(100) on 1-byte bitmap should be false")
	}
}

// TestSetBitHighBitFirst 验证"高位在前"约定：第 0 位在最高位 0x80。
func TestSetBitHighBitFirst(t *testing.T) {
	bitmap := make([]byte, 1)
	setBit(bitmap, 0)
	if bitmap[0] != 0x80 {
		t.Errorf("setBit(0) 应置最高位: got %02X, want 80", bitmap[0])
	}

	bitmap = make([]byte, 1)
	setBit(bitmap, 7)
	if bitmap[0] != 0x01 {
		t.Errorf("setBit(7) 应置最低位: got %02X, want 01", bitmap[0])
	}
}

// TestMissingChunksScenario 模拟断点续传场景：
// 16 块中上传了 0、1、5、15，验证 MissingChunks 逻辑返回正确缺失列表。
func TestMissingChunksScenario(t *testing.T) {
	totalChunks := 16
	bitmap := ensureBitmapLen(nil, totalChunks)

	uploaded := []int{0, 1, 5, 15}
	for _, idx := range uploaded {
		setBit(bitmap, idx)
	}

	// 复现 MissingChunks 的核心逻辑（不依赖 DB）
	uploadedSet := make(map[int]bool)
	for _, idx := range uploaded {
		uploadedSet[idx] = true
	}

	var missing []int
	for i := 0; i < totalChunks; i++ {
		if !getBit(bitmap, i) {
			missing = append(missing, i)
		}
	}

	wantMissing := []int{2, 3, 4, 6, 7, 8, 9, 10, 11, 12, 13, 14}
	if len(missing) != len(wantMissing) {
		t.Fatalf("missing 数量 = %d, want %d", len(missing), len(wantMissing))
	}
	for i, v := range missing {
		if v != wantMissing[i] {
			t.Errorf("missing[%d] = %d, want %d", i, v, wantMissing[i])
		}
	}
}

// TestAllChunksUploaded 验证全部上传后 missing 为空。
func TestAllChunksUploaded(t *testing.T) {
	totalChunks := 10
	bitmap := ensureBitmapLen(nil, totalChunks)
	for i := 0; i < totalChunks; i++ {
		setBit(bitmap, i)
	}

	var missing []int
	for i := 0; i < totalChunks; i++ {
		if !getBit(bitmap, i) {
			missing = append(missing, i)
		}
	}
	if len(missing) != 0 {
		t.Errorf("全部上传后 missing 应为空, got %v", missing)
	}
}

// TestNoChunksUploaded 验证未上传任何块时 missing 包含全部索引。
func TestNoChunksUploaded(t *testing.T) {
	totalChunks := 8
	bitmap := ensureBitmapLen(nil, totalChunks)

	var missing []int
	for i := 0; i < totalChunks; i++ {
		if !getBit(bitmap, i) {
			missing = append(missing, i)
		}
	}
	if len(missing) != totalChunks {
		t.Errorf("未上传时 missing 应包含全部 %d 块, got %d", totalChunks, len(missing))
	}
}
