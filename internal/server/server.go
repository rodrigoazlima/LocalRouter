// internal/server/server.go
package server

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/rodrigoazlima/localrouter/internal/cache"
	"github.com/rodrigoazlima/localrouter/internal/health"
	"github.com/rodrigoazlima/localrouter/internal/metrics"
	"github.com/rodrigoazlima/localrouter/internal/router"
)

type Server struct {
	*http.Server
	router  *router.Router
	monitor *health.Monitor
	cache   *cache.Cache
	metrics *metrics.Collector
}

func New(r *router.Router, mon *health.Monitor, c *cache.Cache, m *metrics.Collector) *Server {
	s := &Server{router: r, monitor: mon, cache: c, metrics: m}

	mux := chi.NewRouter()
	mux.Use(middleware.Recoverer)
	mux.Get("/health", s.handleHealth)
	mux.Get("/metrics", s.handleMetrics)
	mux.Post("/v1/chat/completions", s.handleCompletions)

	s.Server = &http.Server{Addr: ":8080", Handler: mux}
	return s
}
