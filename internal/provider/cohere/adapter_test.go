// internal/provider/cohere/adapter_test.go
package cohere_test

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
	"github.com/rodrigoazlima/localrouter/internal/provider/cohere"
)

func TestComplete_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("missing auth header")
		}
		json.NewEncoder(w).Encode(map[string]any{
			"id":      "chat-1",
			"message": map[string]any{"content": []map[string]any{{"type": "text", "text": "Hello!"}}},
			"usage":   map[string]any{"billed_units": map[string]any{"input_tokens": 5, "output_tokens": 3}},
		})
	}))
	defer srv.Close()

	a := cohere.New("coh-1", "test-key", srv.URL, 0, 0)
	resp, err := a.Complete(context.Background(), &provider.Request{
		Model: "command-r", Messages: []provider.Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "Hello!" {
		t.Fatalf("expected Hello!, got %s", resp.Content)
	}
}

func TestComplete_429_ReturnsHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rate limit", http.StatusTooManyRequests)
	}))
	defer srv.Close()

	a := cohere.New("coh-1", "key", srv.URL, 0, 0)
	_, err := a.Complete(context.Background(), &provider.Request{
		Model: "command-r", Messages: []provider.Message{{Role: "user", Content: "Hi"}},
	})
	var httpErr *provider.HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != 429 {
		t.Fatalf("expected 429 HTTPError, got %v", err)
	}
}

func TestStream_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: content-delta\ndata: {\"delta\":{\"message\":{\"content\":{\"delta\":{\"text\":\"He\"}}}}}\n\n")
		fmt.Fprint(w, "event: content-delta\ndata: {\"delta\":{\"message\":{\"content\":{\"delta\":{\"text\":\"llo\"}}}}}\n\n")
		fmt.Fprint(w, "event: message-end\ndata: {\"finish_reason\":\"COMPLETE\"}\n\n")
		w.(http.Flusher).Flush()
	}))
	defer srv.Close()

	a := cohere.New("coh-1", "key", srv.URL, 0, 0)
	ch, err := a.Stream(context.Background(), &provider.Request{
		Model: "command-r", Messages: []provider.Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got strings.Builder
	for chunk := range ch {
		if chunk.Err != nil {
			t.Fatalf("chunk error: %v", chunk.Err)
		}
		got.WriteString(chunk.Delta)
	}
	if got.String() != "Hello" {
		t.Fatalf("expected Hello, got %s", got.String())
	}
}
