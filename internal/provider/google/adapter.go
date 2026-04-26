// internal/provider/google/adapter.go
package google

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
	apiKey       string
	endpoint     string
	client       *http.Client
	streamClient *http.Client
}

func New(id, apiKey, endpoint string, timeoutMs, streamTimeoutMs int) *Adapter {
	if endpoint == "" {
		endpoint = "https://generativelanguage.googleapis.com"
	}
	if timeoutMs <= 0 {
		timeoutMs = 30000
	}
	if streamTimeoutMs <= 0 {
		streamTimeoutMs = timeoutMs
	}
	return &Adapter{
		id:           id,
		apiKey:       apiKey,
		endpoint:     strings.TrimRight(endpoint, "/"),
		client:       &http.Client{Timeout: time.Duration(timeoutMs) * time.Millisecond},
		streamClient: &http.Client{Timeout: time.Duration(streamTimeoutMs) * time.Millisecond},
	}
}

func (a *Adapter) ID() string       { return a.id }
func (a *Adapter) Type() string     { return "google" }
func (a *Adapter) Endpoint() string { return a.endpoint }

type geminiReq struct {
	Contents []geminiContent `json:"contents"`
}
type geminiContent struct {
	Role  string       `json:"role"`
	Parts []geminiPart `json:"parts"`
}
type geminiPart struct{ Text string `json:"text"` }

type geminiResp struct {
	Candidates []struct {
		Content struct{ Parts []geminiPart `json:"parts"` } `json:"content"`
	} `json:"candidates"`
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
	} `json:"usageMetadata"`
}

func (a *Adapter) url(model, action string) string {
	return fmt.Sprintf("%s/v1beta/models/%s:%s", a.endpoint, model, action)
}

func (a *Adapter) Complete(ctx context.Context, req *provider.Request) (*provider.Response, error) {
	body, _ := json.Marshal(geminiReq{Contents: toGemini(req.Messages)})
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.url(req.Model, "generateContent"), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-goog-api-key", a.apiKey)
	resp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, provider.NewHTTPError(resp.StatusCode, resp.Body)
	}
	var r geminiResp
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	var content string
	if len(r.Candidates) > 0 {
		for _, p := range r.Candidates[0].Content.Parts {
			content += p.Text
		}
	}
	return &provider.Response{
		Model: req.Model, Content: content,
		Usage: provider.Usage{PromptTokens: r.UsageMetadata.PromptTokenCount, CompletionTokens: r.UsageMetadata.CandidatesTokenCount},
	}, nil
}

func (a *Adapter) Stream(ctx context.Context, req *provider.Request) (<-chan provider.Chunk, error) {
	body, _ := json.Marshal(geminiReq{Contents: toGemini(req.Messages)})
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.url(req.Model, "streamGenerateContent"), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-goog-api-key", a.apiKey)
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
			var r geminiResp
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &r); err != nil {
				select {
				case ch <- provider.Chunk{Err: fmt.Errorf("parse stream: %w", err)}:
				case <-ctx.Done():
				}
				return
			}
			if len(r.Candidates) > 0 {
				for _, p := range r.Candidates[0].Content.Parts {
					if p.Text != "" {
						select {
						case ch <- provider.Chunk{Delta: p.Text}:
						case <-ctx.Done():
							return
						}
					}
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/v1beta/models", a.endpoint), nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-goog-api-key", a.apiKey)
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

func toGemini(msgs []provider.Message) []geminiContent {
	out := make([]geminiContent, len(msgs))
	for i, m := range msgs {
		role := m.Role
		if role == "assistant" {
			role = "model"
		}
		out[i] = geminiContent{Role: role, Parts: []geminiPart{{Text: m.Content}}}
	}
	return out
}

var _ provider.Provider = (*Adapter)(nil)
