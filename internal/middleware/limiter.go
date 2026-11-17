package middleware

import (
	"github.com/haierkeys/fast-note-sync-service/pkg/app"
	"github.com/haierkeys/fast-note-sync-service/pkg/code"
	"github.com/haierkeys/fast-note-sync-service/pkg/limiter"

	"github.com/gin-gonic/gin"
)

// RateLimiter creates rate limiting middleware (supports dependency injection)
// RateLimiter 创建限流中间件（支持依赖注入）
func RateLimiter(l limiter.Face) gin.HandlerFunc {
	return func(c *gin.Context) {
		key := l.Key(c)
		if bucket, ok := l.GetBucket(key); ok {
			count := bucket.TakeAvailable(1)
			if count == 0 {
				response := app.NewResponse(c)
				response.ToResponse(code.ErrorTooManyRequests)
				c.Abort()
				return
			}
		}

		c.Next()
	}
}

// LoginSucceededKey 登录处理器在登录成功时置位，FailureRateLimiter 据此不消耗额度
const LoginSucceededKey = "login_succeeded"

// FailureRateLimiter 只按失败请求消耗额度：额度耗尽时拒绝请求；处理器置位 LoginSucceededKey 的成功请求不消耗额度。
// 并发请求在额度检查与扣减之间存在少量超发，作为全局兜底可接受。
func FailureRateLimiter(l limiter.Face) gin.HandlerFunc {
	return func(c *gin.Context) {
		bucket, ok := l.GetBucket(l.Key(c))
		if !ok {
			c.Next()
			return
		}
		if bucket.Available() <= 0 {
			response := app.NewResponse(c)
			response.ToResponse(code.ErrorTooManyRequests)
			c.Abort()
			return
		}

		c.Next()

		if !c.GetBool(LoginSucceededKey) {
			bucket.TakeAvailable(1)
		}
	}
}
