package quota

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Demetrius2107/NimbusDrive/internal/domain"
	"github.com/Demetrius2107/NimbusDrive/internal/middleware"
	"github.com/gin-gonic/gin"
)

// TestSSEHandler_NilNotifier_503 验证 Redis 不可用时 SSE 端点返回 503，
// 而非挂起长连接。前端据此降级为轮询。
func TestSSEHandler_NilNotifier_503(t *testing.T) {
	gin.SetMode(gin.TestMode)
	// nil notifier + nil repos：触发降级路径。
	h := NewSSEHandler(nil, nil, nil)

	r := gin.New()
	// 注入 user_id，模拟 JWT 中间件已解析。
	r.Use(func(c *gin.Context) {
		c.Set(middleware.CtxUserID, int64(1))
		c.Next()
	})
	r.GET("/quota/stream", h.Stream)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/quota/stream", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil notifier should return 503, got %d", w.Code)
	}
	if want := string(domain.CodeInternal); !contains(w.Body.String(), want) {
		t.Errorf("response body should contain code %q, got %s", want, w.Body.String())
	}
}

// TestParseVersion 验证 Last-Event-ID 解析。
func TestParseVersion(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"1", 1, true},
		{"42", 42, true},
		{"0", 0, false}, // 0 不补发
		{"", 0, false},
		{"abc", 0, false},
	}
	for _, c := range cases {
		got, err := parseVersion(c.in)
		if c.ok {
			if err != nil || got != c.want {
				t.Errorf("parseVersion(%q) = %d, %v; want %d, nil", c.in, got, err, c.want)
			}
		} else {
			if err == nil && got > 0 {
				t.Errorf("parseVersion(%q) = %d, nil; want error or <=0", c.in, got)
			}
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
