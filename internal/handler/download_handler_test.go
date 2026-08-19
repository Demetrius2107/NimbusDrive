package handler

import (
	"testing"

	"github.com/Demetrius2107/NimbusDrive/internal/storage"
)

// TestParseRangeFromStart 验证 bytes=0- 格式（从 start 到末尾）。
func TestParseRangeFromStart(t *testing.T) {
	start, end, ok := parseRange("bytes=0-", 1000)
	if !ok || start != 0 || end != 999 {
		t.Errorf("bytes=0- on 1000: got (%d,%d,%v), want (0,999,true)", start, end, ok)
	}
}

// TestParseRangeMiddle 验证 bytes=100-199 格式（指定范围）。
func TestParseRangeMiddle(t *testing.T) {
	start, end, ok := parseRange("bytes=100-199", 1000)
	if !ok || start != 100 || end != 199 {
		t.Errorf("bytes=100-199 on 1000: got (%d,%d,%v), want (100,199,true)", start, end, ok)
	}
}

// TestParseRangeSuffix 验证 bytes=-99 格式（最后 99 字节）。
func TestParseRangeSuffix(t *testing.T) {
	start, end, ok := parseRange("bytes=-99", 1000)
	if !ok || start != 901 || end != 999 {
		t.Errorf("bytes=-99 on 1000: got (%d,%d,%v), want (901,999,true)", start, end, ok)
	}
}

// TestParseRangeSuffixLargerThanFile 验证后缀长度超过文件大小时截断。
func TestParseRangeSuffixLargerThanFile(t *testing.T) {
	start, end, ok := parseRange("bytes=-9999", 100)
	if !ok || start != 0 || end != 99 {
		t.Errorf("bytes=-9999 on 100: got (%d,%d,%v), want (0,99,true)", start, end, ok)
	}
}

// TestParseRangeEndBeyondFile 验证 end 超过文件末尾时截断。
func TestParseRangeEndBeyondFile(t *testing.T) {
	start, end, ok := parseRange("bytes=50-9999", 100)
	if !ok || start != 50 || end != 99 {
		t.Errorf("bytes=50-9999 on 100: got (%d,%d,%v), want (50,99,true)", start, end, ok)
	}
}

// TestParseRangeMultiRange 验证多段 Range 不支持，返回 ok=false。
func TestParseRangeMultiRange(t *testing.T) {
	_, _, ok := parseRange("bytes=0-99,200-", 1000)
	if ok {
		t.Error("多段 Range 应返回 ok=false")
	}
}

// TestParseRangeInvalid 验证各种非法格式安全降级。
func TestParseRangeInvalid(t *testing.T) {
	cases := []string{
		"",           // 空头
		"items=0-99", // 错误前缀
		"bytes=abc",  // 非数字
		"bytes=100-", // start >= totalSize
		"bytes=100-50", // end < start
	}
	for _, c := range cases {
		_, _, ok := parseRange(c, 100)
		if ok {
			t.Errorf("非法格式 %q 应返回 ok=false", c)
		}
	}
}

// TestParseRangeZeroSize 验证 totalSize <= 0 时返回 false。
func TestParseRangeZeroSize(t *testing.T) {
	_, _, ok := parseRange("bytes=0-", 0)
	if ok {
		t.Error("totalSize=0 应返回 ok=false")
	}
}

// TestBuildContentDispositionASCII 验证 ASCII 文件名用 filename="..."。
func TestBuildContentDispositionASCII(t *testing.T) {
	got := storage.BuildContentDisposition("report.pdf")
	want := `attachment; filename="report.pdf"`
	if got != want {
		t.Errorf("ASCII: got %q, want %q", got, want)
	}
}

// TestBuildContentDispositionNonASCII 验证非 ASCII 文件名用 RFC 5987 编码。
func TestBuildContentDispositionNonASCII(t *testing.T) {
	got := storage.BuildContentDisposition("报告.pdf")
	// 应包含 filename*=UTF-8'' 前缀
	prefix := `attachment; filename*=UTF-8''`
	if len(got) <= len(prefix) || got[:len(prefix)] != prefix {
		t.Errorf("非 ASCII: got %q, 应以 %q 开头", got, prefix)
	}
	// 不应包含原始中文
	if contains(got, "报告") {
		t.Errorf("非 ASCII 文件名应被编码, got %q", got)
	}
}

// TestIsASCII 验证 ASCII 判断。
func TestIsASCII(t *testing.T) {
	if !storage.IsASCII("hello.txt") {
		t.Error("纯 ASCII 应返回 true")
	}
	if storage.IsASCII("报告.txt") {
		t.Error("含中文应返回 false")
	}
	if !storage.IsASCII("") {
		t.Error("空字符串应返回 true")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
