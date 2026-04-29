package aichat

import (
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"
)

// intentMatcher 负责识别“本次请求应该走哪种业务意图”。
//
// 每个 matcher 只专注一种命中规则；返回 matched=true 时，分发链会立即停止继续匹配。
type intentMatcher interface {
	Name() string
	Match(chatCtx handlerContext) (chatIntent, bool)
}

// roleHandler 负责在意图已经确定后真正执行业务逻辑。
//
// matcher 与 handler 分离的目的是让“如何判断”和“如何处理”成为两个可独立扩展的层次。
type roleHandler interface {
	Name() string
	CanHandle(intent chatIntent) bool
	Handle(c *gin.Context, chatCtx handlerContext) error
}

// roleRegistry 保存当前聊天链路可用的 matcher 和 handler 集合。
//
// 它本身不做业务，只负责组织可插拔的识别器和执行器。
type roleRegistry struct {
	intentMatchers []intentMatcher
	roleHandlers   []roleHandler
}

// resolveIntent 按注册顺序依次尝试 matcher，返回第一个命中的意图。
func (registry roleRegistry) resolveIntent(chatCtx handlerContext) (chatIntent, error) {
	for _, matcher := range registry.intentMatchers {
		if intent, matched := matcher.Match(chatCtx); matched {
			return intent, nil
		}
	}
	return chatIntent{}, fmt.Errorf("%w: no ai chat intent matched", ErrInternalRequest)
}

// newRoleRegistry 组装默认的角色注册表。
//
// 顺序很关键：更具体的页面分析、测试用例意图必须排在默认兜底意图之前。
func newRoleRegistry() roleRegistry {
	return roleRegistry{
		intentMatchers: []intentMatcher{pageAnalysisIntentMatcher{}, testCaseIntentMatcher{}, defaultIntentMatcher{}},
		roleHandlers:   []roleHandler{pageAnalysisRoleHandler{}, testCaseRoleHandler{}, defaultChatRoleHandler{}},
	}
}

// dispatchChat 是聊天角色分发链路的入口。
//
// 它先识别意图，再根据意图选择能够处理该请求的角色处理器。
func dispatchChat(c *gin.Context, chatCtx handlerContext) error {
	registry := newRoleRegistry()

	intent, err := registry.resolveIntent(chatCtx)
	if err != nil {
		return err
	}

	chatCtx.Intent = intent
	for _, handler := range registry.roleHandlers {
		if handler.CanHandle(intent) {
			if err := handler.Handle(c, chatCtx); err != nil {
				return fmt.Errorf("%s: %w", handler.Name(), err)
			}
			return nil
		}
	}

	return fmt.Errorf("%w: no ai chat role handler matched for intent %s", ErrInternalRequest, intent.Type)
}

// defaultIntentMatcher 是最终兜底的默认意图识别器。
type defaultIntentMatcher struct{}

// Name 返回默认意图识别器的注册名，便于日志和调试链路定位。
func (defaultIntentMatcher) Name() string { return "default_intent" }

// Match 永远命中默认聊天意图，因此必须注册在 matcher 列表最后。
func (defaultIntentMatcher) Match(chatCtx handlerContext) (chatIntent, bool) {
	return chatIntent{Type: chatIntentDefaultChat}, true
}

// pageAnalysisIntentMatcher 识别“围绕当前页面数据进行分析”的请求。
type pageAnalysisIntentMatcher struct{}

// Name 返回页面分析意图识别器的注册名。
func (pageAnalysisIntentMatcher) Name() string { return "page_analysis_intent" }

// Match 只有在“页面上下文存在”且“用户问题包含分析类关键词”时才命中页面分析意图。
func (pageAnalysisIntentMatcher) Match(chatCtx handlerContext) (chatIntent, bool) {
	if strings.TrimSpace(chatCtx.Request.Context.Page) == "" && strings.TrimSpace(chatCtx.Request.Context.Summary) == "" {
		return chatIntent{}, false
	}

	latestUserMessage := strings.ToLower(strings.TrimSpace(getLatestUserMessage(chatCtx.Messages)))
	if latestUserMessage == "" {
		return chatIntent{}, false
	}

	analysisKeywords := []string{"分析", "解读", "怎么看", "原因", "趋势", "风险", "建议"}
	if !containsIntentKeyword(latestUserMessage, analysisKeywords) {
		return chatIntent{}, false
	}

	return chatIntent{Type: chatIntentPageAnalysis}, true
}

// testCaseIntentMatcher 识别聊天中的测试用例相关请求。
type testCaseIntentMatcher struct{}

// Name 返回测试用例意图识别器的注册名。
func (testCaseIntentMatcher) Name() string { return "test_case_intent" }

// Match 优先判断是否在询问帮助，其次判断是否在请求创建测试用例。
func (testCaseIntentMatcher) Match(chatCtx handlerContext) (chatIntent, bool) {
	latestUserMessage := getLatestUserMessage(chatCtx.Messages)
	if latestUserMessage == "" {
		return chatIntent{}, false
	}

	if shouldShowTestCaseHelp(latestUserMessage) {
		return chatIntent{Type: chatIntentShowTestCaseHelp}, true
	}

	if testCaseTitle, ok := tryParseCreateTestCaseTitle(latestUserMessage); ok {
		return chatIntent{Type: chatIntentCreateTestCase, TestCaseTitle: testCaseTitle}, true
	}

	return chatIntent{}, false
}

// containsIntentKeyword 是轻量级关键词命中工具，用于保持 matcher 规则简单可解释。
func containsIntentKeyword(message string, keywords []string) bool {
	for _, keyword := range keywords {
		if strings.Contains(message, keyword) {
			return true
		}
	}
	return false
}
