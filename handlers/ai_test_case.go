package handlers

import (
	"strconv"
	"strings"

	"ai-case-be/database"
	"ai-case-be/internal/aichat"

	"github.com/gin-gonic/gin"
)

// generateAiTestCaseRequest 是“同步生成测试用例”接口在 handlers 层的请求体。
//
// 它与聊天窗口里的自然语言触发不同，这里是显式 HTTP 接口：
// 前端直接给出标题，后端调用 aichat 包生成结构化测试用例并落库。
type generateAiTestCaseRequest struct {
	Title    string `json:"title"`
	Provider string `json:"provider"`
}

// AiTestCaseList 查询已落库的 AI 测试用例列表。
//
// 该 handler 的职责仅限于：
// 1. 解析分页参数 page 和 pageSize；
// 2. 读取可选标题筛选条件；
// 3. 调用 database 层查询；
// 4. 通过统一 JsonResponse 返回分页结果。
func AiTestCaseList(c *gin.Context) {
	// 读取分页参数；非法值直接回退到安全默认值，避免把参数错误继续传给查询层。
	page, err := strconv.Atoi(strings.TrimSpace(c.DefaultQuery("page", "1")))
	if err != nil || page <= 0 {
		page = 1
	}

	pageSize, err := strconv.Atoi(strings.TrimSpace(c.DefaultQuery("pageSize", "10")))
	if err != nil || pageSize <= 0 {
		pageSize = 10
	}

	// title 作为可选筛选条件透传到 database 层。
	title := strings.TrimSpace(c.Query("title"))
	items, total, err := database.ListAiTestCases(page, pageSize, title)
	if err != nil {
		JsonResponse(c, FailureCode, "查询测试 case 失败: "+err.Error(), nil)
		return
	}

	JsonResponse(c, SuccessCode, "Success", gin.H{
		"data":     items,
		"total":    total,
		"page":     page,
		"pageSize": pageSize,
	})
}

// GenerateAiTestCase 是“同步生成并保存测试用例”的 HTTP 入口。
//
// 这条链路与聊天触发不同，适合页面上“输入标题后直接点生成”的交互：
// - handler 负责参数绑定与基础校验；
// - aichat 包负责调用模型生成测试用例并写入数据库；
// - handlers 层只保留响应映射和错误输出。
func GenerateAiTestCase(c *gin.Context) {
	var req generateAiTestCaseRequest
	// 请求结构不合法时立即返回，避免进入业务层。
	if err := c.ShouldBindJSON(&req); err != nil {
		JsonResponse(c, FailureCode, "请求参数不正确", nil)
		return
	}

	// 标题是生成测试用例的最小必要输入，没有标题就没有生成意义。
	title := strings.TrimSpace(req.Title)
	if title == "" {
		JsonResponse(c, FailureCode, "标题不能为空", nil)
		return
	}

	// 真正的 AI 生成与落库逻辑复用 aichat 包的公共业务入口。
	item, err := aichat.CreateTestCaseRecord(title, req.Provider, "root")
	if err != nil {
		JsonResponse(c, FailureCode, err.Error(), nil)
		return
	}

	JsonResponse(c, SuccessCode, "Success", item)
}
