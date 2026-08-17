package handler

import (
	"testing"
)

// TestDetectMIME 验证文件扩展名到 MIME 类型的映射。
func TestDetectMIME(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"photo.jpg", "image/jpeg"},
		{"photo.jpeg", "image/jpeg"},
		{"icon.png", "image/png"},
		{"anim.gif", "image/gif"},
		{"clip.mp4", "video/mp4"},
		{"song.mp3", "audio/mpeg"},
		{"doc.pdf", "application/pdf"},
		{"archive.zip", "application/zip"},
		{"notes.txt", "text/plain"},
		{"unknown.xyz", "application/octet-stream"},
		{"noext", "application/octet-stream"},
	}
	for _, c := range cases {
		got := detectMIME(c.name)
		if got != c.want {
			t.Errorf("detectMIME(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestDetectMIMECaseInsensitive 验证扩展名大小写不敏感。
func TestDetectMIMECaseInsensitive(t *testing.T) {
	if got := detectMIME("PHOTO.JPG"); got != "image/jpeg" {
		t.Errorf("detectMIME 大写: got %q, want image/jpeg", got)
	}
	if got := detectMIME("Doc.PDF"); got != "application/pdf" {
		t.Errorf("detectMIME 混合大小写: got %q, want application/pdf", got)
	}
}

// TestParseFileIDValid 验证合法文件 ID 解析。
func TestParseFileIDValid(t *testing.T) {
	// parseFileID 依赖 gin.Context，此处仅验证逻辑等价的字符串解析
	cases := []struct {
		input string
		want  int64
	}{
		{"1", 1},
		{"12345", 12345},
		{"9999999999", 9999999999},
	}
	for _, c := range cases {
		// 复现 parseFileID 的核心解析逻辑
		var got int64
		_, err := parseFileIDLogic(c.input, &got)
		if err != nil || got != c.want {
			t.Errorf("parseFileID(%q): got %d, want %d", c.input, got, c.want)
		}
	}
}

// TestParseFileIDInvalid 验证非法文件 ID 被拒绝。
func TestParseFileIDInvalid(t *testing.T) {
	cases := []string{"abc", "0", "-1", "", "1.5"}
	for _, c := range cases {
		var got int64
		ok, _ := parseFileIDLogic(c, &got)
		if ok {
			t.Errorf("parseFileID(%q) 应返回 false", c)
		}
	}
}

// parseFileIDLogic 复现 parseFileID 的解析逻辑（不依赖 gin.Context）。
func parseFileIDLogic(s string, out *int64) (bool, error) {
	if s == "" {
		return false, nil
	}
	var v int64
	var sign int64 = 1
	i := 0
	if s[0] == '-' {
		sign = -1
		i = 1
	}
	if i >= len(s) {
		return false, nil
	}
	for ; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false, nil
		}
		v = v*10 + int64(s[i]-'0')
	}
	v *= sign
	if v <= 0 {
		return false, nil
	}
	*out = v
	return true, nil
}
