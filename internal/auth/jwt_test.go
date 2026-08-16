package auth

import (
	"testing"
	"time"
)

// TestJWTIssueParseRoundTrip 验证签发 → 解析的闭环，以及 claims 字段正确回填。
func TestJWTIssueParseRoundTrip(t *testing.T) {
	mgr := New("test-secret-change-me", 120, 30, "nimbusdrive-test")

	token, err := mgr.IssueAccessToken(42, "alice", true)
	if err != nil {
		t.Fatalf("IssueAccessToken: %v", err)
	}
	if token == "" {
		t.Fatal("empty token")
	}

	claims, err := mgr.Parse(token)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if claims.UserID != 42 {
		t.Errorf("UserID = %d, want 42", claims.UserID)
	}
	if claims.Username != "alice" {
		t.Errorf("Username = %q, want alice", claims.Username)
	}
	if !claims.IsAdmin {
		t.Error("IsAdmin = false, want true")
	}
	if claims.Issuer != "nimbusdrive-test" {
		t.Errorf("Issuer = %q, want nimbusdrive-test", claims.Issuer)
	}
	if claims.ExpiresAt == nil {
		t.Fatal("ExpiresAt nil")
	}
	// 过期时间应在未来（约 120 分钟后）
	if claims.ExpiresAt.Time.Before(time.Now()) {
		t.Error("token already expired")
	}
}

// TestJWTParseInvalid 验证无效 token 与错误密钥被拒绝。
func TestJWTParseInvalid(t *testing.T) {
	mgr := New("correct-secret", 120, 30, "issuer")

	if _, err := mgr.Parse("not-a-jwt"); err == nil {
		t.Error("expected error for malformed token")
	}

	// 用另一个密钥签发的 token 应解析失败
	other := New("wrong-secret", 120, 30, "issuer")
	token, _ := other.IssueAccessToken(1, "bob", false)
	if _, err := mgr.Parse(token); err == nil {
		t.Error("expected error for token signed with wrong secret")
	}
}

// TestExtractBearer 验证 Bearer 头解析。
func TestExtractBearer(t *testing.T) {
	cases := []struct {
		header string
		want   string
		ok     bool
	}{
		{"Bearer abc123", "abc123", true},
		{"bearer abc123", "", false},  // 大小写敏感
		{"abc123", "", false},
		{"", "", false},
		{"Bearer ", "", false},
	}
	for _, c := range cases {
		got, ok := ExtractBearer(c.header)
		if ok != c.ok || got != c.want {
			t.Errorf("ExtractBearer(%q) = (%q,%v), want (%q,%v)", c.header, got, ok, c.want, c.ok)
		}
	}
}
