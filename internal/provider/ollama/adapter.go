// internal/provider/ollama/adapter.go
package ollama

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/rodrigoazlima/localrouter/internal/provider"
	"github.com/rodrigoazlima/localrouter/internal/provider/openaicompat"
)

type Adapter struct {
	*openaicompat.Adapter
}

func New(id, endpoint, apiKey string, timeoutMs, streamTimeoutMs int) *Adapter {
	return &Adapter{
		Adapter: openaicompat.New(id, endpoint, apiKey, timeoutMs, streamTimeoutMs),
	}
}

func (a *Adapter) Type() string { return "ollama" }

func (a *Adapter) HealthCheck(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.Endpoint()+"/api/tags", nil)
	if err != nil {
		return err
	}
	resp, err := a.Client().Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ollama health: HTTP %d", resp.StatusCode)
	}
	return nil
}

func (a *Adapter) ListModels(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.Endpoint()+"/api/tags", nil)
	if err != nil {
		return nil, err
	}
	resp, err := a.Client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama list models: HTTP %d", resp.StatusCode)
	}
	var result struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("ollama list models: decode: %w", err)
	}
	names := make([]string, 0, len(result.Models))
	for _, m := range result.Models {
		if m.Name != "" {
			names = append(names, m.Name)
		}
	}
	return names, nil
}

var _ provider.Provider = (*Adapter)(nil)
