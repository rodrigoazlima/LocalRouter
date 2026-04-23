// internal/provider/cohere/adapter.go
package cohere

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
	id       string
	apiKey   string
	endpoint string
	client   *http.Client
}

func New(id, apiKey, endpoint string) *Adapter {
	if endpoint == "" {
		endpoint = "https://api.cohere.com"
	}
	return &Adapter{
		id:       id,
		apiKey:   apiKey,
		endpoint: strings.TrimRight(endpoint, "/"),
		client:   &http.Client{Timeout: 30 * time.Second},
	}
}

func (a *Adapter) ID() string   { return a.id }
func (a *Adapter) Type() string { return "cohere" }

type cohReq struct {
	Model    string   `json:"model"`
	Messages []cohMsg `json:"messages"`
	Stream   bool     `json:"stream,omitempty"`
}
type cohMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type cohResp struct {
	ID      string `json:"id"`
	Message struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"message"`
	Usage struct {
		BilledUnits struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"billed_units"`
	} `json:"usage"`
}

func (a *Adapter) Complete(ctx context.Context, req *provider.Request) (*provider.Response, error) {
	body, _ := json.Marshal(cohReq{Model: req.Model, Messages: toCoh(req.Messages)})
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint+"/v2/chat", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+a.apiKey)
	resp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, provider.NewHTTPError(resp.StatusCode, resp.Body)
	}
	var r cohResp
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	var content string
	for _, c := range r.Message.Content {
		if c.Type == "text" {
			content += c.Text
		}
	}
	return &provider.Response{
		ID: r.ID, Model: req.Model, Content: content,
		Usage: provider.Usage{PromptTokens: r.Usage.BilledUnits.InputTokens, CompletionTokens: r.Usage.BilledUnits.OutputTokens},
	}, nil
}

type cohDelta struct {
	Delta struct {
		Message struct {
			Content struct {
				Delta struct{ Text string `json:"text"` } `json:"delta"`
			} `json:"content"`
		} `json:"message"`
	} `json:"delta"`
}

func (a *Adapter) Stream(ctx context.Context, req *provider.Request) (<-chan provider.Chunk, error) {
	body, _ := json.Marshal(cohReq{Model: req.Model, Messages: toCoh(req.Messages), Stream: true})
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint+"/v2/chat", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+a.apiKey)
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
				if lastEvent == "message-end" {
					return
				}
				continue
			}
			if lastEvent != "content-delta" || !strings.HasPrefix(line, "data: ") {
				continue
			}
			var d cohDelta
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &d); err != nil {
				select {
				case ch <- provider.Chunk{Err: fmt.Errorf("parse stream: %w", err)}:
				case <-ctx.Done():
				}
				return
			}
			if text := d.Delta.Message.Content.Delta.Text; text != "" {
				select {
				case ch <- provider.Chunk{Delta: text}:
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.endpoint+"/v2/models", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.apiKey)
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return provider.NewHTTPError(resp.StatusCode, nil)
	}
	return nil
}

func toCoh(msgs []provider.Message) []cohMsg {
	out := make([]cohMsg, len(msgs))
	for i, m := range msgs {
		out[i] = cohMsg{Role: m.Role, Content: m.Content}
	}
	return out
}

var _ provider.Provider = (*Adapter)(nil)
