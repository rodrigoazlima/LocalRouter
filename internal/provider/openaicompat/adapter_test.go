package openaicompat_test

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
	"github.com/rodrigoazlima/localrouter/internal/provider/openaicompat"
)

func TestComplete_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("missing auth header")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id":    "chatcmpl-1",
			"model": "gpt-4o",
			"choices": []map[string]any{
				{"message": map[string]any{"content": "Hello!"}},
			},
			"usage": map[string]any{"prompt_tokens": 5, "completion_tokens": 3},
		})
	}))
	defer srv.Close()

	a := openaicompat.New("test", srv.URL, "test-key", 3000, 0)
	resp, err := a.Complete(context.Background(), &provider.Request{
		Model:    "gpt-4o",
		Messages: []provider.Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "Hello!" {
		t.Fatalf("expected Hello!, got %s", resp.Content)
	}
	if resp.Usage.PromptTokens != 5 {
		t.Fatalf("expected 5 prompt tokens, got %d", resp.Usage.PromptTokens)
	}
}

func TestComplete_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	defer srv.Close()

	a := openaicompat.New("test", srv.URL, "", 3000, 0)
	_, err := a.Complete(context.Background(), &provider.Request{
		Model:    "gpt-4o",
		Messages: []provider.Message{{Role: "user", Content: "Hi"}},
	})
	var httpErr *provider.HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("expected HTTPError, got %T: %v", err, err)
	}
	if httpErr.StatusCode != 429 {
		t.Fatalf("expected 429, got %d", httpErr.StatusCode)
	}
}

func TestStream_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"He\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"llo\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	}))
	defer srv.Close()

	a := openaicompat.New("test", srv.URL, "", 3000, 0)
	ch, err := a.Stream(context.Background(), &provider.Request{
		Model:    "gpt-4o",
		Messages: []provider.Message{{Role: "user", Content: "Hi"}},
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

func TestHealthCheck_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	a := openaicompat.New("test", srv.URL, "", 3000, 0)
	if err := a.HealthCheck(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
