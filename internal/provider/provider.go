package provider

import (
	"context"
	"fmt"
	"io"
	"strings"
)

type Provider interface {
	ID() string
	Type() string
	Endpoint() string
	Complete(ctx context.Context, req *Request) (*Response, error)
	Stream(ctx context.Context, req *Request) (<-chan Chunk, error)
	HealthCheck(ctx context.Context) error
}

type Request struct {
	Model    string         `json:"model"`
	Messages []Message      `json:"messages"`
	Stream   bool           `json:"stream,omitempty"`
	Raw      map[string]any `json:"-"`
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Response struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Content string `json:"content"`
	Usage   Usage  `json:"usage"`
}

type Chunk struct {
	Delta string
	Done  bool
	Err   error
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

type HTTPError struct {
	StatusCode int
	Body       string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.Body)
}

func NewHTTPError(code int, body io.Reader) *HTTPError {
	var sb strings.Builder
	if body != nil {
		io.Copy(&sb, io.LimitReader(body, 512))
	}
	return &HTTPError{StatusCode: code, Body: sb.String()}
}
