package aichat

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"ai-case-be/config"
)

// upstreamConfig 是包内统一后的上游模型配置对象。
//
// 它不直接暴露 config.Config，而是把一次请求真正会用到的最小字段收敛到一起，
// 方便聊天链路和测试用例链路共享同一套 provider 调用逻辑。
type upstreamConfig struct {
	Name          string
	APIKey        string
	Model         string
	WireAPI       string
	BaseURL       string
	AuthType      string
	ServiceTier   string
	Reasoning     string
	TLSSkipVerify bool
}

// resolveProviderConfig 根据 provider 名称和全局配置，解析出本次调用应使用的上游配置。
//
// 处理逻辑包括：
// - 默认 provider 选择；
// - 各 provider 的模型与 baseURL 回退值；
// - chat_completions / responses 两类 wire API 的 URL 归一化；
// - Azure 场景的 api-key 鉴权推断；
// - service_tier、reasoning 等附加参数清洗。
func resolveProviderConfig(provider string, cfg config.Config) (upstreamConfig, error) {
	selected := strings.ToLower(strings.TrimSpace(provider))
	if selected == "" {
		selected = "deepseek"
	}

	switch selected {
	case "claude":
		model := cfg.Claude.Model
		if model == "" {
			model = "claude-sonnet-4-6"
		}
		baseURL := cfg.Claude.BaseURL
		if baseURL == "" {
			baseURL = "https://d3op0m6ej5uzjt.cloudfront.net/v1/chat/completions"
		}
		baseURL = normalizeChatCompletionsURL(baseURL, model)
		if cfg.Claude.APIKey == "" {
			return upstreamConfig{}, fmt.Errorf("Claude API key not configured")
		}

		return upstreamConfig{Name: "Claude", APIKey: cfg.Claude.APIKey, Model: model, WireAPI: "chat_completions", BaseURL: baseURL, AuthType: "bearer", TLSSkipVerify: cfg.Claude.TLSSkipVerify}, nil
	case "deepseek":
		apiKey, err := resolveProviderAPIKey(cfg.DeepSeek.APIKey, cfg.DeepSeek.EnvKey)
		if err != nil {
			return upstreamConfig{}, err
		}
		if apiKey == "" {
			return upstreamConfig{}, fmt.Errorf("DeepSeek API key not configured")
		}

		model := strings.TrimSpace(cfg.DeepSeek.Model)
		if model == "" {
			model = "deepseek-chat"
		}

		baseURL := strings.TrimSpace(cfg.DeepSeek.BaseURL)
		if baseURL == "" {
			baseURL = "https://api.deepseek.com"
		}
		baseURL = normalizeChatCompletionsURL(baseURL, model)

		return upstreamConfig{Name: "DeepSeek", APIKey: apiKey, Model: model, WireAPI: "chat_completions", BaseURL: baseURL, AuthType: "bearer", TLSSkipVerify: cfg.DeepSeek.TLSSkipVerify}, nil
	case "codex":
		apiKey, err := resolveProviderAPIKey(cfg.Codex.APIKey, cfg.Codex.EnvKey)
		if err != nil {
			return upstreamConfig{}, err
		}
		if apiKey == "" {
			return upstreamConfig{}, fmt.Errorf("Codex API key not configured")
		}

		model := cfg.Codex.Model
		if model == "" {
			model = "gpt-4o-2024-08-06"
		}
		baseURL := cfg.Codex.BaseURL
		if baseURL == "" {
			return upstreamConfig{}, fmt.Errorf("Codex base_url not configured")
		}

		wireAPI := normalizeCodexWireAPI(cfg.Codex.WireAPI)
		if wireAPI == "responses" {
			baseURL = normalizeResponsesURL(baseURL)
		} else {
			baseURL = normalizeChatCompletionsURL(baseURL, model)
		}

		baseURL = attachAPIVersion(baseURL, cfg.Codex.APIVersion, wireAPI)

		authType := strings.ToLower(strings.TrimSpace(cfg.Codex.AuthType))
		if authType == "" {
			if strings.Contains(strings.ToLower(baseURL), ".openai.azure.com") {
				authType = "api-key"
			} else {
				authType = "bearer"
			}
		}

		return upstreamConfig{
			Name:          "Codex",
			APIKey:        apiKey,
			Model:         model,
			WireAPI:       wireAPI,
			BaseURL:       baseURL,
			AuthType:      authType,
			ServiceTier:   normalizeServiceTier(cfg.Codex.ServiceTier),
			Reasoning:     normalizeReasoningEffort(cfg.Codex.ModelReasoningEffort),
			TLSSkipVerify: cfg.Codex.TLSSkipVerify,
		}, nil
	default:
		return upstreamConfig{}, fmt.Errorf("unsupported provider: %s (only claude/codex/deepseek)", provider)
	}
}

// normalizeUpstreamStatusCode 把上游返回状态码限制在可安全透传的范围内。
//
// 只有明确的 4xx/5xx 会被保留，其他异常值统一降级为 502。
func normalizeUpstreamStatusCode(upstreamStatus int) int {
	if upstreamStatus >= 400 && upstreamStatus <= 599 {
		return upstreamStatus
	}
	return http.StatusBadGateway
}

// resolveProviderAPIKey 按优先级解析 provider 的鉴权密钥。
//
// 优先读取显式配置的 apiKey；若为空，再继续解析 envKey。
func resolveProviderAPIKey(apiKey, envKey string) (string, error) {
	trimmedAPIKey := strings.TrimSpace(apiKey)
	if trimmedAPIKey != "" {
		return trimmedAPIKey, nil
	}
	return resolveEnvOrLiteralAPIKey(envKey)
}

// resolveEnvOrLiteralAPIKey 兼容两种 env_key 配置习惯：
// - env_key 填环境变量名；
// - env_key 直接填历史遗留的真实密钥文本。
func resolveEnvOrLiteralAPIKey(envKey string) (string, error) {
	trimmed := strings.TrimSpace(envKey)
	if trimmed == "" {
		return "", nil
	}
	if value := strings.TrimSpace(os.Getenv(trimmed)); value != "" {
		return value, nil
	}
	if looksLikeEnvVarName(trimmed) {
		return "", fmt.Errorf("environment variable %s is not set", trimmed)
	}
	return trimmed, nil
}

// looksLikeEnvVarName 粗略判断一个字符串更像环境变量名还是直接密钥值。
func looksLikeEnvVarName(value string) bool {
	if value == "" {
		return false
	}
	for index := 0; index < len(value); index++ {
		char := value[index]
		if (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '_' {
			continue
		}
		return false
	}
	return true
}

// setAuthHeader 根据 authType 写入正确的鉴权头。
//
// 当前支持：
// - api-key：适用于 Azure OpenAI 风格网关；
// - bearer：适用于大多数 OpenAI-compatible 接口。
func setAuthHeader(req *http.Request, authType, apiKey string) {
	if strings.ToLower(strings.TrimSpace(authType)) == "api-key" {
		req.Header.Set("api-key", apiKey)
		return
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
}

// normalizeChatCompletionsURL 把输入的 baseURL 归一化到 Chat Completions 端点。
//
// 它兼容：
// - 已完整配置到 /chat/completions 的 URL；
// - 仅配置到 /v1 的 URL；
// - Azure OpenAI 的 /openai 或 /openai/v1 风格路径。
func normalizeChatCompletionsURL(baseURL, model string) string {
	trimmed := strings.TrimSpace(baseURL)
	if trimmed == "" {
		return trimmed
	}
	lower := strings.ToLower(trimmed)
	if strings.Contains(lower, "/chat/completions") {
		return trimmed
	}
	if strings.HasSuffix(lower, "/v1") {
		return strings.TrimRight(trimmed, "/") + "/chat/completions"
	}
	if strings.Contains(lower, ".openai.azure.com") {
		if strings.Contains(lower, "/openai/v1") {
			return strings.TrimRight(trimmed, "/") + "/chat/completions"
		}
		deployment := strings.TrimSpace(model)
		if deployment == "" {
			deployment = "gpt-4o-2024-08-06"
		}
		return strings.TrimRight(trimmed, "/") + "/deployments/" + url.PathEscape(deployment) + "/chat/completions"
	}
	return strings.TrimRight(trimmed, "/") + "/v1/chat/completions"
}

// normalizeResponsesURL 把输入的 baseURL 归一化到 Responses API 端点。
func normalizeResponsesURL(baseURL string) string {
	trimmed := strings.TrimSpace(baseURL)
	if trimmed == "" {
		return trimmed
	}
	lower := strings.ToLower(trimmed)
	if strings.Contains(lower, "/responses") {
		return trimmed
	}
	if strings.HasSuffix(lower, "/v1") {
		return strings.TrimRight(trimmed, "/") + "/responses"
	}
	return strings.TrimRight(trimmed, "/") + "/v1/responses"
}

// normalizeCodexWireAPI 把历史别名和不同写法统一收敛到内部枚举值。
func normalizeCodexWireAPI(wireAPI string) string {
	switch normalized := strings.ToLower(strings.TrimSpace(wireAPI)); normalized {
	case "", "chat", "chat_completions", "chat-completions":
		return "chat_completions"
	case "responses", "response":
		return "responses"
	default:
		return "chat_completions"
	}
}

// attachAPIVersion 仅在确有必要时为 URL 追加 api-version 查询参数。
//
// 这样可以避免 Azure 某些 v1 路径因重复或不兼容的 api-version 参数而报错。
func attachAPIVersion(rawURL, apiVersion, wireAPI string) string {
	version := strings.TrimSpace(apiVersion)
	if version == "" {
		return rawURL
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	if wireAPI == "chat_completions" && strings.Contains(strings.ToLower(parsed.Path), "/v1/chat/completions") {
		return rawURL
	}
	if wireAPI == "responses" && strings.Contains(strings.ToLower(parsed.Path), "/openai/v1/responses") {
		return rawURL
	}
	query := parsed.Query()
	if query.Get("api-version") != "" {
		return rawURL
	}
	query.Set("api-version", version)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

// stripAPIVersion 从 URL 中移除 api-version 查询参数。
//
// 它主要服务于“第一次调用失败后去掉 api-version 再重试”的兼容回退逻辑。
func stripAPIVersion(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	query := parsed.Query()
	query.Del("api-version")
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

// shouldRetryWithoutAPIVersion 判断当前失败是否值得执行“移除 api-version 后重试一次”。
//
// 当前仅针对 Codex + Responses API + 400 场景生效。
func shouldRetryWithoutAPIVersion(cfg upstreamConfig, statusCode int, body string) bool {
	if cfg.Name != "Codex" || cfg.WireAPI != "responses" {
		return false
	}
	if statusCode != http.StatusBadRequest {
		return false
	}
	lowerBody := strings.ToLower(body)
	return strings.Contains(lowerBody, "api version not supported") || strings.Contains(lowerBody, "api-version not supported")
}

// normalizeReasoningEffort 清洗 reasoning 配置，只保留上游 API 支持的枚举值。
//
// 空值表示不传该配置；其余非法值统一回退为空，避免把未知参数透传给上游模型接口。
func normalizeReasoningEffort(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return ""
	case "minimal", "low", "medium", "high":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

// normalizeServiceTier 清洗 service_tier 配置，避免把无效值透传到上游。
func normalizeServiceTier(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return ""
	case "auto", "default", "flex", "priority":
		return strings.ToLower(strings.TrimSpace(value))
	case "fast":
		return ""
	default:
		return ""
	}
}
