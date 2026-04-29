package handlers

import (
	"errors"
	"net/http"
	"strings"

	"ai-case-be/internal/aichat"

	"github.com/gin-gonic/gin"
)

// AiChat 是 AI 聊天接口在 handlers 层的 HTTP 入口。
//
// 这里故意不承载实际的 AI 业务逻辑，而只负责三件事：
// 1. 把前端 JSON 请求体绑定到 aichat.Request；
// 2. 调用 internal/aichat 执行真正的消息清洗、角色分发和上游模型请求；
// 3. 把业务层返回的不同错误类型映射为最终 HTTP 响应。
//
// 这种“薄 handler”设计的价值在于：
// - 网络入口层保持简单稳定；
// - AI 能力可在独立包中持续扩展；
// - 错误码策略依然集中在 handlers 层统一控制。
func AiChat(c *gin.Context) {
	var req aichat.Request
	// 先完成请求体绑定；如果 JSON 结构不合法，直接返回 400。
	if err := c.ShouldBindJSON(&req); err != nil {
		_ = c.Error(err)
		c.JSON(http.StatusBadRequest, gin.H{"code": 1, "msg": "invalid request body"})
		return
	}

	if requestID, exists := c.Get("requestId"); exists {
		if id, ok := requestID.(string); ok {
			req.RequestID = id
		}
	}
	req.Permissions = parsePermissionHeader(c.GetHeader("X-User-Permissions"))

	// 真正的 AI 业务处理交给 aichat 包，这里只做错误分类和响应转换。
	if err := aichat.HandleChat(c, req); err != nil {
		_ = c.Error(err)
		// 请求参数或业务前置条件错误，按 400 返回。
		if errors.Is(err, aichat.ErrInvalidRequest) {
			c.JSON(http.StatusBadRequest, gin.H{"code": 1, "msg": err.Error()})
			return
		}

		// 上游模型已经给出明确状态码时，优先透传该状态码。
		var httpErr *aichat.HTTPError
		if errors.As(err, &httpErr) {
			c.JSON(httpErr.StatusCode, gin.H{"code": 1, "msg": httpErr.Message})
			return
		}

		// 其余错误按网关错误处理；若是内部流程异常，则提升为 500。
		statusCode := http.StatusBadGateway
		if errors.Is(err, aichat.ErrInternalRequest) {
			statusCode = http.StatusInternalServerError
		}
		c.JSON(statusCode, gin.H{"code": 1, "msg": err.Error()})
	}
}

// parsePermissionHeader 解析权限请求头并输出去重后的权限列表。
//
// 该函数兼容逗号、分号、竖线和空格等多种分隔符，
// 便于后端对接不同网关或上游鉴权系统传下来的权限格式。
func parsePermissionHeader(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}

	segments := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '|' || r == ' '
	})
	permissions := make([]string, 0, len(segments))
	seen := make(map[string]struct{}, len(segments))
	for _, segment := range segments {
		permission := strings.TrimSpace(segment)
		if permission == "" {
			continue
		}
		if _, exists := seen[permission]; exists {
			continue
		}
		seen[permission] = struct{}{}
		permissions = append(permissions, permission)
	}

	return permissions
}
