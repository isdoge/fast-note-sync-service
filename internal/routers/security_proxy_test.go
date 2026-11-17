package routers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/haierkeys/fast-note-sync-service/internal/middleware"
	"github.com/haierkeys/fast-note-sync-service/pkg/limiter"
)

func TestClientIPTrustsOnlyConfiguredProxy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newRouterEngine([]string{"192.0.2.10/32", "127.0.0.1/32"})
	r.GET("/probe", func(c *gin.Context) { c.String(http.StatusOK, c.ClientIP()) })
	for _, test := range []struct{ remote, forwarded, expected string }{
		{"192.0.2.1:1234", "198.51.100.2", "192.0.2.1"},
		{"192.0.2.10:1234", "198.51.100.2", "198.51.100.2"},
		{"192.0.2.11:1234", "198.51.100.2", "192.0.2.11"},
	} {
		request := httptest.NewRequest(http.MethodGet, "/probe", nil)
		request.RemoteAddr = test.remote
		request.Header.Set("X-Forwarded-For", test.forwarded)
		response := httptest.NewRecorder()
		r.ServeHTTP(response, request)
		if response.Body.String() != test.expected {
			t.Fatalf("remote %s: got %s, want %s", test.remote, response.Body.String(), test.expected)
		}
	}
}

// 登录接口全局兜底桶：大量不同来源各自未超单客户端限额时，总量超过全局容量后被拒绝。
func TestLoginGlobalLimiterCapsDistributedAttempts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	_ = r.SetTrustedProxies(nil)
	perClient := limiter.NewMethodLimiter().AddBuckets(limiter.BucketRule{Key: "/api/user/login", PerClient: true, FillInterval: time.Hour, Capacity: 5, Quantum: 1})
	global := limiter.NewMethodLimiter().AddBuckets(limiter.BucketRule{Key: "/api/user/login", FillInterval: time.Hour, Capacity: 50, Quantum: 50})
	r.Use(middleware.RateLimiter(perClient), middleware.RateLimiter(global))
	r.POST("/api/user/login", func(c *gin.Context) { c.Status(http.StatusUnauthorized) })

	rejected := 0
	for i := 0; i < 60; i++ {
		request := httptest.NewRequest(http.MethodPost, "/api/user/login", nil)
		request.RemoteAddr = fmt.Sprintf("[2001:db8:%x::1]:1000", i)
		response := httptest.NewRecorder()
		r.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			rejected++
		}
	}
	if rejected != 10 {
		t.Fatalf("expected 10 requests rejected by global bucket, got %d", rejected)
	}

	// 生产配置：登录接口必须同时存在单客户端桶与全局桶
	if _, ok := globalLimiters.GetBucket("/api/user/login"); !ok {
		t.Fatal("global login bucket missing")
	}
}
