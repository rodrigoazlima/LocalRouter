package openaicompat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/rodrigoazlima/localrouter/internal/provider"
)

type Adapter struct {
	id           string
	endpoint     string
	apiKey       string
	client       *http.Client
	streamClient *http.Client
}

func New(id, endpoint, apiKey string, timeoutMs int) *Adapter {
	if timeoutMs <= 0 {
		timeoutMs = 30000
	}
	return &Adapter{
		id:           id,
		endpoint:     strings.TrimRight(endpoint, "/"),
		apiKey:       apiKey,
		client:       &http.Client{Timeout: time.Duration(timeoutMs) * time.Millisecond},
		streamClient: &http.Client{Timeout: 0}, // no timeout; context cancellation handles teardown
	}
}

func (a *Adapter) ID() string            { return a.id }
func (a *Adapter) Type() string          { return "openai-compatible" }
func (a *Adapter) Endpoint() string      { return a.endpoint }
func (a *Adapter) Client() *http.Client  { return a.client }

type oaiRequest struct {
	Model       string       `json:"model"`
	Messages    []oaiMessage `json:"messages"`
	Stream      bool         `json:"stream,omitempty"`
	Temperature *float64     `json:"temperature,omitempty"`
	TopP        *float64     `json:"top_p,omitempty"`
	MaxTokens   *int         `json:"max_tokens,omitempty"`
	Seed        *int         `json:"seed,omitempty"`
}
type oaiMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type oaiResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Message struct{ Content string `json:"content"` } `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}
type streamChunk struct {
	Choices []struct {
		Delta struct{ Content string `json:"content"` } `json:"delta"`
	} `json:"choices"`
}

func (a *Adapter) Complete(ctx context.Context, req *provider.Request) (*provider.Response, error) {
	body, _ := json.Marshal(oaiRequest{
		Model:       req.Model,
		Messages:    toOAI(req.Messages),
		Temperature: req.Temperature,
		TopP:        req.TopP,
		MaxTokens:   req.MaxTokens,
		Seed:        req.Seed,
	})
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		a.endpoint+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if key := req.APIKey; key != "" {
		httpReq.Header.Set("Authorization", "Bearer "+key)
	} else if a.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+a.apiKey)
	}
	resp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, provider.NewHTTPError(resp.StatusCode, resp.Body)
	}
	var r oaiResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	content := ""
	if len(r.Choices) > 0 {
		content = r.Choices[0].Message.Content
	}
	return &provider.Response{
		ID: r.ID, Model: r.Model, Content: content,
		Usage: provider.Usage{PromptTokens: r.Usage.PromptTokens, CompletionTokens: r.Usage.CompletionTokens},
	}, nil
}

func (a *Adapter) Stream(ctx context.Context, req *provider.Request) (<-chan provider.Chunk, error) {
	body, _ := json.Marshal(oaiRequest{
		Model:       req.Model,
		Messages:    toOAI(req.Messages),
		Stream:      true,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		MaxTokens:   req.MaxTokens,
		Seed:        req.Seed,
	})
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		a.endpoint+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if key := req.APIKey; key != "" {
		httpReq.Header.Set("Authorization", "Bearer "+key)
	} else if a.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+a.apiKey)
	}
	resp, err := a.streamClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		err := provider.NewHTTPError(resp.StatusCode, resp.Body)
		resp.Body.Close()
		return nil, err
	}
	ch := make(chan provider.Chunk, 10)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			select {
			case <-ctx.Done():
				return
			default:
			}
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				return
			}
			var sc streamChunk
			if err := json.Unmarshal([]byte(data), &sc); err != nil {
				select {
				case ch <- provider.Chunk{Err: fmt.Errorf("parse stream: %w", err)}:
				case <-ctx.Done():
				}
				return
			}
			if len(sc.Choices) > 0 && sc.Choices[0].Delta.Content != "" {
				select {
				case ch <- provider.Chunk{Delta: sc.Choices[0].Delta.Content}:
				case <-ctx.Done():
					return
				}
			}
		}
		if err := scanner.Err(); err != nil {
			select {
			case ch <- provider.Chunk{Err: err}:
			case <-ctx.Done():
			}
		}
	}()
	return ch, nil
}

func (a *Adapter) HealthCheck(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.endpoint+"/v1/models", nil)
	if err != nil {
		return err
	}
	if a.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+a.apiKey)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health check: HTTP %d", resp.StatusCode)
	}
	return nil
}

func (a *Adapter) ListModels(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.endpoint+"/v1/models", nil)
	if err != nil {
		return nil, err
	}
	if a.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+a.apiKey)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list models: HTTP %d", resp.StatusCode)
	}
	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("list models: decode: %w", err)
	}
	ids := make([]string, 0, len(result.Data))
	for _, m := range result.Data {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	return ids, nil
}

func toOAI(msgs []provider.Message) []oaiMessage {
	out := make([]oaiMessage, len(msgs))
	for i, m := range msgs {
		out[i] = oaiMessage{Role: m.Role, Content: m.Content}
	}
	return out
}

var _ provider.Provider = (*Adapter)(nil)
