package aichat

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"ai-case-be/config"
	"ai-case-be/database"

	"github.com/gin-gonic/gin"
)

// generatedTestCase 表示模型生成后、正式落库前的标准化测试用例对象。
//
// 该结构要求字段已经符合后端入库约定，避免后续还要对模型结果做二次结构转换。
type generatedTestCase struct {
	CaseTitle      string   `json:"caseTitle"`
	Preconditions  []string `json:"preconditions"`
	Steps          []string `json:"steps"`
	ExpectedResult string   `json:"expectedResult"`
	Priority       string   `json:"priority"`
}

// testCaseRoleHandler 处理聊天中的测试用例相关意图。
type testCaseRoleHandler struct{}

// Name 返回测试用例角色处理器的名称。
func (testCaseRoleHandler) Name() string { return "test_case" }

// CanHandle 声明该 handler 同时接住“创建测试用例”和“查看帮助”两类意图。
func (testCaseRoleHandler) CanHandle(intent chatIntent) bool {
	return intent.Type == chatIntentCreateTestCase || intent.Type == chatIntentShowTestCaseHelp
}

// Handle 根据意图类型分发到真正的测试用例处理逻辑。
func (testCaseRoleHandler) Handle(c *gin.Context, chatCtx handlerContext) error {
	switch chatCtx.Intent.Type {
	case chatIntentCreateTestCase:
		streamTestCaseCreationResult(c, chatCtx.Intent.TestCaseTitle, chatCtx.Request.Provider)
		return nil
	case chatIntentShowTestCaseHelp:
		streamTestCaseHelpResult(c)
		return nil
	default:
		return fmt.Errorf("unsupported intent: %s", chatCtx.Intent.Type)
	}
}

// CreateTestCaseRecord 负责把“标题 -> 模型生成 -> 数据库存储”串成一个原子流程。
//
// 这是当前 AI 测试用例能力对外暴露的核心业务入口，HTTP handler 与聊天角色都会复用它。
func CreateTestCaseRecord(title, provider, creator string) (database.AiTestCaseItem, error) {
	generated, normalizedProvider, err := generateTestCaseByTitle(title, provider)
	if err != nil {
		return database.AiTestCaseItem{}, err
	}

	item, err := database.CreateAiTestCase(database.AiTestCaseRecord{
		Title:          strings.TrimSpace(title),
		CaseTitle:      generated.CaseTitle,
		Preconditions:  strings.Join(generated.Preconditions, "\n"),
		Steps:          strings.Join(generated.Steps, "\n"),
		ExpectedResult: generated.ExpectedResult,
		Priority:       generated.Priority,
		Status:         "created",
		Source:         "ai",
		Provider:       normalizedProvider,
		Creator:        creator,
	})
	if err != nil {
		return database.AiTestCaseItem{}, fmt.Errorf("创建测试 case 失败: %w", err)
	}

	return item, nil
}

// generateTestCaseByTitle 根据标题调用上游模型生成结构化测试用例。
//
// 它会完成 provider 解析、请求体组装、上游调用、响应解析和结果归一化。
func generateTestCaseByTitle(title, provider string) (generatedTestCase, string, error) {
	cfg := config.GetConfig()
	upstreamCfg, err := resolveProviderConfig(provider, cfg)
	if err != nil {
		return generatedTestCase{}, "", err
	}

	requestBody, err := buildTestCaseRequestBody(upstreamCfg, title)
	if err != nil {
		return generatedTestCase{}, "", fmt.Errorf("生成测试 case 请求失败: %w", err)
	}

	bodyBytes, err := json.Marshal(requestBody)
	if err != nil {
		return generatedTestCase{}, "", fmt.Errorf("编码测试 case 请求失败: %w", err)
	}

	httpClient := &http.Client{}
	if upstreamCfg.TLSSkipVerify {
		httpClient.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec
	}

	resp, err := doTestCaseUpstreamRequest(httpClient, upstreamCfg, bodyBytes)
	if err != nil {
		return generatedTestCase{}, "", fmt.Errorf("%s API 不可用: %w", upstreamCfg.Name, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return generatedTestCase{}, "", fmt.Errorf("读取测试 case 响应失败: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return generatedTestCase{}, "", fmt.Errorf("%s API error: %s — %s", upstreamCfg.Name, resp.Status, string(body))
	}

	content, err := extractTestCaseResponseContent(body)
	if err != nil {
		return generatedTestCase{}, "", err
	}

	parsed, err := parseGeneratedTestCase(content, title)
	if err != nil {
		return generatedTestCase{}, "", err
	}

	providerName := strings.ToLower(strings.TrimSpace(upstreamCfg.Name))
	if providerName == "" {
		providerName = "codex"
	}

	return parsed, providerName, nil
}

// doTestCaseUpstreamRequest 执行测试用例生成请求，并处理网络抖动或 api-version 兼容性重试。
func doTestCaseUpstreamRequest(httpClient *http.Client, upstreamCfg upstreamConfig, bodyBytes []byte) (*http.Response, error) {
	requestURL := upstreamCfg.BaseURL
	transportRetried := false
	versionRetried := false

	for {
		httpReq, err := http.NewRequest(http.MethodPost, requestURL, bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, fmt.Errorf("创建测试 case 请求失败: %w", err)
		}
		httpReq.Header.Set("Content-Type", "application/json")
		setAuthHeader(httpReq, upstreamCfg.AuthType, upstreamCfg.APIKey)

		resp, err := httpClient.Do(httpReq)
		if err != nil {
			if !transportRetried && shouldRetryTransportError(err) {
				transportRetried = true
				continue
			}
			return nil, err
		}

		if resp.StatusCode == http.StatusOK {
			return resp, nil
		}

		body, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("读取测试 case 响应失败: %w", readErr)
		}

		if !versionRetried && shouldRetryWithoutAPIVersion(upstreamCfg, resp.StatusCode, string(body)) {
			retryURL := stripAPIVersion(requestURL)
			if retryURL != requestURL {
				versionRetried = true
				requestURL = retryURL
				continue
			}
		}

		return nil, &HTTPError{StatusCode: normalizeUpstreamStatusCode(resp.StatusCode), Message: fmt.Sprintf("%s API error: %s — %s", upstreamCfg.Name, resp.Status, string(body))}
	}
}

// shouldRetryTransportError 判断当前错误是否属于可安全重试一次的暂态网络错误。
func shouldRetryTransportError(err error) bool {
	if err == nil {
		return false
	}
	lowerError := strings.ToLower(err.Error())
	return strings.Contains(lowerError, "connection reset by peer") || strings.Contains(lowerError, "broken pipe") || strings.Contains(lowerError, "unexpected eof") || strings.Contains(lowerError, "server closed idle connection")
}

// buildTestCaseRequestBody 为不同上游协议构造“生成测试用例”的请求体。
//
// prompt 的重点是强约束模型只返回 JSON，并限制字段名、数组长度和优先级枚举。
func buildTestCaseRequestBody(upstreamCfg upstreamConfig, title string) (map[string]interface{}, error) {
	systemPrompt := strings.Join([]string{"你是一位资深测试工程师。", "请根据给定标题生成一个可直接落库的测试 case。", "只返回 JSON 对象，不要输出 Markdown，不要输出解释文字。", "JSON 字段固定为 caseTitle、preconditions、steps、expectedResult、priority。", "preconditions 和 steps 必须是字符串数组，长度 2 到 5。", "priority 只能是 高、中、低 之一。", "caseTitle 要简洁明确，expectedResult 要可验证。"}, "")
	userPrompt := "标题：" + strings.TrimSpace(title)
	messages := []map[string]string{{"role": "system", "content": systemPrompt}, {"role": "user", "content": userPrompt}}

	if upstreamCfg.Name == "Codex" && upstreamCfg.WireAPI == "responses" {
		body := map[string]interface{}{"model": upstreamCfg.Model, "stream": false, "max_output_tokens": 1200, "input": messages}
		if upstreamCfg.ServiceTier != "" {
			body["service_tier"] = upstreamCfg.ServiceTier
		}
		if upstreamCfg.Reasoning != "" {
			body["reasoning"] = map[string]string{"effort": upstreamCfg.Reasoning}
		}
		return body, nil
	}

	return map[string]interface{}{"model": upstreamCfg.Model, "max_tokens": 1200, "stream": false, "messages": messages}, nil
}

// extractTestCaseResponseContent 从不同 provider 的响应结构中提取模型正文文本。
//
// 当前兼容 output_text、chat completions 和 responses 嵌套 content 三种常见格式。
func extractTestCaseResponseContent(body []byte) (string, error) {
	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("解析测试 case 响应失败: %w", err)
	}
	if outputText, ok := payload["output_text"].(string); ok && strings.TrimSpace(outputText) != "" {
		return outputText, nil
	}
	if choices, ok := payload["choices"].([]interface{}); ok && len(choices) > 0 {
		if firstChoice, ok := choices[0].(map[string]interface{}); ok {
			if message, ok := firstChoice["message"].(map[string]interface{}); ok {
				if content, ok := message["content"].(string); ok && strings.TrimSpace(content) != "" {
					return content, nil
				}
				if parts, ok := message["content"].([]interface{}); ok {
					text := extractTextFromParts(parts)
					if text != "" {
						return text, nil
					}
				}
			}
		}
	}
	if output, ok := payload["output"].([]interface{}); ok {
		for _, item := range output {
			if itemMap, ok := item.(map[string]interface{}); ok {
				if content, ok := itemMap["content"].([]interface{}); ok {
					text := extractTextFromParts(content)
					if text != "" {
						return text, nil
					}
				}
			}
		}
	}
	return "", fmt.Errorf("AI 未返回可解析的测试 case 内容")
}

// extractTextFromParts 把 content 数组中的多个文本片段拼接为完整字符串。
func extractTextFromParts(parts []interface{}) string {
	segments := make([]string, 0, len(parts))
	for _, part := range parts {
		partMap, ok := part.(map[string]interface{})
		if !ok {
			continue
		}
		if text, ok := partMap["text"].(string); ok && strings.TrimSpace(text) != "" {
			segments = append(segments, text)
			continue
		}
		if nestedText, ok := partMap["output_text"].(string); ok && strings.TrimSpace(nestedText) != "" {
			segments = append(segments, nestedText)
		}
	}
	return strings.TrimSpace(strings.Join(segments, "\n"))
}

// parseGeneratedTestCase 把模型返回的 JSON 文本解析并归一化为 generatedTestCase。
//
// 即使模型输出存在轻微偏差，也会尽量补齐标题、前置条件、步骤、预期结果和优先级的默认值。
func parseGeneratedTestCase(content, fallbackTitle string) (generatedTestCase, error) {
	jsonContent := extractFirstJSONObject(content)
	if jsonContent == "" {
		return generatedTestCase{}, fmt.Errorf("AI 返回内容不是有效 JSON")
	}

	var generated generatedTestCase
	if err := json.Unmarshal([]byte(jsonContent), &generated); err != nil {
		return generatedTestCase{}, fmt.Errorf("解析 AI 测试 case 失败: %w", err)
	}

	generated.CaseTitle = strings.TrimSpace(generated.CaseTitle)
	if generated.CaseTitle == "" {
		generated.CaseTitle = strings.TrimSpace(fallbackTitle)
	}
	generated.Preconditions = normalizeGeneratedLines(generated.Preconditions, []string{"准备测试环境", "准备与标题相关的基础数据"})
	generated.Steps = normalizeGeneratedLines(generated.Steps, []string{"进入目标功能页面", "执行与标题相关的核心操作"})
	generated.ExpectedResult = strings.TrimSpace(generated.ExpectedResult)
	if generated.ExpectedResult == "" {
		generated.ExpectedResult = "系统按预期完成处理，并返回正确结果。"
	}
	generated.Priority = normalizePriority(generated.Priority)

	return generated, nil
}

// normalizeGeneratedLines 清洗前置条件和步骤数组，移除编号、空白和无效文本。
func normalizeGeneratedLines(lines []string, fallback []string) []string {
	normalized := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		trimmed = strings.TrimLeft(trimmed, "-0123456789. ")
		trimmed = strings.TrimSpace(trimmed)
		if trimmed == "" {
			continue
		}
		normalized = append(normalized, trimmed)
	}
	if len(normalized) > 0 {
		return normalized
	}
	return fallback
}

// normalizePriority 只保留允许入库的优先级取值，其余情况统一降级为“中”。
func normalizePriority(priority string) string {
	switch strings.TrimSpace(priority) {
	case "高", "中", "低":
		return strings.TrimSpace(priority)
	default:
		return "中"
	}
}

// extractFirstJSONObject 从模型回复文本中截取第一个完整 JSON 对象。
//
// 这是对“模型在 JSON 前后附带解释文字”这类情况的轻量容错。
func extractFirstJSONObject(content string) string {
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start < 0 || end <= start {
		return ""
	}
	return strings.TrimSpace(content[start : end+1])
}

// createTestCasePatterns 是从自然语言中提取测试用例标题的正则模板集合。
var createTestCasePatterns = []*regexp.Regexp{
	regexp.MustCompile(`根据\s*[“"']?(.+?)[”"']?\s*标题`),
	regexp.MustCompile(`标题[：: ]*[“"']?(.+?)[”"']?(?:[,，。]|$)`),
	regexp.MustCompile(`(?:测试case|测试用例|case|用例)\s*[：:]\s*[“"']?(.+?)[”"']?(?:[,，。]|$)`),
	regexp.MustCompile(`(?:帮我|请|麻烦)?(?:生成|创建|建|新增|写|补)(?:一个|个)?(?:测试case|测试用例|case|用例)\s*[：: ]+\s*[“"']?(.+?)[”"']?(?:[,，。]|$)`),
	regexp.MustCompile(`(?:帮我|请|麻烦)?(?:生成|创建|建|新增|写|补).+?(?:测试case|测试用例|case|用例).+?(?:标题|为)\s*[：: ]*\s*[“"']?(.+?)[”"']?(?:[,，。]|$)`),
}

// testCaseHelpPatterns 用于识别“用户是在询问测试用例功能怎么用”。
var testCaseHelpPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?:怎么|如何|怎样).*(?:测试case|测试用例|case|用例)`),
	regexp.MustCompile(`(?:测试case|测试用例|case|用例).*(?:怎么|如何|怎样)`),
	regexp.MustCompile(`(?:测试case|测试用例|case|用例).*(?:命令|示例|例子|帮助|用法)`),
	regexp.MustCompile(`(?:命令|示例|例子|帮助|用法).*(?:测试case|测试用例|case|用例)`),
	regexp.MustCompile(`(?:不会|不知道).*(?:测试case|测试用例|case|用例)`),
}

// createTestCaseTailPatterns 用于清理标题尾部残留的动作描述，避免误把命令痕迹保存为标题内容。
var createTestCaseTailPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\s*(?:并且|并|然后)?(?:帮我)?(?:创建|建|新增|保存)(?:成功)?\s*$`),
	regexp.MustCompile(`\s*(?:这个|该)?(?:case|测试case|测试用例|用例)?(?:也)?(?:创建|建|新增|保存)(?:一下|下)?\s*$`),
}

// tryParseCreateTestCaseTitle 尝试从自然语言指令中提取“要创建的测试用例标题”。
//
// 它会先做关键词过滤，再走多组模板匹配，最后再尝试命令前缀和引号兜底提取。
func tryParseCreateTestCaseTitle(content string) (string, bool) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return "", false
	}

	lower := strings.ToLower(trimmed)
	hasCaseKeyword := strings.Contains(lower, "case") || strings.Contains(trimmed, "测试用例") || strings.Contains(trimmed, "用例")
	hasActionKeyword := strings.Contains(trimmed, "生成") || strings.Contains(trimmed, "创建") || strings.Contains(trimmed, "建") || strings.Contains(trimmed, "新增") || strings.Contains(trimmed, "写") || strings.Contains(trimmed, "补")
	if !hasCaseKeyword || !hasActionKeyword {
		return "", false
	}

	for _, pattern := range createTestCasePatterns {
		matches := pattern.FindStringSubmatch(trimmed)
		if len(matches) < 2 {
			continue
		}
		title := normalizeCreateTestCaseTitle(matches[1])
		if title != "" {
			return title, true
		}
	}

	commandPrefixes := []string{"帮我建个case", "帮我建个测试case", "帮我建个测试用例", "帮我创建case", "帮我创建测试case", "帮我创建测试用例", "帮我生成测试case", "帮我生成测试用例", "请建个case", "请创建测试case", "请创建测试用例", "创建测试case", "创建测试用例", "生成测试case", "生成测试用例", "新增测试case", "新增测试用例"}
	for _, prefix := range commandPrefixes {
		if strings.HasPrefix(trimmed, prefix) {
			title := normalizeCreateTestCaseTitle(strings.TrimSpace(strings.TrimPrefix(trimmed, prefix)))
			if title != "" {
				return title, true
			}
		}
	}

	if quotedTitle := extractQuotedTestCaseTitle(trimmed); quotedTitle != "" {
		return quotedTitle, true
	}

	return "", false
}

// shouldShowTestCaseHelp 判断当前消息更像“求用法说明”还是“要求立即创建”。
func shouldShowTestCaseHelp(content string) bool {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return false
	}
	for _, pattern := range testCaseHelpPatterns {
		if pattern.MatchString(trimmed) {
			return true
		}
	}
	return false
}

// normalizeCreateTestCaseTitle 清洗提取到的标题文本，移除命令前后缀和标点噪声。
func normalizeCreateTestCaseTitle(title string) string {
	normalized := strings.TrimSpace(title)
	normalized = strings.TrimPrefix(normalized, "标题")
	normalized = strings.TrimPrefix(normalized, "case")
	normalized = strings.TrimPrefix(normalized, "测试case")
	normalized = strings.TrimPrefix(normalized, "测试用例")
	normalized = strings.TrimPrefix(normalized, "用例")
	normalized = strings.TrimSpace(normalized)
	normalized = strings.TrimLeft(normalized, "：: ")
	for _, pattern := range createTestCaseTailPatterns {
		normalized = pattern.ReplaceAllString(normalized, "")
	}
	normalized = strings.Trim(normalized, "“”\"'，。,.：: ")
	return strings.TrimSpace(normalized)
}

// extractQuotedTestCaseTitle 尝试直接从引号包裹文本中提取标题。
func extractQuotedTestCaseTitle(content string) string {
	quotePatterns := []*regexp.Regexp{regexp.MustCompile(`[“"]([^”"]+)[”"]`), regexp.MustCompile(`'([^']+)'`)}
	for _, pattern := range quotePatterns {
		matches := pattern.FindStringSubmatch(content)
		if len(matches) < 2 {
			continue
		}
		title := normalizeCreateTestCaseTitle(matches[1])
		if title != "" {
			return title
		}
	}
	return ""
}

// streamTestCaseCreationResult 用聊天流式协议返回“测试用例已创建成功”的摘要信息。
func streamTestCaseCreationResult(c *gin.Context, title, provider string) {
	item, err := CreateTestCaseRecord(title, provider, "root")
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"code": 1, "msg": err.Error()})
		return
	}

	message := strings.Join([]string{"测试 case 已创建成功。", "原始标题：" + item.Title, "生成标题：" + item.CaseTitle, "优先级：" + item.Priority, "你可以在 [AI 测试用例](/tools/ai-test-case) 菜单查看完整内容。"}, "\n")
	streamAssistantTextResult(c, message)
}

// streamTestCaseHelpResult 用聊天流式协议返回测试用例功能的使用帮助文案。
func streamTestCaseHelpResult(c *gin.Context) {
	message := strings.Join([]string{"你可以直接在聊天窗口输入创建测试 case 命令。", "可用示例：", "1. 帮我建个case：登录失败告警通知异常", "2. 帮我创建测试用例：支付回调超时重试", "3. 标题：订单取消后库存未回滚，生成一个测试case", "命中后我会自动调用后端创建，并把结果保存到 [AI 测试用例](/tools/ai-test-case) 页面。"}, "\n")
	streamAssistantTextResult(c, message)
}
