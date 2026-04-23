// internal/provider/factory/factory.go
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

func NewFromNode(n config.NodeConfig) (provider.Provider, error) {
	switch n.Type {
	case "ollama":
		return ollama.New(n.ID, n.Endpoint, n.APIKey, n.TimeoutMs), nil
	case "openai-compatible":
		return openaicompat.New(n.ID, n.Endpoint, n.APIKey, n.TimeoutMs), nil
	default:
		return nil, fmt.Errorf("unknown node type: %s", n.Type)
	}
}

func NewFromRemote(p config.ProviderConfig) (provider.Provider, error) {
	switch p.Type {
	case "openai-compatible":
		return openaicompat.New(p.ID, p.Endpoint, p.APIKey, 30000), nil
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
