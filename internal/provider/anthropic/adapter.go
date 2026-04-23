// internal/provider/anthropic/adapter.go
package anthropic

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

const antVersion = "2023-06-01"
const defaultMaxTokens = 1024

type Adapter struct {
	id       string
	apiKey   string
	endpoint string
	client   *http.Client
}

func New(id, apiKey, endpoint string) *Adapter {
	if endpoint == "" {
		endpoint = "https://api.anthropic.com"
	}
	return &Adapter{
		id:       id,
		apiKey:   apiKey,
		endpoint: strings.TrimRight(endpoint, "/"),
		client:   &http.Client{Timeout: 30 * time.Second},
	}
}

func (a *Adapter) ID() string   { return a.id }
func (a *Adapter) Type() string { return "anthropic" }

type antReq struct {
	Model     string   `json:"model"`
	Messages  []antMsg `json:"messages"`
	MaxTokens int      `json:"max_tokens"`
	Stream    bool     `json:"stream,omitempty"`
}
type antMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type antResp struct {
	ID    string `json:"id"`
	Model string `json:"model"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

func (a *Adapter) Complete(ctx context.Context, req *provider.Request) (*provider.Response, error) {
	body, _ := json.Marshal(antReq{Model: req.Model, Messages: toAnt(req.Messages), MaxTokens: defaultMaxTokens})
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", a.apiKey)
	httpReq.Header.Set("anthropic-version", antVersion)
	resp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, provider.NewHTTPError(resp.StatusCode, resp.Body)
	}
	var r antResp
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	var content string
	for _, c := range r.Content {
		if c.Type == "text" {
			content += c.Text
		}
	}
	return &provider.Response{
		ID: r.ID, Model: r.Model, Content: content,
		Usage: provider.Usage{PromptTokens: r.Usage.InputTokens, CompletionTokens: r.Usage.OutputTokens},
	}, nil
}

type antDelta struct {
	Delta struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"delta"`
}

func (a *Adapter) Stream(ctx context.Context, req *provider.Request) (<-chan provider.Chunk, error) {
	body, _ := json.Marshal(antReq{Model: req.Model, Messages: toAnt(req.Messages), MaxTokens: defaultMaxTokens, Stream: true})
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", a.apiKey)
	httpReq.Header.Set("anthropic-version", antVersion)
	resp, err := a.client.Do(httpReq)
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
		var lastEvent string
		for scanner.Scan() {
			select {
			case <-ctx.Done():
				return
			default:
			}
			line := scanner.Text()
			if strings.HasPrefix(line, "event: ") {
				lastEvent = strings.TrimPrefix(line, "event: ")
				if lastEvent == "message_stop" {
					return
				}
				continue
			}
			if lastEvent != "content_block_delta" || !strings.HasPrefix(line, "data: ") {
				continue
			}
			var d antDelta
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &d); err != nil {
				select {
				case ch <- provider.Chunk{Err: fmt.Errorf("parse stream: %w", err)}:
				case <-ctx.Done():
				}
				return
			}
			if d.Delta.Type == "text_delta" && d.Delta.Text != "" {
				select {
				case ch <- provider.Chunk{Delta: d.Delta.Text}:
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
	req.Header.Set("x-api-key", a.apiKey)
	req.Header.Set("anthropic-version", antVersion)
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return provider.NewHTTPError(resp.StatusCode, nil)
	}
	return nil
}

func toAnt(msgs []provider.Message) []antMsg {
	out := make([]antMsg, len(msgs))
	for i, m := range msgs {
		out[i] = antMsg{Role: m.Role, Content: m.Content}
	}
	return out
}

var _ provider.Provider = (*Adapter)(nil)
