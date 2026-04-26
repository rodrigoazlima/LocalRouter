// internal/provider/google/adapter_test.go
package google_test

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
	"github.com/rodrigoazlima/localrouter/internal/provider/google"
)

func TestComplete_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-goog-api-key") != "test-key" {
			t.Errorf("missing X-goog-api-key header, got: %q", r.Header.Get("X-goog-api-key"))
		}
		if !strings.Contains(r.URL.Path, ":generateContent") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"candidates": []map[string]any{
				{"content": map[string]any{"parts": []map[string]any{{"text": "Hello!"}}, "role": "model"}},
			},
			"usageMetadata": map[string]any{"promptTokenCount": 5, "candidatesTokenCount": 3},
		})
	}))
	defer srv.Close()

	a := google.New("g-1", "test-key", srv.URL, 0, 0)
	resp, err := a.Complete(context.Background(), &provider.Request{
		Model:    "gemini-1.5-flash",
		Messages: []provider.Message{{Role: "user", Content: "Hi"}},
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
		http.Error(w, "quota exceeded", http.StatusTooManyRequests)
	}))
	defer srv.Close()

	a := google.New("g-1", "key", srv.URL, 0, 0)
	_, err := a.Complete(context.Background(), &provider.Request{
		Model: "gemini-1.5-flash", Messages: []provider.Message{{Role: "user", Content: "Hi"}},
	})
	var httpErr *provider.HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != 429 {
		t.Fatalf("expected 429 HTTPError, got %v", err)
	}
}

func TestStream_UsesStreamGenerateContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, ":streamGenerateContent") {
			t.Errorf("expected streamGenerateContent, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"Hi\"}]}}]}\n\n")
		w.(http.Flusher).Flush()
	}))
	defer srv.Close()

	a := google.New("g-1", "key", srv.URL, 0, 0)
	ch, err := a.Stream(context.Background(), &provider.Request{
		Model: "gemini-1.5-flash", Messages: []provider.Message{{Role: "user", Content: "Hi"}},
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
	if got.String() != "Hi" {
		t.Fatalf("expected Hi, got %s", got.String())
	}
}
