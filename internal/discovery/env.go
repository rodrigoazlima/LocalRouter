package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"
)

// EnvProvider represents a provider discoverable via environment variable
type EnvProvider struct {
	ID             string `json:"id"`
	Type           string `json:"type"`
	EnvVar         string `json:"env_var"`
	Endpoint       string `json:"endpoint"`
	Priority       int    `json:"priority"`
	IsFree         bool   `json:"is_free"`
	RecoveryWindow string `json:"recovery_window"`
	TimeoutMs      int    `json:"timeout_ms"`
}

// ProviderModels contains provider info and its discovered models
type ProviderModels struct {
	Provider EnvProvider `json:"provider"`
	Models   []string    `json:"models"`
}

var envProviders = []EnvProvider{
	{
		ID:             "openrouter",
		Type:           "openai-compatible",
		EnvVar:         "OPENROUTER_API_KEY",
		Endpoint:       "https://openrouter.ai/api/v1",
		Priority:       10,
		IsFree:         true,
		RecoveryWindow: "15m",
		TimeoutMs:      30000,
	},
	{
		ID:             "groq",
		Type:           "openai-compatible",
		EnvVar:         "GROQ_API_KEY",
		Endpoint:       "https://api.groq.com/openai/v1",
		Priority:       20,
		IsFree:         true,
		RecoveryWindow: "10m",
		TimeoutMs:      30000,
	},
	{
		ID:             "nvidia",
		Type:           "openai-compatible",
		EnvVar:         "NVIDIA_API_KEY",
		Endpoint:       "https://integrate.api.nvidia.com/v1",
		Priority:       30,
		IsFree:         false,
		RecoveryWindow: "10m",
		TimeoutMs:      30000,
	},
	{
		ID:             "github",
		Type:           "openai-compatible",
		EnvVar:         "GITHUB_TOKEN",
		Endpoint:       "https://models.github.ai/v1",
		Priority:       40,
		IsFree:         true,
		RecoveryWindow: "15m",
		TimeoutMs:      30000,
	},
	{
		ID:             "mistral",
		Type:           "openai-compatible",
		EnvVar:         "MISTRAL_API_KEY",
		Endpoint:       "https://api.mistral.ai/v1",
		Priority:       50,
		IsFree:         true,
		RecoveryWindow: "15m",
		TimeoutMs:      30000,
	},
	{
		ID:             "cohere",
		Type:           "cohere",
		EnvVar:         "COHERE_API_KEY",
		Endpoint:       "https://api.cohere.com/v1",
		Priority:       60,
		IsFree:         true,
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
		RecoveryWindow: "10m",
		TimeoutMs:      30000,
	},
	{
		ID:             "siliconflow",
		Type:           "openai-compatible",
		EnvVar:         "SILICONFLOW_API_KEY",
		Endpoint:       "https://api.siliconflow.cn/v1",
		Priority:       90,
		IsFree:         true,
		RecoveryWindow: "10m",
		TimeoutMs:      30000,
	},
	{
		ID:             "google",
		Type:           "google",
		EnvVar:         "GOOGLE_API_KEY",
		Endpoint:       "",
		Priority:       100,
		IsFree:         true,
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

// FetchModels fetches available models from a provider's endpoint.
func FetchModels(ctx context.Context, provider *EnvProvider) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if provider.Endpoint == "" {
		// Providers without endpoints (like Google, Anthropic) return empty model list
		return []string{}, nil
	}

	url := fmt.Sprintf("%s/models", provider.Endpoint)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	var models []string

	switch provider.Type {
	case "openai-compatible":
		models, err = parseOpenAIModels(resp)
	case "ollama":
		models, err = parseOllamaModels(resp)
	default:
		models, err = parseOpenAIModels(resp)
	}

	if err != nil {
		return nil, fmt.Errorf("parse models: %w", err)
	}

	return models, nil
}

// parseOpenAIModels parses models from an OpenAI-compatible /v1/models response.
func parseOpenAIModels(resp *http.Response) ([]string, error) {
	var result struct {
		Object string `json:"object"`
		Data   []struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			Created int64  `json:"created"`
			OwnedBy string `json:"owned_by"`
			Deleted bool   `json:"deleted,omitempty"`
			Root    string `json:"root"`
			Parent  string `json:"parent,omitempty"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}

	models := make([]string, 0)
	for _, m := range result.Data {
		if !m.Deleted && m.ID != "" {
			models = append(models, m.ID)
		}
	}

	return models, nil
}

// parseOllamaModels parses models from an Ollama /api/tags response.
func parseOllamaModels(resp *http.Response) ([]string, error) {
	var result struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}

	models := make([]string, 0, len(result.Models))
	for _, m := range result.Models {
		if m.Name != "" {
			models = append(models, m.Name)
		}
	}

	return models, nil
}

// DiscoverModelsForProviders discovers available models for each provider.
func DiscoverModelsForProviders(ctx context.Context, providers []EnvProvider) ([]ProviderModels, error) {
	results := make([]ProviderModels, 0, len(providers))

	for _, p := range providers {
		models, err := FetchModels(ctx, &p)
		if err != nil {
			return nil, fmt.Errorf("fetch models for %s: %w", p.ID, err)
		}
		results = append(results, ProviderModels{
			Provider: p,
			Models:   models,
		})
	}

	return results, nil
}
