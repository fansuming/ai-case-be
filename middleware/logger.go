package middleware

import (
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"ai-case-be/logger"
)

// ZapLogger 是一个 Gin 中间件，使用 Zap 记录请求日志
func ZapLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 记录请求开始时间
		start := time.Now()

		// 处理请求
		path := c.Request.URL.Path
		query := c.Request.URL.RawQuery
		c.Next()

		// 计算处理时间
		latency := time.Since(start)

		// 获取客户端真实IP
		clientIP := c.Request.Header.Get("X-Forwarded-For")
		if clientIP == "" {
			clientIP = c.ClientIP()
		}

		// 记录日志
		fields := []zap.Field{
			zap.String("requestId", GetRequestID(c)),
			zap.Int("status", c.Writer.Status()),
			zap.String("method", c.Request.Method),
			zap.String("path", path),
			zap.String("query", query),
			zap.String("ip", clientIP),
			zap.String("user-agent", c.Request.UserAgent()),
			zap.Duration("latency", latency),
			zap.String("error", c.Errors.ByType(gin.ErrorTypePrivate).String()),
		}

		// 根据状态码选择日志级别
		if c.Writer.Status() >= 500 {
			logger.Error("Server error", fields...)
		} else if c.Writer.Status() >= 400 {
			logger.Warn("Client error", fields...)
		} else {
			logger.Info("Request completed", fields...)
		}
	}
}

// Recovery 是一个 Gin 中间件，用于捕获 panic 并记录日志
func Recovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if err := recover(); err != nil {
				logger.Error("Panic recovered",
					zap.String("requestId", GetRequestID(c)),
					zap.Any("error", err),
					zap.String("path", c.Request.URL.Path),
					zap.String("method", c.Request.Method),
					zap.String("ip", c.ClientIP()),
				)
				c.AbortWithStatus(500)
			}
		}()
		c.Next()
	}
}
