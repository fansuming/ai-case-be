package aichat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"ai-case-be/config"
	"ai-case-be/database"
	"ai-case-be/logger"

	"go.uber.org/zap"
)

type toolAccessMode string

const (
	toolAccessModeRead  toolAccessMode = "read"
	toolAccessModeWrite toolAccessMode = "write"
)

// aiToolSettings 表示一次请求生效后的工具治理配置。
type aiToolSettings struct {
	Enabled      bool
	AllowWrite   bool
	AllowAllPerm bool
	AuditEnabled bool
}

// toolCallContext 保存一次工具执行需要的请求级上下文。
type toolCallContext struct {
	Request  Request
	Upstream upstreamConfig
	Settings aiToolSettings
}

// toolHandler 约定所有内置工具的统一执行签名。
type toolHandler func(ctx context.Context, toolCtx toolCallContext, arguments json.RawMessage) (string, error)

// toolDefinition 是后端内部统一的工具注册对象。
type toolDefinition struct {
	Name        string
	Description string
	AccessMode  toolAccessMode
	Permission  string
	Parameters  map[string]interface{}
	Handler     toolHandler
}

// supportsToolCalling 标识当前 provider 是否启用后端内置工具调用能力。
func supportsToolCalling(upstreamCfg upstreamConfig) bool {
	switch upstreamCfg.WireAPI {
	case "chat_completions":
		return true
	case "responses":
		return upstreamCfg.Name == "Codex"
	default:
		return false
	}
}

// toolRegistry 返回默认启用的内置工具集合。
func toolRegistry(req Request, upstreamCfg upstreamConfig, cfg config.Config) []toolDefinition {
	settings := resolveAIToolSettings(req, cfg)
	if !supportsToolCalling(upstreamCfg) || !settings.Enabled {
		return nil
	}

	registeredTools := []toolDefinition{
		newListAITestCasesTool(),
		newCreateAITestCaseTool(),
	}

	allowedTools := make([]toolDefinition, 0, len(registeredTools))
	blockedReasons := make(map[string]string)
	for _, tool := range registeredTools {
		if tool.AccessMode == toolAccessModeWrite && !settings.AllowWrite {
			blockedReasons[tool.Name] = "write_disabled"
			continue
		}
		if !hasToolPermission(req.Permissions, tool.Permission, settings.AllowAllPerm) {
			blockedReasons[tool.Name] = "permission_denied"
			continue
		}
		allowedTools = append(allowedTools, tool)
	}

	auditToolVisibility(req, upstreamCfg, settings, allowedTools, blockedReasons)

	return allowedTools
}

// hasToolPermission 判断当前请求是否拥有访问某个工具所需的权限码。
func hasToolPermission(grantedPermissions []string, requiredPermission string, allowAllPermissions bool) bool {
	if allowAllPermissions {
		return true
	}

	requiredPermission = strings.TrimSpace(requiredPermission)
	if requiredPermission == "" {
		return true
	}
	for _, permission := range grantedPermissions {
		normalizedPermission := strings.TrimSpace(permission)
		if normalizedPermission == "" {
			continue
		}
		if normalizedPermission == requiredPermission || normalizedPermission == "*" {
			return true
		}
	}
	return false
}

// auditToolVisibility 记录一次请求最终暴露给模型的工具集合，以及被过滤工具的原因。
func auditToolVisibility(req Request, upstreamCfg upstreamConfig, settings aiToolSettings, allowedTools []toolDefinition, blockedReasons map[string]string) {
	if !settings.AuditEnabled || logger.Logger == nil {
		return
	}

	allowedToolNames := make([]string, 0, len(allowedTools))
	for _, tool := range allowedTools {
		allowedToolNames = append(allowedToolNames, tool.Name)
	}

	fields := []zap.Field{
		zap.String("requestId", strings.TrimSpace(req.RequestID)),
		zap.String("provider", strings.ToLower(strings.TrimSpace(upstreamCfg.Name))),
		zap.Bool("allowWriteTools", settings.AllowWrite),
		zap.Bool("allowAllPermissions", settings.AllowAllPerm),
		zap.Strings("grantedPermissions", req.Permissions),
		zap.Strings("allowedTools", allowedToolNames),
	}
	if len(blockedReasons) > 0 {
		fields = append(fields, zap.Any("blockedTools", blockedReasons))
	}

	logger.Info("ai tool visibility resolved", fields...)
}

// resolveAIToolSettings 计算一次请求实际生效的工具开关和写权限策略。
func resolveAIToolSettings(req Request, cfg config.Config) aiToolSettings {
	settings := aiToolSettings{
		Enabled:      true,
		AllowWrite:   false,
		AllowAllPerm: false,
		AuditEnabled: true,
	}

	if cfg.AITools.Enabled != nil {
		settings.Enabled = *cfg.AITools.Enabled
	}
	if cfg.AITools.AllowWrite != nil {
		settings.AllowWrite = *cfg.AITools.AllowWrite
	}
	if cfg.AITools.AllowAllPermissions != nil {
		settings.AllowAllPerm = *cfg.AITools.AllowAllPermissions
	}
	if cfg.AITools.AuditEnabled != nil {
		settings.AuditEnabled = *cfg.AITools.AuditEnabled
	}
	if req.EnableTools != nil {
		settings.Enabled = *req.EnableTools
	}
	if req.AllowWriteTools != nil {
		settings.AllowWrite = *req.AllowWriteTools
	}

	return settings
}

// buildChatCompletionTools 把内部工具定义转换为 Chat Completions 协议需要的 tools 格式。
func buildChatCompletionTools(tools []toolDefinition) []map[string]interface{} {
	if len(tools) == 0 {
		return nil
	}

	converted := make([]map[string]interface{}, 0, len(tools))
	for _, tool := range tools {
		converted = append(converted, map[string]interface{}{
			"type": "function",
			"function": map[string]interface{}{
				"name":        tool.Name,
				"description": tool.Description,
				"parameters":  tool.Parameters,
			},
		})
	}

	return converted
}

// buildResponsesTools 把内部工具定义转换为 Responses API 需要的 tools 格式。
func buildResponsesTools(tools []toolDefinition) []map[string]interface{} {
	if len(tools) == 0 {
		return nil
	}

	converted := make([]map[string]interface{}, 0, len(tools))
	for _, tool := range tools {
		converted = append(converted, map[string]interface{}{
			"type":        "function",
			"name":        tool.Name,
			"description": tool.Description,
			"parameters":  tool.Parameters,
			"strict":      false,
		})
	}

	return converted
}

// buildToolPromptSuffix 为模型补充清晰的工具使用边界，降低误调用概率。
func buildToolPromptSuffix(tools []toolDefinition) string {
	if len(tools) == 0 {
		return ""
	}

	var builder strings.Builder
	builder.WriteString("\n\n你可以在必要时调用后端内置工具。请严格遵守以下规则：")
	builder.WriteString("\n1. 只有在用户请求与工具能力直接相关时才调用工具；")
	builder.WriteString("\n2. 涉及创建或写入类动作时，只有用户明确表达“创建、生成并保存、落库”等意图时才允许调用；")
	builder.WriteString("\n3. 如果无需工具即可回答，则直接回答，不要为了调用而调用。")
	builder.WriteString("\n可用工具：")
	for _, tool := range tools {
		builder.WriteString("\n- ")
		builder.WriteString(tool.Name)
		builder.WriteString(": ")
		builder.WriteString(tool.Description)
	}

	return builder.String()
}

// toolMap 方便按名称快速查找注册工具。
func toolMap(tools []toolDefinition) map[string]toolDefinition {
	indexed := make(map[string]toolDefinition, len(tools))
	for _, tool := range tools {
		indexed[tool.Name] = tool
	}
	return indexed
}

// newListAITestCasesTool 构造“查询 AI 测试用例列表”工具定义。
//
// 这个工具只暴露只读能力，供模型在用户明确询问已有测试 case 时调用，
// 其内部会负责参数兜底、数据库查询和统一 JSON 结果封装。
func newListAITestCasesTool() toolDefinition {
	type listAITestCasesArgs struct {
		Title    string `json:"title"`
		Page     int    `json:"page"`
		PageSize int    `json:"pageSize"`
	}

	return toolDefinition{
		Name:        "list_ai_test_cases",
		Description: "查询系统中已保存的 AI 测试用例，可按标题关键字筛选。适用于查看已有测试 case。",
		AccessMode:  toolAccessModeRead,
		Permission:  "qa:testcase:read",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"title": map[string]interface{}{
					"type":        "string",
					"description": "可选，按标题关键字筛选测试用例。",
				},
				"page": map[string]interface{}{
					"type":        "integer",
					"description": "页码，从 1 开始。",
				},
				"pageSize": map[string]interface{}{
					"type":        "integer",
					"description": "每页数量，建议 1 到 20。",
				},
			},
			"required":             []string{"title", "page", "pageSize"},
			"additionalProperties": false,
		},
		Handler: func(ctx context.Context, toolCtx toolCallContext, arguments json.RawMessage) (string, error) {
			_ = ctx

			var args listAITestCasesArgs
			if len(arguments) > 0 {
				if err := json.Unmarshal(arguments, &args); err != nil {
					return marshalToolError("invalid arguments", err)
				}
			}

			if args.Page <= 0 {
				args.Page = 1
			}
			if args.PageSize <= 0 || args.PageSize > 20 {
				args.PageSize = 10
			}

			items, total, err := database.ListAiTestCases(args.Page, args.PageSize, strings.TrimSpace(args.Title))
			if err != nil {
				return marshalToolError("list ai test cases failed", err)
			}

			return marshalToolResult(map[string]interface{}{
				"page":     args.Page,
				"pageSize": args.PageSize,
				"total":    total,
				"items":    items,
			})
		},
	}
}

// newCreateAITestCaseTool 构造“生成并保存 AI 测试用例”工具定义。
//
// 该工具属于写操作能力，只在用户明确表达创建意图时开放，
// 内部会触发标题校验、测试用例生成和数据库落库流程。
func newCreateAITestCaseTool() toolDefinition {
	type createAITestCaseArgs struct {
		Title   string `json:"title"`
		Creator string `json:"creator"`
	}

	return toolDefinition{
		Name:        "create_ai_test_case",
		Description: "根据标题生成并保存一条 AI 测试用例。仅当用户明确要求创建、生成并保存测试 case 时使用。",
		AccessMode:  toolAccessModeWrite,
		Permission:  "qa:testcase:write",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"title": map[string]interface{}{
					"type":        "string",
					"description": "测试用例标题，要求明确描述目标功能或场景。",
				},
				"creator": map[string]interface{}{
					"type":        "string",
					"description": "可选，创建人标识；为空时使用 ai-agent。",
				},
			},
			"required":             []string{"title", "creator"},
			"additionalProperties": false,
		},
		Handler: func(ctx context.Context, toolCtx toolCallContext, arguments json.RawMessage) (string, error) {
			_ = ctx

			var args createAITestCaseArgs
			if err := json.Unmarshal(arguments, &args); err != nil {
				return marshalToolError("invalid arguments", err)
			}

			title := strings.TrimSpace(args.Title)
			if title == "" {
				return marshalToolError("invalid title", fmt.Errorf("title cannot be empty"))
			}

			creator := strings.TrimSpace(args.Creator)
			if creator == "" {
				creator = "ai-agent"
			}

			item, err := CreateTestCaseRecord(title, toolCtx.Request.Provider, creator)
			if err != nil {
				return marshalToolError("create ai test case failed", err)
			}

			return marshalToolResult(map[string]interface{}{
				"created": true,
				"item":    item,
			})
		},
	}
}

// marshalToolResult 把工具执行成功结果编码成统一的 JSON 字符串。
//
// 工具调用链要求所有结果以字符串形式回传给模型，因此这里集中处理编码逻辑。
func marshalToolResult(payload map[string]interface{}) (string, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// marshalToolError 把工具执行失败信息编码成模型可消费的错误对象。
//
// 这样模型既能看到统一的 error 字段，也能在 detail 中拿到具体失败原因，
// 便于下一轮决定是重试、澄清还是直接向用户解释。
func marshalToolError(message string, err error) (string, error) {
	payload := map[string]interface{}{"ok": false, "error": message}
	if err != nil {
		payload["detail"] = err.Error()
	}
	encoded, marshalErr := json.Marshal(payload)
	if marshalErr != nil {
		return "", marshalErr
	}
	return string(encoded), nil
}
