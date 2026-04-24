// internal/server/server.go
package server

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/rodrigoazlima/localrouter/internal/health"
	"github.com/rodrigoazlima/localrouter/internal/metrics"
	"github.com/rodrigoazlima/localrouter/internal/registry"
	"github.com/rodrigoazlima/localrouter/internal/router"
	"github.com/rodrigoazlima/localrouter/internal/state"
)

type Server struct {
	*http.Server
	router   *router.Router
	monitor  *health.Monitor
	state    *state.Manager
	registry *registry.Registry
	metrics  *metrics.Collector
}

func New(r *router.Router, mon *health.Monitor, st *state.Manager, reg *registry.Registry, m *metrics.Collector, addr string) *Server {
	if addr == "" {
		addr = ":8080"
	}
	s := &Server{router: r, monitor: mon, state: st, registry: reg, metrics: m}

	mux := chi.NewRouter()
	mux.Use(middleware.Recoverer)
	mux.Get("/health", s.handleHealth)
	mux.Get("/metrics", s.handleMetrics)
	mux.Get("/models", s.handleModels)
	mux.Get("/v1/models", s.handleModels)
	mux.Post("/v1/chat/completions", s.handleCompletions)

	s.Server = &http.Server{Addr: addr, Handler: mux}
	return s
}
