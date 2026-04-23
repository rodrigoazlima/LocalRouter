// internal/provider/anthropic/adapter_test.go
package anthropic_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rodrigoazlima/localrouter/internal/provider"
	"github.com/rodrigoazlima/localrouter/internal/provider/anthropic"
)

func TestComplete_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "test-key" {
			t.Errorf("missing x-api-key")
		}
		if r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Errorf("missing anthropic-version")
		}
		json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_1", "model": "claude-3-5-sonnet-20241022",
			"content": []map[string]any{{"type": "text", "text": "Hello!"}},
			"usage":   map[string]any{"input_tokens": 5, "output_tokens": 3},
		})
	}))
	defer srv.Close()

	a := anthropic.New("ant-1", "test-key", srv.URL)
	resp, err := a.Complete(context.Background(), &provider.Request{
		Model:    "claude-3-5-sonnet-20241022",
		Messages: []provider.Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "Hello!" {
		t.Fatalf("expected Hello!, got %s", resp.Content)
	}
	if resp.Usage.PromptTokens != 5 {
		t.Fatalf("expected 5, got %d", resp.Usage.PromptTokens)
	}
}

func TestComplete_401_ReturnsHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	a := anthropic.New("ant-1", "bad", srv.URL)
	_, err := a.Complete(context.Background(), &provider.Request{
		Model: "claude-3-5-sonnet-20241022", Messages: []provider.Message{{Role: "user", Content: "Hi"}},
	})
	var httpErr *provider.HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != 401 {
		t.Fatalf("expected 401 HTTPError, got %v", err)
	}
}

func TestStream_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: content_block_delta\ndata: {\"delta\":{\"type\":\"text_delta\",\"text\":\"He\"}}\n\n")
		fmt.Fprint(w, "event: content_block_delta\ndata: {\"delta\":{\"type\":\"text_delta\",\"text\":\"llo\"}}\n\n")
		fmt.Fprint(w, "event: message_stop\ndata: {}\n\n")
		w.(http.Flusher).Flush()
	}))
	defer srv.Close()

	a := anthropic.New("ant-1", "key", srv.URL)
	ch, err := a.Stream(context.Background(), &provider.Request{
		Model: "claude-3-5-sonnet-20241022", Messages: []provider.Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result strings.Builder
	for chunk := range ch {
		if chunk.Err != nil {
			t.Fatalf("chunk error: %v", chunk.Err)
		}
		result.WriteString(chunk.Delta)
	}
	if result.String() != "Hello" {
		t.Fatalf("expected Hello, got %s", result.String())
	}
}
