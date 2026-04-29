package middleware

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const requestIDContextKey = "requestId"

// GetRequestID 从 Gin 上下文中读取当前请求的 requestId。
func GetRequestID(c *gin.Context) string {
	if c == nil {
		return ""
	}
	requestID, exists := c.Get(requestIDContextKey)
	if !exists {
		return ""
	}
	value, ok := requestID.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

// RequestID 为每个请求注入稳定的 requestId，并透传到响应头。
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID := strings.TrimSpace(c.GetHeader("X-Request-Id"))
		if requestID == "" {
			requestID = uuid.NewString()
		}

		c.Set(requestIDContextKey, requestID)
		c.Header("X-Request-Id", requestID)
		c.Next()
	}
}
