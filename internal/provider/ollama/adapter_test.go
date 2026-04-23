// internal/provider/ollama/adapter_test.go
package ollama_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rodrigoazlima/localrouter/internal/provider"
	"github.com/rodrigoazlima/localrouter/internal/provider/ollama"
)

func TestOllamaHealthCheck_UsesApiTags(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			called = true
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	a := ollama.New("ollama-1", srv.URL, "", 3000)
	if err := a.HealthCheck(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatal("must call /api/tags")
	}
}

func TestOllamaHealthCheck_Non200_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	a := ollama.New("ollama-1", srv.URL, "", 3000)
	if err := a.HealthCheck(context.Background()); err == nil {
		t.Fatal("expected error for non-200")
	}
}

func TestOllamaType(t *testing.T) {
	a := ollama.New("x", "http://localhost", "", 3000)
	if a.Type() != "ollama" {
		t.Fatalf("expected ollama, got %s", a.Type())
	}
}

func TestOllama_ImplementsProvider(t *testing.T) {
	var _ provider.Provider = ollama.New("x", "http://localhost", "", 3000)
}
