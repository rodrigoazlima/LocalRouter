package factory

import (
	"fmt"

	"github.com/rodrigoazlima/localrouter/internal/config"
	"github.com/rodrigoazlima/localrouter/internal/provider"
	"github.com/rodrigoazlima/localrouter/internal/provider/anthropic"
	"github.com/rodrigoazlima/localrouter/internal/provider/cohere"
	"github.com/rodrigoazlima/localrouter/internal/provider/google"
	"github.com/rodrigoazlima/localrouter/internal/provider/ollama"
	"github.com/rodrigoazlima/localrouter/internal/provider/openaicompat"
)

const defaultTimeoutMs = 30000

// New creates a provider.Provider from a ProviderConfig.
// Returns an error if the type is unknown.
func New(p config.ProviderConfig) (provider.Provider, error) {
	timeoutMs := p.TimeoutMs
	if timeoutMs <= 0 {
		timeoutMs = defaultTimeoutMs
	}
	switch p.Type {
	case "ollama":
		return ollama.New(p.ID, p.Endpoint, p.APIKey, timeoutMs), nil
	case "openai-compatible", "mistral":
		return openaicompat.New(p.ID, p.Endpoint, p.APIKey, timeoutMs), nil
	case "anthropic":
		return anthropic.New(p.ID, p.APIKey, ""), nil
	case "google":
		return google.New(p.ID, p.APIKey, ""), nil
	case "cohere":
		return cohere.New(p.ID, p.APIKey, ""), nil
	default:
		return nil, fmt.Errorf("unknown provider type: %s", p.Type)
	}
}
