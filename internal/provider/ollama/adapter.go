// internal/provider/ollama/adapter.go
package ollama

import (
	"context"
	"fmt"
	"net/http"

	"github.com/rodrigoazlima/localrouter/internal/provider"
	"github.com/rodrigoazlima/localrouter/internal/provider/openaicompat"
)

type Adapter struct {
	*openaicompat.Adapter
}

func New(id, endpoint, apiKey string, timeoutMs int) *Adapter {
	return &Adapter{
		Adapter: openaicompat.New(id, endpoint, apiKey, timeoutMs),
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

var _ provider.Provider = (*Adapter)(nil)
