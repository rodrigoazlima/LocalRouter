package discovery

import (
	"fmt"
	"os"
	"strings"
)

// EnvProvider represents a provider discoverable via environment variable
type EnvProvider struct {
	ID             string
	Type           string
	EnvVar         string
	Endpoint       string
	Priority       int
	IsFree         bool
	Models         []string
	RecoveryWindow string
	TimeoutMs      int
	ChatPath       string
}

var envProviders = []EnvProvider{
	{
		ID:             "openrouter",
		Type:           "openai-compatible",
		EnvVar:         "OPENROUTER_API_KEY",
		Endpoint:       "https://openrouter.ai/api",
		Priority:       10,
		IsFree:         true,
		Models:         []string{"openrouter/free", "openrouter/auto"},
		RecoveryWindow: "15m",
		TimeoutMs:      30000,
	},
	{
		ID:       "groq",
		Type:     "openai-compatible",
		EnvVar:   "GROQ_API_KEY",
		Endpoint: "https://api.groq.com/openai/v1",
		Priority: 20,
		IsFree:   true,
		Models: []string{
			"llama-3.1-8b-instant",
			"llama-3.3-70b-versatile",
			"llama-4-scout-17b-16e-instruct",
			"llama-4-maverick-17b-128e-instruct",
			"deepseek-r1-distill-llama-70b",
			"qwen-qwq-32b",
		},
		RecoveryWindow: "10m",
		TimeoutMs:      30000,
	},
	{
		ID:       "nvidia",
		Type:     "openai-compatible",
		EnvVar:   "NVIDIA_API_KEY",
		Endpoint: "https://integrate.api.nvidia.com/v1",
		Priority: 30,
		IsFree:   false,
		Models: []string{
			"mistralai/devstral-2-123b-instruct-2512",
		},
		RecoveryWindow: "10m",
		TimeoutMs:      30000,
	},
	{
		ID:       "github",
		Type:     "openai-compatible",
		EnvVar:   "GITHUB_TOKEN",
		Endpoint: "https://models.github.ai",
		Priority: 40,
		IsFree:   true,
		Models: []string{
			"openai/gpt-4.1",
			"openai/gpt-4o",
			"openai/gpt-4o-mini",
			"openai/o4-mini",
			"openai/o3-mini",
			"meta-llama/Llama-4-Maverick-17B-128E-Instruct",
		},
		RecoveryWindow: "15m",
		TimeoutMs:      30000,
		ChatPath:       "/inference/chat/completions",
	},
	{
		ID:             "mistral",
		Type:           "openai-compatible",
		EnvVar:         "MISTRAL_API_KEY",
		Endpoint:       "https://api.mistral.ai/v1",
		Priority:       50,
		IsFree:         true,
		Models:         []string{"mistral-small-latest", "open-mistral-nemo", "codestral-latest"},
		RecoveryWindow: "15m",
		TimeoutMs:      30000,
	},
	{
		ID:             "cohere",
		Type:           "cohere",
		EnvVar:         "COHERE_API_KEY",
		Endpoint:       "https://api.cohere.com/",
		Priority:       60,
		IsFree:         true,
		Models:         []string{"command-a-03-2025", "command-r7b-12-2024"},
		RecoveryWindow: "15m",
		TimeoutMs:      30000,
	},
	{
		ID:             "zhipu",
		Type:           "openai-compatible",
		EnvVar:         "ZHIPU_API_KEY",
		Endpoint:       "https://open.bigmodel.cn/api/paas/v4",
		Priority:       70,
		IsFree:         true,
		Models:         []string{"glm-4-flash", "glm-4.5-flash", "glm-4.7-flash"},
		RecoveryWindow: "15m",
		TimeoutMs:      30000,
	},
	{
		ID:             "cerebras",
		Type:           "openai-compatible",
		EnvVar:         "CEREBRAS_API_KEY",
		Endpoint:       "https://api.cerebras.ai/v1",
		Priority:       80,
		IsFree:         true,
		Models:         []string{"qwen-3-235b-a22b-instruct-2507", "qwen-3-235b"},
		RecoveryWindow: "10m",
		TimeoutMs:      30000,
	},
	{
		ID:       "siliconflow",
		Type:     "openai-compatible",
		EnvVar:   "SILICONFLOW_API_KEY",
		Endpoint: "https://api.siliconflow.cn/v1",
		Priority: 90,
		IsFree:   true,
		Models: []string{
			"Qwen/Qwen3-8B",
			"deepseek-ai/DeepSeek-R1",
			"deepseek-ai/DeepSeek-V3",
		},
		RecoveryWindow: "10m",
		TimeoutMs:      30000,
	},
	{
		ID:       "google",
		Type:     "google",
		EnvVar:   "GOOGLE_API_KEY",
		Endpoint: "",
		Priority: 100,
		IsFree:   true,
		Models: []string{
			"gemini-2.5-flash", "gemini-3-flash", "gemma-4-31b-it", "gemma-4-26b-a4b-it",
			"gemini-2.5-flash-lite", "gemini-3.1-flash-lite", "gemma-3-27b-it", "gemma-3-12b-it", "gemma-3-4b-it",
		},
		RecoveryWindow: "15m",
		TimeoutMs:      30000,
	},
	{
		ID:             "anthropic",
		Type:           "anthropic",
		EnvVar:         "ANTHROPIC_KEY",
		Endpoint:       "",
		Priority:       110,
		IsFree:         false,
		Models:         []string{"claude-3-5-haiku-20241022"},
		RecoveryWindow: "10m",
		TimeoutMs:      30000,
	},
	{
		ID:             "deepseek",
		Type:           "openai-compatible",
		EnvVar:         "DEEPSEEK_KEY",
		Endpoint:       "https://api.deepseek.com/v1",
		Priority:       120,
		IsFree:         false,
		Models:         []string{"deepseek-chat"},
		RecoveryWindow: "10m",
		TimeoutMs:      30000,
	},
	{
		ID:             "ollama-cloud",
		Type:           "ollama",
		EnvVar:         "OLLAMA_API_KEY",
		Endpoint:       "https://ollama.com",
		Priority:       130,
		IsFree:         true,
		Models:         []string{"qwen3-coder:480b-cloud", "gpt-oss:120b"},
		RecoveryWindow: "10m",
		TimeoutMs:      30000,
	},
}

// DiscoverFromEnv detects available providers from environment variables.
func DiscoverFromEnv() []EnvProvider {
	discovered := make([]EnvProvider, 0)
	for _, ep := range envProviders {
		if apiKey := os.Getenv(ep.EnvVar); apiKey != "" {
			discovered = append(discovered, ep)
		}
	}
	return discovered
}

// FormatAsYAML returns the provider config formatted as YAML snippet.
func (p EnvProvider) FormatAsYAML() string {
	var lines []string
	lines = append(lines, fmt.Sprintf("  - id: %s", p.ID))
	lines = append(lines, fmt.Sprintf("    type: %s", p.Type))
	if p.Endpoint != "" {
		lines = append(lines, fmt.Sprintf("    endpoint: %s", p.Endpoint))
	}
	lines = append(lines, fmt.Sprintf("    api_key: ${%s}", p.EnvVar))
	if p.ChatPath != "" {
		lines = append(lines, fmt.Sprintf("    chat_path: %s", p.ChatPath))
	}
	lines = append(lines, fmt.Sprintf("    recovery_window: %s", p.RecoveryWindow))
	lines = append(lines, "    models:")

	for i, model := range p.Models {
		prefix := "      - id:"
		if i >= len(p.Models)-1 {
			prefix = "        id:" // last item gets different indentation
		}
		lines = append(lines, fmt.Sprintf("%s %s", prefix, model))
	}

	return strings.Join(lines, "\n")
}

// FormatAsYAMLFull returns the provider config formatted as YAML snippet with full details.
func (p EnvProvider) FormatAsYAMLFull() string {
	var lines []string
	lines = append(lines, fmt.Sprintf("  - id: %s", p.ID))
	lines = append(lines, fmt.Sprintf("    type: %s", p.Type))
	if p.Endpoint != "" {
		lines = append(lines, fmt.Sprintf("    endpoint: %s", p.Endpoint))
	}
	lines = append(lines, fmt.Sprintf("    api_key: ${%s}", p.EnvVar))
	if p.ChatPath != "" {
		lines = append(lines, fmt.Sprintf("    chat_path: %s", p.ChatPath))
	}
	lines = append(lines, fmt.Sprintf("    timeout_ms: %d", p.TimeoutMs))
	lines = append(lines, fmt.Sprintf("    recovery_window: %s", p.RecoveryWindow))
	if p.IsFree {
		lines = append(lines, "    limits:")
		lines = append(lines, "      requests: 100")
		lines = append(lines, "      window: 1m")
	}
	lines = append(lines, "    models:")

	for _, model := range p.Models {
		lines = append(lines, fmt.Sprintf("      - id: %s", model))
		if p.IsFree {
			lines = append(lines, "        is_free: true")
		}
	}

	return strings.Join(lines, "\n")
}
