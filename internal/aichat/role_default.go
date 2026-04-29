package aichat

import "github.com/gin-gonic/gin"

// defaultChatRoleHandler 处理所有未命中更具体规则的普通聊天请求。
type defaultChatRoleHandler struct{}

// Name 返回默认聊天角色处理器的名称。
func (defaultChatRoleHandler) Name() string { return "default_chat" }

// CanHandle 判断当前 handler 是否负责默认聊天意图。
func (defaultChatRoleHandler) CanHandle(intent chatIntent) bool {
	return intent.Type == chatIntentDefaultChat
}

// Handle 调用默认聊天链路，并交给统一的 prompt + 上游流式调用逻辑执行。
func (defaultChatRoleHandler) Handle(c *gin.Context, chatCtx handlerContext) error {
	return streamDefaultChat(c, chatCtx.Request, chatCtx.Messages)
}

// streamDefaultChat 是默认角色对应的聊天执行入口。
func streamDefaultChat(c *gin.Context, req Request, filteredMessages []Message) error {
	return streamChatWithPrompt(c, req, filteredMessages, buildDefaultSystemPrompt, retrieveDefaultKnowledge)
}

// buildDefaultSystemPrompt 构造默认聊天角色使用的 system prompt。
//
// 该 prompt 主要聚焦三件事：
// - 将默认角色定义为通用 AI 助手，而非特定分析专家；
// - 如果存在页面上下文，则优先结合页面摘要与检索知识回答；
// - 统一要求模型以中文、专业、简洁的方式输出。
func buildDefaultSystemPrompt(req Request, filteredMessages []Message, retrievedDocs []knowledgeDocument) string {
	base := `你是一个默认的通用 AI 助手，负责回答用户在平台中的各类问题。

请使用中文回答，保持专业、简洁、直接。你的默认职责包括：
1. 优先理解并直接回答用户问题，不要无关扩展；
2. 如果存在“当前页面数据摘要”或页面路径，优先结合这些上下文作答；
3. 如果页面信息不足，再参考“检索增强知识片段”中的平台知识；
4. 如果仍无法确认具体事实、数值、筛选条件或实时状态，要明确说明信息不足，不要编造；
5. 如果用户的问题不依赖当前页面，也可以按通用助手方式直接回答。

当提及系统中的具体功能或页面时，请使用 Markdown 链接格式 [功能名称](/路径) 来引用，便于用户直接跳转。`

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
		base += "\n\n检索增强知识片段："
		for index, doc := range retrievedDocs {
			base += formatKnowledgeDocument(index, doc)
		}
	}

	return base
}

// func buildDefaultSystemPrompt(req Request, filteredMessages []Message, retrievedDocs []knowledgeDocument) string {
// 	_ = filteredMessages
// 	base := `你是一位专业的代码质量分析专家，服务于企业级数据研效监控平台。平台的核心监控能力包括：
// - MR 代码合并与评审统计（路径：/code/mr）：通过率、评论数、审核参与率、百行评论数
// - CRMR 指标（路径：/code/crmr）：综合代码质量指标

// 请使用中文回答，保持专业、简洁。优先基于当前页面数据给出分析和建议。

// 回答时请遵循以下优先级：
// 1. 优先使用“当前页面数据摘要”中的事实进行分析；
// 2. 如果页面摘要不足，再参考“检索增强知识片段”中的平台知识；
// 3. 如果仍无法确认具体数值、筛选条件或实时状态，要明确说明信息不足，不要编造数据；
// 4. 输出建议时，优先给出可执行的排查方向、指标解释和下一步查看路径。

// 当提及系统中的具体功能或页面时，请使用 Markdown 链接格式 [功能名称](/路径) 来引用，例如 [代码合并和评审统计](/code/mr)，这样用户可以直接点击跳转到对应页面。`

// 	if req.Context.Page != "" {
// 		base += "\n\n当前用户所在页面路径：" + req.Context.Page
// 	}
// 	if req.Context.Summary != "" {
// 		base += "\n当前页面数据摘要：" + req.Context.Summary
// 	}
// 	if len(retrievedDocs) > 0 {
// 		base += "\n\n检索增强知识片段："
// 		for index, doc := range retrievedDocs {
// 			base += formatKnowledgeDocument(index, doc)
// 		}
// 	}

// 	return base
// }
