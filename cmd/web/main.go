package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"ai-case-be/config"
	"ai-case-be/database"
	"ai-case-be/handlers"
	"ai-case-be/logger"
	"ai-case-be/middleware"
)

// main 是 Web 服务进程的启动入口。
//
// 启动顺序固定为：加载配置、初始化日志、建立数据库连接、注册 Gin 路由，
// 最后启动 HTTP 服务并监听退出信号，确保服务可以优雅关闭。
func main() {
	// 加载配置文件
	cfg, err := config.LoadConfig()
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}
	fmt.Printf("Current cfg:\n%+v\n", cfg)

	// 初始化日志系统
	if err := logger.InitLogger(cfg.App.Env); err != nil {
		fmt.Printf("Failed to initialize logger: %v\n", err)
		os.Exit(1)
	}
	defer logger.Sync()

	//数据库连接
	database.Connect(cfg)

	// 创建 Gin 引擎（不使用默认中间件）
	r := gin.New()
	r.Use(middleware.Recovery())
	r.Use(middleware.RequestID())
	r.Use(middleware.ZapLogger())

	apiV1 := r.Group("/api/v1")
	{

		// AI 助手
		apiV1.POST("/ai/chat", handlers.AiChat)
		apiV1.GET("/ai-test-cases", handlers.AiTestCaseList)
		apiV1.POST("/ai-test-cases/generate", handlers.GenerateAiTestCase)

	}

	// 配置服务器
	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.Server.Port),
		Handler: r,
	}

	// 在一个新的goroutine中启动服务器，以便可以进行关闭操作
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %s\n", err)
		}
	}()

	// 监听系统信号以实现优雅关闭
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("Shutting down server...")

	// 最大时间控制，用于通知该服务端它有5秒的时间来处理原有请求
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Fatal("Server forced to shutdown: ", err)
	}

	log.Println("Server exiting")
}

// JSONLogger 是一个自定义中间件，用于输出 JSON 格式的日志
func JSONLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 记录请求开始时间
		startTime := time.Now()

		// 处理请求
		c.Next()

		// 计算处理时间
		latency := time.Since(startTime)

		// 获取客户端真实IP
		clientIP := c.Request.Header.Get("X-Forwarded-For")
		if clientIP == "" {
			clientIP = c.ClientIP()
		}

		// 创建一个日志条目信息
		logEntry := map[string]interface{}{
			"time":      time.Now().Format(time.RFC3339),
			"method":    c.Request.Method,
			"path":      c.Request.URL.Path,
			"status":    c.Writer.Status(),
			"latency":   latency.String(),
			"clientIP":  clientIP,
			"userAgent": c.Request.UserAgent(),
		}

		// 将日志条目转换为 JSON 格式
		if jsonLog, err := json.Marshal(logEntry); err == nil {
			log.SetOutput(os.Stdout)
			log.Println(string(jsonLog)) // 输出 JSON 字符串到日志
		} else {
			log.Printf("Failed to create JSON log: %v", err)
		}
	}
}
