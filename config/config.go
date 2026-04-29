package config

import (
	"os"

	"gopkg.in/yaml.v2"
)

type Config struct {
	App      AppConfig      `yaml:"app"`
	Server   ServerConfig   `yaml:"server"`
	MySQL    MySQLConfig    `yaml:"mysql"`
	AITools  AIToolsConfig  `yaml:"ai_tools"`
	Claude   ClaudeConfig   `yaml:"claude"`
	DeepSeek DeepSeekConfig `yaml:"deepseek"`
	Codex    CodexConfig    `yaml:"codex"`
}

// AIToolsConfig 保存内置工具调用能力的治理配置。
type AIToolsConfig struct {
	// Enabled 控制是否整体开启后端内置工具调用能力。
	Enabled *bool `yaml:"enabled"`

	// AllowWrite 控制是否允许模型调用写操作工具。
	AllowWrite *bool `yaml:"allow_write"`

	// AllowAllPermissions 控制是否忽略请求权限头，直接放行所有工具权限校验。
	AllowAllPermissions *bool `yaml:"allow_all_permissions"`

	// AuditEnabled 控制是否记录工具调用审计日志。
	AuditEnabled *bool `yaml:"audit_enabled"`
}

// ClaudeConfig 保存 AI 助手对接公司 Claude 网关所需的配置项。
// 对应 app.yaml 中的 claude 节点。
type ClaudeConfig struct {
	// APIKey 为公司内部 Claude 网关的鉴权密钥，以 Bearer Token 方式传递。
	// 不同于 Anthropic 原生 API 的 x-api-key 头，此处使用 Authorization: Bearer <key>。
	APIKey string `yaml:"api_key"`

	// Model 指定调用的模型名称，需与公司网关支持的模型列表一致。
	// 留空时 handler 自动降级为 "claude-sonnet-4-6"。
	Model string `yaml:"model"`

	// BaseURL 为公司内部 Claude 网关的完整请求路径（含 /v1/chat/completions 后缀）。
	// 留空时使用默认网关地址。切换测试/生产环境时只需修改此项。
	BaseURL string `yaml:"base_url"`

	// TLSSkipVerify 为 true 时跳过 HTTPS 证书校验。
	// 仅用于公司内网存在 SSL 中间人拦截的场景，对外暴露的服务严禁开启。
	TLSSkipVerify bool `yaml:"tls_skip_verify"`
}

// DeepSeekConfig 保存 DeepSeek API 配置。
// 对应 app.yaml 中的 deepseek 节点。
type DeepSeekConfig struct {
	// EnvKey 为上游鉴权密钥（兼容直接写密钥或环境变量名两种方式）。
	EnvKey string `yaml:"env_key"`

	// APIKey 为上游鉴权密钥（推荐字段名）。
	APIKey string `yaml:"api_key"`

	// Model 指定调用模型，留空时默认使用 deepseek-chat。
	Model string `yaml:"model"`

	// BaseURL 为上游完整请求地址或基础地址。
	// 支持 https://api.deepseek.com 和 https://api.deepseek.com/v1 两种写法。
	BaseURL string `yaml:"base_url"`

	// TLSSkipVerify 为 true 时跳过 HTTPS 证书校验。
	TLSSkipVerify bool `yaml:"tls_skip_verify"`
}

// CodexConfig 保存 Codex/OpenAI 网关配置。
// 对应 app.yaml 中的 codex 节点。
type CodexConfig struct {
	// EnvKey 为上游鉴权密钥（兼容历史字段名）。
	// 现在同时支持两种写法：
	// 1) 直接写入真实密钥（兼容旧配置）；
	// 2) 写入环境变量名，例如 "AZURE_OPENAI_API_KEY"（更推荐）。
	EnvKey string `yaml:"env_key"`

	// APIKey 为上游鉴权密钥（推荐字段名）。
	APIKey string `yaml:"api_key"`

	// Model 指定调用模型，留空时由 handler 使用默认值。
	Model string `yaml:"model"`

	// WireAPI 指定上游协议：responses / chat_completions。
	// 建议 Codex 使用 responses（与本地 Codex 配置一致）。
	WireAPI string `yaml:"wire_api"`

	// BaseURL 为上游完整请求地址或基础地址。
	// 若未包含 /chat/completions，handler 会自动补齐到 /v1/chat/completions。
	BaseURL string `yaml:"base_url"`

	// APIVersion 仅在 Azure OpenAI 等需要 api-version 参数时使用。
	APIVersion string `yaml:"api_version"`

	// AuthType 可选值：bearer / api-key。留空时自动推断。
	AuthType string `yaml:"auth_type"`

	// ModelReasoningEffort 对应 Responses API 的 reasoning.effort。
	// 可选：minimal / low / medium / high。
	ModelReasoningEffort string `yaml:"model_reasoning_effort"`

	// ServiceTier 对应 Responses API 的 service_tier。
	ServiceTier string `yaml:"service_tier"`

	// TLSSkipVerify 为 true 时跳过 HTTPS 证书校验（仅内网场景）。
	TLSSkipVerify bool `yaml:"tls_skip_verify"`
}

type AppConfig struct {
	Env string `yaml:"env"`
}
type ServerConfig struct {
	Port int `yaml:"port"`
}

type MySQLConfig struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	DBName   string `yaml:"dbname"`
}

// globalConfig 在进程启动时由 LoadConfig 写入，后续通过 GetConfig 只读访问，无需加锁。
var globalConfig Config

// LoadConfig 从当前工作目录下的 app.yaml 读取并解析配置。
// 应在 main 函数最早期调用，解析结果保存到包级变量供全局使用。
// 返回解析后的 Config 和可能的错误（文件不存在或 YAML 格式错误）。
func LoadConfig() (Config, error) {
	var config Config

	data, err := os.ReadFile("./app.yaml")
	if err != nil {
		return config, err
	}
	err = yaml.Unmarshal(data, &config)
	if err != nil {
		return config, err
	}
	globalConfig = config
	return config, nil
}

// GetConfig 返回最近一次 LoadConfig 解析成功的配置
// 如果还没调用过 LoadConfig，将返回零值 Config
func GetConfig() Config {
	return globalConfig
}
