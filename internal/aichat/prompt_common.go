package aichat

import (
	"fmt"
	"sort"
	"strings"
)

// knowledgeDocument 表示可被检索注入 prompt 的平台知识片段。
//
// 这不是实时业务数据，而是平台页面说明、指标解释和分析口径的轻量语料。
type knowledgeDocument struct {
	ID       string
	Title    string
	Route    string
	Content  string
	Keywords []string
	Roles    []chatIntentType
}

// formatKnowledgeDocument 把知识片段格式化为适合直接拼进 prompt 的文本段落。
func formatKnowledgeDocument(index int, doc knowledgeDocument) string {
	return fmt.Sprintf("\n%d. %s（路径：%s）：%s", index+1, doc.Title, doc.Route, doc.Content)
}

// retrieveDefaultKnowledge 是默认聊天角色使用的检索策略。
//
// 默认聊天并不总是需要平台知识：
// - 若当前有页面路径或页面摘要，说明用户很可能在结合当前页面提问，应执行检索；
// - 若没有页面上下文，则只有在用户问题明显指向平台能力或指标时，才补充 RAG；
// - 对明显的通用闲聊或泛问答，直接不注入平台知识，避免默认助手被过度“分析化”。
func retrieveDefaultKnowledge(req Request, messages []Message) []knowledgeDocument {
	if strings.TrimSpace(req.Context.Page) != "" || strings.TrimSpace(req.Context.Summary) != "" {
		return retrieveKnowledgeForRole(chatIntentDefaultChat, req.Context.Page, req.Context.Summary, messages)
	}

	latestUserMessage := normalizeText(getLatestUserMessage(messages))
	if latestUserMessage == "" {
		return nil
	}

	platformKeywords := []string{
		"mr", "crmr", "代码质量", "缺陷密度", "bug", "corax", "静态检查", "单测", "覆盖率",
		"coredump", "hotfix", "版本说明", "审批", "责任田", "平台", "页面", "代码评审",
	}
	for _, keyword := range platformKeywords {
		if strings.Contains(latestUserMessage, normalizeText(keyword)) {
			return retrieveKnowledgeForRole(chatIntentDefaultChat, req.Context.Page, req.Context.Summary, messages)
		}
	}

	return nil
}

// retrievePageAnalysisKnowledge 是页面分析角色使用的检索策略。
//
// 页面分析回答天然依赖平台口径和页面能力说明，因此只要进入该角色，
// 就应该稳定执行检索，并优先返回适用于页面分析的知识片段。
func retrievePageAnalysisKnowledge(req Request, messages []Message) []knowledgeDocument {
	return retrieveKnowledgeForRole(chatIntentPageAnalysis, req.Context.Page, req.Context.Summary, messages)
}

// retrieveKnowledgeForRole 根据角色、页面、摘要和最后一条用户消息，从内置语料中召回最相关的知识片段。
//
// 当前实现是轻量级、可解释的规则检索：
// - 先把检索输入归一化；
// - 再按角色过滤不适用的知识片段；
// - 再按路由、关键词、标题和内容做加权打分；
// - 最终只返回 TopN 文档注入 prompt。
func retrieveKnowledgeForRole(role chatIntentType, page, summary string, messages []Message) []knowledgeDocument {
	latestUserMessage := getLatestUserMessage(messages)
	if page == "" && summary == "" && latestUserMessage == "" {
		return nil
	}

	searchText := normalizeText(strings.Join([]string{page, summary, latestUserMessage}, " "))
	if searchText == "" {
		return nil
	}

	corpus := []knowledgeDocument{
		{ID: "mr-overview", Title: "代码合并与评审统计", Route: "/code/mr", Content: "适合回答 MR 通过率、评论数、审核参与率、百行评论数等问题。分析时通常需要结合时间范围、仓库或责任田维度，避免脱离筛选条件直接比较。", Keywords: []string{"mr", "代码评审", "评论数", "通过率", "审核参与率", "百行评论数", "merge request"}, Roles: []chatIntentType{chatIntentDefaultChat, chatIntentPageAnalysis}},
		{ID: "crmr-overview", Title: "CRMR 指标", Route: "/code/crmr", Content: "CRMR 是综合代码质量指标，适合做横向对比和趋势观察。回答时应优先解释波动趋势、异常仓库或团队，再给出质量改进建议。", Keywords: []string{"crmr", "综合代码质量", "代码质量", "质量趋势"}, Roles: []chatIntentType{chatIntentDefaultChat, chatIntentPageAnalysis}},
	}

	type scoredDocument struct {
		document knowledgeDocument
		score    int
	}

	scored := make([]scoredDocument, 0, len(corpus))
	for _, doc := range corpus {
		if !supportsKnowledgeRole(doc, role) {
			continue
		}
		score := scoreKnowledgeDocument(doc, page, searchText)
		if score <= 0 {
			continue
		}
		scored = append(scored, scoredDocument{document: doc, score: score})
	}

	if len(scored) == 0 {
		return nil
	}

	sort.SliceStable(scored, func(i, j int) bool { return scored[i].score > scored[j].score })

	limit := 3
	if len(scored) < limit {
		limit = len(scored)
	}

	docs := make([]knowledgeDocument, 0, limit)
	for _, item := range scored[:limit] {
		docs = append(docs, item.document)
	}

	return docs
}

// supportsKnowledgeRole 判断当前知识片段是否适用于给定角色。
//
// 目前采用白名单策略：
// - Roles 为空时，视为所有角色都可用；
// - Roles 非空时，只有显式列出的角色可以命中。
func supportsKnowledgeRole(doc knowledgeDocument, role chatIntentType) bool {
	if len(doc.Roles) == 0 {
		return true
	}
	for _, supportedRole := range doc.Roles {
		if supportedRole == role {
			return true
		}
	}
	return false
}

// scoreKnowledgeDocument 为单条知识片段计算相关性分数。
//
// 路由命中权重最高，关键词次之，标题和内容命中作为辅助信号。
func scoreKnowledgeDocument(doc knowledgeDocument, page, searchText string) int {
	score := 0
	normalizedRoute := normalizeText(doc.Route)
	normalizedPage := normalizeText(page)
	if normalizedPage != "" {
		if normalizedPage == normalizedRoute {
			score += 8
		} else if strings.Contains(normalizedPage, normalizedRoute) || strings.Contains(normalizedRoute, normalizedPage) {
			score += 4
		}
	}

	for _, keyword := range doc.Keywords {
		normalizedKeyword := normalizeText(keyword)
		if normalizedKeyword != "" && strings.Contains(searchText, normalizedKeyword) {
			score += 3
		}
	}

	if strings.Contains(searchText, normalizeText(doc.Title)) {
		score += 2
	}
	if strings.Contains(searchText, normalizeText(doc.Content)) {
		score += 1
	}

	return score
}

// getLatestUserMessage 从后往前扫描消息列表，返回最近一条 user 消息。
func getLatestUserMessage(messages []Message) string {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == "user" {
			return messages[index].Content
		}
	}
	return ""
}

// normalizeText 将自然语言与知识字段归一化到可做 contains 匹配的统一文本形态。
func normalizeText(input string) string {
	replacer := strings.NewReplacer("\n", " ", "\t", " ", "/", " ", "-", " ", "_", " ", "（", " ", "）", " ", "(", " ", ")", " ", "：", " ", ":", " ", "，", " ", ",", " ")
	return strings.ToLower(strings.Join(strings.Fields(replacer.Replace(input)), " "))
}

// normalizeSpace 对文本做基础 trim，避免空白噪声污染消息内容。
func normalizeSpace(input string) string {
	return strings.TrimSpace(input)
}
