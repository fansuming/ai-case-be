package aichat

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"ai-case-be/config"

	"github.com/gin-gonic/gin"
)

// systemPromptBuilder 抽象“不同角色如何构造 system prompt”的差异点。
//
// 聊天主链路并不关心当前是默认聊天、页面分析还是其他角色，
// 它只要求调用方提供一个 prompt 构造函数，并在统一的上游调用流程中注入即可。
type systemPromptBuilder func(req Request, filteredMessages []Message, retrievedDocs []knowledgeDocument) string

// knowledgeRetriever 抽象“不同角色如何做 RAG 检索”的差异点。
//
// 这样默认聊天和页面分析可以共用同一条聊天主链路，
// 但在是否检索、检索什么、如何筛选知识片段上保留各自策略。
type knowledgeRetriever func(req Request, filteredMessages []Message) []knowledgeDocument

// HandleChat 是 aichat 包暴露给 handlers 的聊天入口。
//
// 它的职责非常聚焦：
// 1. 先清洗消息，只保留允许透传给模型的 user/assistant 消息；
// 2. 如果消息为空，尽早返回请求错误；
// 3. 构造内部 handlerContext；
// 4. 将请求交给角色分发链路处理。
func HandleChat(c *gin.Context, req Request) error {
	filteredMessages := filterMessages(req.Messages)
	if len(filteredMessages) == 0 {
		return fmt.Errorf("%w: messages cannot be empty", ErrInvalidRequest)
	}

	chatCtx := handlerContext{
		Request:  req,
		Messages: filteredMessages,
	}

	return dispatchChat(c, chatCtx)
}

// filterMessages 清洗前端消息列表，留下真正可进入模型上下文的内容。
//
// 当前规则较严格：
// - 只接受 user 和 assistant 两种 role；
// - 内容 trim 后为空的消息直接丢弃。
//
// 这样做可以避免未受控 role 干扰后端统一构造的 system prompt，
// 同时减少无意义 token 消耗。
func filterMessages(messages []Message) []Message {
	filtered := make([]Message, 0, len(messages))
	for _, message := range messages {
		if message.Role != "user" && message.Role != "assistant" {
			continue
		}

		trimmedContent := normalizeSpace(message.Content)
		if trimmedContent == "" {
			continue
		}

		filtered = append(filtered, Message{Role: message.Role, Content: trimmedContent})
	}

	return filtered
}

// streamChatWithPrompt 在统一链路中执行“构造 prompt -> 调用上游 -> 透传 SSE”。
//
// 这是所有聊天角色共享的主执行函数：
// - 它负责选择 provider；
// - 把消息转换为上游协议所需结构；
// - 注入基于角色生成的 system prompt；
// - 处理 Azure api-version 兼容回退；
// - 最终把上游流式响应原样透传给前端。
//
// 各具体角色只需要决定自己的 promptBuilder，不需要重复实现这条网络调用链。
func streamChatWithPrompt(c *gin.Context, req Request, filteredMessages []Message, promptBuilder systemPromptBuilder, retriever knowledgeRetriever) error {
	cfg := config.GetConfig()
	// 组装ai请求配置
	upstreamCfg, err := resolveProviderConfig(req.Provider, cfg)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidRequest, err.Error())
	}
	// 根据请求参数和配置解析出当前请求可用的工具列表。
	tools := toolRegistry(req, upstreamCfg, cfg)

	messages := make([]map[string]string, 0, len(filteredMessages))
	for _, message := range filteredMessages {
		messages = append(messages, map[string]string{
			"role":    message.Role,
			"content": message.Content,
		})
	}

	retrievedDocs := retriever(req, filteredMessages)
	systemPrompt := promptBuilder(req, filteredMessages, retrievedDocs)
	if len(tools) > 0 {
		// 如果当前请求配置了工具，且上游模型接口支持工具调用，则在 system prompt 后追加工具使用说明。
		systemPrompt += buildToolPromptSuffix(tools)
	}
	if len(tools) > 0 {
		// 如果有工具调用，走增强的工具调用链路，负责处理模型的工具调用指令并执行工具。
		finalText, toolErr := runToolCallingChat(c.Request.Context(), req, upstreamCfg, systemPrompt, filteredMessages, tools)
		if toolErr != nil {
			return toolErr
		}
		streamAssistantTextResult(c, finalText)
		return nil
	}

	allMessages := make([]map[string]string, 0, len(messages)+1)
	allMessages = append(allMessages, map[string]string{
		"role":    "system",
		"content": systemPrompt,
	})
	allMessages = append(allMessages, messages...)

	openAIBody := map[string]interface{}{}
	if upstreamCfg.Name == "Codex" && upstreamCfg.WireAPI == "responses" {
		openAIBody = map[string]interface{}{
			"model":             upstreamCfg.Model,
			"stream":            true,
			"max_output_tokens": 2048,
			"input":             allMessages,
		}
		if upstreamCfg.ServiceTier != "" {
			openAIBody["service_tier"] = upstreamCfg.ServiceTier
		}
		if upstreamCfg.Reasoning != "" {
			openAIBody["reasoning"] = map[string]string{"effort": upstreamCfg.Reasoning}
		}
	} else {
		openAIBody = map[string]interface{}{
			"model":      upstreamCfg.Model,
			"max_tokens": 2048,
			"stream":     true,
			"messages":   allMessages,
		}
	}

	bodyBytes, err := json.Marshal(openAIBody)
	if err != nil {
		return fmt.Errorf("%w: failed to encode request", ErrInternalRequest)
	}

	httpReq, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, upstreamCfg.BaseURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("%w: failed to create upstream request", ErrInternalRequest)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	setAuthHeader(httpReq, upstreamCfg.AuthType, upstreamCfg.APIKey)

	httpClient := &http.Client{}
	if upstreamCfg.TLSSkipVerify {
		httpClient.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec
	}

	upstream, err := httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("%s API unreachable: %v", upstreamCfg.Name, err)
	}

	if upstream.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(upstream.Body)
		_ = upstream.Body.Close()

		if shouldRetryWithoutAPIVersion(upstreamCfg, upstream.StatusCode, string(body)) {
			retryURL := stripAPIVersion(upstreamCfg.BaseURL)
			retryReq, reqErr := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, retryURL, bytes.NewReader(bodyBytes))
			if reqErr == nil {
				retryReq.Header.Set("Content-Type", "application/json")
				setAuthHeader(retryReq, upstreamCfg.AuthType, upstreamCfg.APIKey)
				retryResp, retryErr := httpClient.Do(retryReq)
				if retryErr == nil {
					if retryResp.StatusCode == http.StatusOK {
						upstream = retryResp
						goto streamResponse
					}
					body, _ = io.ReadAll(retryResp.Body)
					_ = retryResp.Body.Close()
				}
			}
		}

		return &HTTPError{
			StatusCode: normalizeUpstreamStatusCode(upstream.StatusCode),
			Message:    fmt.Sprintf("%s API error: %s — %s", upstreamCfg.Name, upstream.Status, string(body)),
		}
	}

streamResponse:
	return streamUpstreamResponse(c, upstream.Body)
}
