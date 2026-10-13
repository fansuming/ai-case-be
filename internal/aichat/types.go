package aichat

import (
	"errors"
	"fmt"
)

// ---- 公共请求/消息模型 ----

// Message 表示聊天会话中的单条消息。
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// RequestContext 携带前端当前页面的上下文信息，用于 RAG 检索和 prompt 构造。
type RequestContext struct {
	Page    string `json:"page"`
	Summary string `json:"summary"`
}

// Request 是前端发往 /ai/chat 接口的请求体结构。
//
// 字段对应关系：
//   - Messages：聊天历史，前端透传；
//   - Provider：指定使用的上游模型（deepseek/claude/codex），空则由后端选默认；
//   - Context：前端当前页面上下文，用于页面分析角色和 RAG；
//   - RequestID：由中间件注入，用于全链路追踪；
//   - Permissions：来自请求头，表达当前用户拥有的功能权限点；
//   - EnableTools：前端可显式开关工具调用能力；
//   - AllowWriteTools：前端可显式允许/禁止写入类工具。
type Request struct {
	Messages        []Message      `json:"messages"`
	Provider        string         `json:"provider"`
	Context         RequestContext `json:"context"`
	RequestID       string         `json:"-"`
	Permissions     []string       `json:"-"`
	EnableTools     *bool          `json:"enableTools"`
	AllowWriteTools *bool          `json:"allowWriteTools"`
}

// ---- 意图与分发上下文 ----

// chatIntentType 是聊天意图的枚举类型。
type chatIntentType string

const (
	// chatIntentDefaultChat 兜底的通用聊天意图。
	chatIntentDefaultChat chatIntentType = "default_chat"
	// chatIntentPageAnalysis 围绕当前页面数据做分析的意图。
	chatIntentPageAnalysis chatIntentType = "page_analysis"
	// chatIntentCreateTestCase 请求创建测试用例的意图。
	chatIntentCreateTestCase chatIntentType = "create_test_case"
	// chatIntentShowTestCaseHelp 询问测试用例功能用法的意图。
	chatIntentShowTestCaseHelp chatIntentType = "show_test_case_help"
)

// chatIntent 表示一次请求被识别出的意图，携带意图调度所需的附加数据。
type chatIntent struct {
	Type          chatIntentType
	TestCaseTitle string // 仅在 chatIntentCreateTestCase 时有值
}

// handlerContext 是贯穿聊天分发链路的请求级上下文。
//
// 它把经过清洗的请求、消息列表和已识别的意图收敛到一起，
// 供每个 roleHandler 直接取用，无需再从原始 gin.Context 中二次解析。
type handlerContext struct {
	Request  Request
	Messages []Message
	Intent   chatIntent
}

// ---- 错误类型 ----

// ErrInvalidRequest 表示由客户端输入导致的请求错误（对应 HTTP 400）。
var ErrInvalidRequest = errors.New("invalid request")

// ErrInternalRequest 表示后端内部处理异常（对应 HTTP 500）。
var ErrInternalRequest = errors.New("internal error")

// HTTPError 携带上游返回的明确 HTTP 状态码，便于 handler 层按原状态码透传响应。
type HTTPError struct {
	StatusCode int
	Message    string
}

// Error 实现 error 接口。
func (e *HTTPError) Error() string {
	return fmt.Sprintf("http %d: %s", e.StatusCode, e.Message)
}
