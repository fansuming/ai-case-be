package aichat

import "github.com/gin-gonic/gin"

// pageAnalysisRoleHandler 处理“围绕当前页面数据做分析”的请求。
type pageAnalysisRoleHandler struct{}

// Name 返回页面分析角色处理器的名称。
func (pageAnalysisRoleHandler) Name() string { return "page_analysis" }

// CanHandle 判断当前 handler 是否负责页面分析意图。
func (pageAnalysisRoleHandler) CanHandle(intent chatIntent) bool {
	return intent.Type == chatIntentPageAnalysis
}

// Handle 调用页面分析专用聊天链路。
func (pageAnalysisRoleHandler) Handle(c *gin.Context, chatCtx handlerContext) error {
	return streamPageAnalysisChat(c, chatCtx.Request, chatCtx.Messages)
}

// streamPageAnalysisChat 复用统一聊天主链路，但注入页面分析专用 prompt。
func streamPageAnalysisChat(c *gin.Context, req Request, filteredMessages []Message) error {
	return streamChatWithPrompt(c, req, filteredMessages, buildPageAnalysisSystemPrompt, retrievePageAnalysisKnowledge)
}

// buildPageAnalysisSystemPrompt 构造页面分析角色使用的专用 system prompt。
//
// 相比默认角色，它更强调“信号概括 -> 原因判断 -> 风险说明 -> 排查建议”的报告式输出，
// 并弱化通用背景介绍，保证回答更贴近页面分析场景。
func buildPageAnalysisSystemPrompt(req Request, filteredMessages []Message, retrievedDocs []knowledgeDocument) string {
	base := `你是一位专注于页面数据分析的代码质量分析专家，服务于企业级数据研效监控平台。

你的任务不是泛泛介绍平台能力，而是围绕用户当前页面的数据做定向分析。请使用中文回答，并严格遵循以下要求：
1. 先概括当前页面最值得关注的 1 到 3 个信号，优先引用页面摘要中的事实；
2. 再解释这些信号可能代表的原因、风险和判断依据；
3. 最后给出可执行的排查建议或后续查看路径；
4. 如果页面摘要没有提供足够事实，就明确说明信息不足，不要编造具体数值；
5. 回答尽量贴近“分析报告”风格，少做泛化背景介绍。

当提及系统中的具体功能或页面时，请使用 Markdown 链接格式 [功能名称](/路径) 来引用。`

	if req.Context.Page != "" {
		base += "\n\n当前用户所在页面路径：" + req.Context.Page
	}
	if req.Context.Summary != "" {
		base += "\n当前页面数据摘要：" + req.Context.Summary
	}

	latestUserMessage := getLatestUserMessage(filteredMessages)
	if latestUserMessage != "" {
		base += "\n当前用户问题：" + latestUserMessage
	}

	if len(retrievedDocs) > 0 {
		base += "\n\n可参考的平台知识片段："
		for index, doc := range retrievedDocs {
			base += formatKnowledgeDocument(index, doc)
		}
	}

	return base
}
