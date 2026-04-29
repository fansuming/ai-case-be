package aichat

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"ai-case-be/config"
	"ai-case-be/logger"

	"go.uber.org/zap"
)

const maxToolLoopIterations = 4

type chatCompletionResponse struct {
	Choices []chatCompletionChoice `json:"choices"`
}

type chatCompletionChoice struct {
	Message chatCompletionMessage `json:"message"`
}

type chatCompletionMessage struct {
	Role      string                   `json:"role"`
	Content   json.RawMessage          `json:"content"`
	ToolCalls []chatCompletionToolCall `json:"tool_calls"`
}

type chatCompletionToolCall struct {
	ID       string                     `json:"id"`
	Type     string                     `json:"type"`
	Function chatCompletionFunctionCall `json:"function"`
}

type chatCompletionFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type responsesFunctionCall struct {
	ID        string `json:"id"`
	CallID    string `json:"call_id"`
	Type      string `json:"type"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type responsesOutputEnvelope struct {
	Output     []json.RawMessage `json:"output"`
	OutputText string            `json:"output_text"`
}

type responsesMessageOutput struct {
	Type    string                   `json:"type"`
	Role    string                   `json:"role"`
	Content []responsesContentOutput `json:"content"`
}

type responsesContentOutput struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// runToolCallingChat 在单次请求内完成“模型选工具 -> 后端执行 -> 回传结果 -> 模型总结”的闭环。
func runToolCallingChat(ctx context.Context, req Request, upstreamCfg upstreamConfig, systemPrompt string, filteredMessages []Message, tools []toolDefinition) (string, error) {
	if len(tools) == 0 {
		return "", fmt.Errorf("no tools configured")
	}

	switch upstreamCfg.WireAPI {
	case "responses":
		return runResponsesToolLoop(ctx, req, upstreamCfg, systemPrompt, filteredMessages, tools)
	case "chat_completions":
		return runChatCompletionsToolLoop(ctx, req, upstreamCfg, systemPrompt, filteredMessages, tools)
	default:
		return "", fmt.Errorf("%w: unsupported tool wire api %s", ErrInternalRequest, upstreamCfg.WireAPI)
	}
}

// runChatCompletionsToolLoop 执行基于 Chat Completions 协议的多轮工具调用循环。
//
// 每一轮都先向模型请求下一步动作；若模型返回 tool_calls，则在后端执行工具后把结果追加回消息历史，
// 直到模型直接给出最终文本答案，或达到最大循环次数。
func runChatCompletionsToolLoop(ctx context.Context, req Request, upstreamCfg upstreamConfig, systemPrompt string, filteredMessages []Message, tools []toolDefinition) (string, error) {
	messages := buildToolLoopMessages(systemPrompt, filteredMessages)
	indexedTools := toolMap(tools)
	toolCtx := toolCallContext{Request: req, Upstream: upstreamCfg, Settings: resolveAIToolSettings(req, config.GetConfig())}

	for iteration := 0; iteration < maxToolLoopIterations; iteration++ {
		// 先让模型决定本轮是继续调用工具，还是直接输出最终回答。
		assistantMessage, err := requestChatCompletionToolLoop(ctx, upstreamCfg, messages, tools)
		if err != nil {
			return "", err
		}

		messages = append(messages, buildAssistantLoopMessage(assistantMessage))
		if len(assistantMessage.ToolCalls) == 0 {
			content := extractChatCompletionContent(assistantMessage.Content)
			if strings.TrimSpace(content) == "" {
				return "", fmt.Errorf("%w: empty assistant response", ErrInternalRequest)
			}
			return content, nil
		}

		for _, toolCall := range assistantMessage.ToolCalls {
			// 工具名未注册时也要把错误结果回传给模型，让它自行修正下一轮动作。
			tool, ok := indexedTools[toolCall.Function.Name]
			if !ok {
				toolOutput, marshalErr := marshalToolError("unknown tool", fmt.Errorf("tool %s is not registered", toolCall.Function.Name))
				if marshalErr != nil {
					return "", fmt.Errorf("%w: unknown tool %s", ErrInternalRequest, toolCall.Function.Name)
				}
				messages = append(messages, map[string]interface{}{
					"role":         "tool",
					"tool_call_id": toolCall.ID,
					"content":      toolOutput,
				})
				continue
			}

			startedAt := time.Now()
			toolOutput, err := tool.Handler(ctx, toolCtx, json.RawMessage(toolCall.Function.Arguments))
			auditToolExecution(toolCtx, tool, toolCall.Function.Arguments, time.Since(startedAt), err)
			if err != nil {
				return "", fmt.Errorf("%w: execute tool %s: %s", ErrInternalRequest, tool.Name, err.Error())
			}

			messages = append(messages, map[string]interface{}{
				"role":         "tool",
				"tool_call_id": toolCall.ID,
				"content":      toolOutput,
			})
		}
	}

	return "", fmt.Errorf("%w: tool calling exceeded max iterations", ErrInternalRequest)
}

// runResponsesToolLoop 执行基于 Responses API 协议的多轮工具调用循环。
//
// 与 Chat Completions 版本的目标一致，只是输入输出结构换成 Responses API 的 output / function_call 格式，
// 便于兼容 Codex 等要求不同协议的上游提供方。
func runResponsesToolLoop(ctx context.Context, req Request, upstreamCfg upstreamConfig, systemPrompt string, filteredMessages []Message, tools []toolDefinition) (string, error) {
	inputItems := buildResponsesToolLoopInput(systemPrompt, filteredMessages)
	indexedTools := toolMap(tools)
	toolCtx := toolCallContext{Request: req, Upstream: upstreamCfg, Settings: resolveAIToolSettings(req, config.GetConfig())}

	for iteration := 0; iteration < maxToolLoopIterations; iteration++ {
		// 先拿到本轮模型输出，再从中拆出函数调用和普通消息片段。
		responseEnvelope, functionCalls, responseItems, err := requestResponsesToolLoop(ctx, upstreamCfg, inputItems, tools)
		if err != nil {
			return "", err
		}

		if len(functionCalls) == 0 {
			finalText := strings.TrimSpace(responseEnvelope.OutputText)
			if finalText == "" {
				finalText = extractResponsesOutputText(responseItems)
			}
			if finalText == "" {
				return "", fmt.Errorf("%w: empty assistant response", ErrInternalRequest)
			}
			return finalText, nil
		}

		inputItems = append(inputItems, responseItems...)
		for _, functionCall := range functionCalls {
			tool, ok := indexedTools[functionCall.Name]
			if !ok {
				toolOutput, marshalErr := marshalToolError("unknown tool", fmt.Errorf("tool %s is not registered", functionCall.Name))
				if marshalErr != nil {
					return "", fmt.Errorf("%w: unknown tool %s", ErrInternalRequest, functionCall.Name)
				}
				inputItems = append(inputItems, map[string]interface{}{
					"type":    "function_call_output",
					"call_id": functionCall.CallID,
					"output":  toolOutput,
				})
				continue
			}

			startedAt := time.Now()
			toolOutput, err := tool.Handler(ctx, toolCtx, json.RawMessage(functionCall.Arguments))
			auditToolExecution(toolCtx, tool, functionCall.Arguments, time.Since(startedAt), err)
			if err != nil {
				return "", fmt.Errorf("%w: execute tool %s: %s", ErrInternalRequest, tool.Name, err.Error())
			}

			inputItems = append(inputItems, map[string]interface{}{
				"type":    "function_call_output",
				"call_id": functionCall.CallID,
				"output":  toolOutput,
			})
		}
	}

	return "", fmt.Errorf("%w: tool calling exceeded max iterations", ErrInternalRequest)
}

// buildToolLoopMessages 构造 Chat Completions 工具循环所需的消息历史。
//
// 它会把 system prompt 放在最前面，并把过滤后的对话消息按原顺序追加，
// 作为模型决定是否调用工具的完整上下文。
func buildToolLoopMessages(systemPrompt string, filteredMessages []Message) []map[string]interface{} {
	messages := make([]map[string]interface{}, 0, len(filteredMessages)+1)
	messages = append(messages, map[string]interface{}{
		"role":    "system",
		"content": systemPrompt,
	})
	for _, message := range filteredMessages {
		messages = append(messages, map[string]interface{}{
			"role":    message.Role,
			"content": message.Content,
		})
	}
	return messages
}

// buildResponsesToolLoopInput 构造 Responses API 工具循环使用的 input 数组。
//
// 这里与 Chat Completions 的核心差异只有协议字段名不同，因此专门抽成独立函数，
// 让两条循环主链路共享同一套业务上下文但不混淆协议细节。
func buildResponsesToolLoopInput(systemPrompt string, filteredMessages []Message) []interface{} {
	inputItems := make([]interface{}, 0, len(filteredMessages)+1)
	inputItems = append(inputItems, map[string]interface{}{
		"role":    "system",
		"content": systemPrompt,
	})
	for _, message := range filteredMessages {
		inputItems = append(inputItems, map[string]interface{}{
			"role":    message.Role,
			"content": message.Content,
		})
	}
	return inputItems
}

// buildAssistantLoopMessage 把模型助手消息转换回下一轮请求可复用的消息结构。
//
// 当模型同时给出 content 和 tool_calls 时，两部分都需要保留下来，
// 否则下一轮模型可能丢失自己的中间推理上下文。
func buildAssistantLoopMessage(message chatCompletionMessage) map[string]interface{} {
	assistantMessage := map[string]interface{}{
		"role": "assistant",
	}

	content := extractChatCompletionContent(message.Content)
	if content != "" {
		assistantMessage["content"] = content
	} else {
		assistantMessage["content"] = nil
	}

	if len(message.ToolCalls) > 0 {
		assistantMessage["tool_calls"] = message.ToolCalls
	}

	return assistantMessage
}

// requestChatCompletionToolLoop 向 Chat Completions 上游发起一次非流式工具决策请求。
//
// 该函数只负责协议层请求和响应解析，不负责执行工具本身，
// 以便主循环专注于“请求模型 -> 执行工具 -> 回填消息”的状态推进。
func requestChatCompletionToolLoop(ctx context.Context, upstreamCfg upstreamConfig, messages []map[string]interface{}, tools []toolDefinition) (chatCompletionMessage, error) {
	requestBody := map[string]interface{}{
		"model":       upstreamCfg.Model,
		"max_tokens":  2048,
		"stream":      false,
		"messages":    messages,
		"tools":       buildChatCompletionTools(tools),
		"tool_choice": "auto",
	}

	bodyBytes, err := json.Marshal(requestBody)
	if err != nil {
		return chatCompletionMessage{}, fmt.Errorf("%w: failed to encode tool request", ErrInternalRequest)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, upstreamCfg.BaseURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return chatCompletionMessage{}, fmt.Errorf("%w: failed to create tool request", ErrInternalRequest)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	setAuthHeader(httpReq, upstreamCfg.AuthType, upstreamCfg.APIKey)

	httpClient := &http.Client{}
	if upstreamCfg.TLSSkipVerify {
		httpClient.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec
	}

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return chatCompletionMessage{}, fmt.Errorf("%s API unreachable: %v", upstreamCfg.Name, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return chatCompletionMessage{}, fmt.Errorf("%w: failed to read tool response", ErrInternalRequest)
	}
	if resp.StatusCode != http.StatusOK {
		return chatCompletionMessage{}, &HTTPError{
			StatusCode: normalizeUpstreamStatusCode(resp.StatusCode),
			Message:    fmt.Sprintf("%s API error: %s — %s", upstreamCfg.Name, resp.Status, string(body)),
		}
	}

	var decoded chatCompletionResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return chatCompletionMessage{}, fmt.Errorf("%w: failed to parse tool response", ErrInternalRequest)
	}
	if len(decoded.Choices) == 0 {
		return chatCompletionMessage{}, fmt.Errorf("%w: empty model choices", ErrInternalRequest)
	}

	return decoded.Choices[0].Message, nil
}

// requestResponsesToolLoop 向 Responses API 发起一次工具决策请求，并拆解返回的 output 项。
//
// 返回值同时保留原始 envelope、提取后的 function_call 列表和可回填的 responseItems，
// 这样主循环可以根据是否存在函数调用决定继续执行工具还是结束对话。
func requestResponsesToolLoop(ctx context.Context, upstreamCfg upstreamConfig, inputItems []interface{}, tools []toolDefinition) (responsesOutputEnvelope, []responsesFunctionCall, []interface{}, error) {
	requestBody := map[string]interface{}{
		"model":             upstreamCfg.Model,
		"stream":            false,
		"max_output_tokens": 2048,
		"input":             inputItems,
		"tools":             buildResponsesTools(tools),
		"tool_choice":       "auto",
	}
	if upstreamCfg.ServiceTier != "" {
		requestBody["service_tier"] = upstreamCfg.ServiceTier
	}
	if upstreamCfg.Reasoning != "" {
		requestBody["reasoning"] = map[string]string{"effort": upstreamCfg.Reasoning}
	}

	bodyBytes, err := json.Marshal(requestBody)
	if err != nil {
		return responsesOutputEnvelope{}, nil, nil, fmt.Errorf("%w: failed to encode tool request", ErrInternalRequest)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, upstreamCfg.BaseURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return responsesOutputEnvelope{}, nil, nil, fmt.Errorf("%w: failed to create tool request", ErrInternalRequest)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	setAuthHeader(httpReq, upstreamCfg.AuthType, upstreamCfg.APIKey)

	httpClient := &http.Client{}
	if upstreamCfg.TLSSkipVerify {
		httpClient.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec
	}

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return responsesOutputEnvelope{}, nil, nil, fmt.Errorf("%s API unreachable: %v", upstreamCfg.Name, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return responsesOutputEnvelope{}, nil, nil, fmt.Errorf("%w: failed to read tool response", ErrInternalRequest)
	}
	if resp.StatusCode != http.StatusOK {
		return responsesOutputEnvelope{}, nil, nil, &HTTPError{
			StatusCode: normalizeUpstreamStatusCode(resp.StatusCode),
			Message:    fmt.Sprintf("%s API error: %s — %s", upstreamCfg.Name, resp.Status, string(body)),
		}
	}

	var envelope responsesOutputEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return responsesOutputEnvelope{}, nil, nil, fmt.Errorf("%w: failed to parse tool response", ErrInternalRequest)
	}

	functionCalls := make([]responsesFunctionCall, 0)
	responseItems := make([]interface{}, 0, len(envelope.Output))
	for _, outputItem := range envelope.Output {
		var rawItem map[string]interface{}
		if err := json.Unmarshal(outputItem, &rawItem); err == nil {
			responseItems = append(responseItems, rawItem)
		}

		var outputType struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(outputItem, &outputType); err != nil {
			continue
		}
		if outputType.Type != "function_call" {
			continue
		}

		var functionCall responsesFunctionCall
		if err := json.Unmarshal(outputItem, &functionCall); err != nil {
			continue
		}
		functionCalls = append(functionCalls, functionCall)
	}

	return envelope, functionCalls, responseItems, nil
}

// extractChatCompletionContent 从 Chat Completions 的 content 字段中提取纯文本内容。
//
// 上游可能返回字符串、分段数组甚至原始 JSON 片段，因此这里按常见格式依次尝试解析，
// 尽量把最终可读文本统一抽取出来。
func extractChatCompletionContent(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}

	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return strings.TrimSpace(text)
	}

	var parts []interface{}
	if err := json.Unmarshal(raw, &parts); err == nil {
		return extractTextFromParts(parts)
	}

	return strings.TrimSpace(string(raw))
}

// extractResponsesOutputText 从 Responses API 的 output 项中拼装最终文本回答。
//
// 该函数会过滤非 message 项和空文本片段，只保留 output_text/text 类型内容，
// 用于补足 output_text 字段为空时的兜底提取。
func extractResponsesOutputText(outputItems []interface{}) string {
	segments := make([]string, 0)
	for _, outputItem := range outputItems {
		encoded, err := json.Marshal(outputItem)
		if err != nil {
			continue
		}

		var messageOutput responsesMessageOutput
		if err := json.Unmarshal(encoded, &messageOutput); err != nil {
			continue
		}
		if messageOutput.Type != "message" {
			continue
		}

		for _, contentPart := range messageOutput.Content {
			if (contentPart.Type == "output_text" || contentPart.Type == "text") && strings.TrimSpace(contentPart.Text) != "" {
				segments = append(segments, strings.TrimSpace(contentPart.Text))
			}
		}
	}

	return strings.TrimSpace(strings.Join(segments, "\n"))
}

// auditToolExecution 记录一次工具调用的审计日志。
//
// 它会统一输出请求标识、工具名、权限、耗时和参数预览，
// 便于排查模型调用了什么工具、是否成功以及失败发生在哪一轮。
func auditToolExecution(toolCtx toolCallContext, tool toolDefinition, rawArguments string, duration time.Duration, executeErr error) {
	if !toolCtx.Settings.AuditEnabled || logger.Logger == nil {
		return
	}

	fields := []zap.Field{
		zap.String("requestId", strings.TrimSpace(toolCtx.Request.RequestID)),
		zap.String("toolName", tool.Name),
		zap.String("permission", tool.Permission),
		zap.String("accessMode", string(tool.AccessMode)),
		zap.String("provider", strings.ToLower(strings.TrimSpace(toolCtx.Upstream.Name))),
		zap.Bool("success", executeErr == nil),
		zap.Int64("durationMs", duration.Milliseconds()),
		zap.String("page", strings.TrimSpace(toolCtx.Request.Context.Page)),
		zap.String("argumentsPreview", truncateForLog(rawArguments, 256)),
	}
	if executeErr != nil {
		fields = append(fields, zap.String("error", executeErr.Error()))
		logger.Warn("ai tool execution finished", fields...)
		return
	}

	logger.Info("ai tool execution finished", fields...)
}

// truncateForLog 截断审计日志中的长文本字段，避免参数内容撑爆日志体积。
//
// 该函数只用于日志展示，不改变真实执行参数；超长内容会保留前缀并追加省略号。
func truncateForLog(value string, limit int) string {
	trimmed := strings.TrimSpace(value)
	if limit <= 0 || len(trimmed) <= limit {
		return trimmed
	}
	return trimmed[:limit] + "..."
}
