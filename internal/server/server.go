// internal/server/server.go
package server

import (
	"log"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/rodrigoazlima/localrouter/internal/health"
	"github.com/rodrigoazlima/localrouter/internal/limits"
	"github.com/rodrigoazlima/localrouter/internal/metrics"
	"github.com/rodrigoazlima/localrouter/internal/registry"
	"github.com/rodrigoazlima/localrouter/internal/router"
	"github.com/rodrigoazlima/localrouter/internal/state"
)

type Server struct {
	*http.Server
	router      *router.Router
	monitor     *health.Monitor
	state       *state.Manager      // Original state manager for routing
	reportState *state.StateManager // Extended state manager for reporting
	registry    *registry.Registry
	metrics     *metrics.Collector
	limits      *limits.Tracker // provider-level rate/concurrency tracker
	modelLimits *limits.Tracker // per-(provider,model) rate limits
	logPrompts  atomic.Bool
}

func New(r *router.Router, mon *health.Monitor, st *state.Manager, reg *registry.Registry, m *metrics.Collector, lim *limits.Tracker, addr string) *Server {
	return newWithReport(r, mon, st, nil, reg, m, lim, nil, addr)
}

// NewWithReport creates a server with extended reporting capabilities
func NewWithReport(r *router.Router, mon *health.Monitor, st *state.Manager, sr *state.StateManager, reg *registry.Registry, m *metrics.Collector, lim *limits.Tracker, modelLim *limits.Tracker, addr string) *Server {
	return newWithReport(r, mon, st, sr, reg, m, lim, modelLim, addr)
}

func newWithReport(r *router.Router, mon *health.Monitor, st *state.Manager, sr *state.StateManager, reg *registry.Registry, m *metrics.Collector, lim *limits.Tracker, modelLim *limits.Tracker, addr string) *Server {
	if addr == "" {
		addr = ":8080"
	}
	s := &Server{
		router:      r,
		monitor:     mon,
		state:       st,
		reportState: sr,
		registry:    reg,
		metrics:     m,
		limits:      lim,
		modelLimits: modelLim,
	}

	mux := chi.NewRouter()
	mux.Use(middleware.Recoverer)
	mux.Use(requestLogger)
	mux.Get("/health", s.handleHealth)
	mux.Get("/v1/health", s.handleHealth)
	mux.Get("/metrics", s.handleMetrics)
	mux.Get("/models", s.handleModels)
	mux.Get("/v1/models", s.handleModels)
	mux.Post("/v1/chat/completions", s.handleCompletions)
	mux.Post("/chat/completions", s.handleCompletions)
	mux.Get("/report", s.handleReport)

	// Catch-all: log any unmatched routes so we know what Cline is hitting.
	mux.NotFound(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("[HTTP] 404 %s %s (no route)", r.Method, r.URL.Path)
		http.NotFound(w, r)
	})
	mux.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("[HTTP] 405 %s %s (method not allowed)", r.Method, r.URL.Path)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	})

	s.Server = &http.Server{Addr: addr, Handler: mux}
	return s
}

// SetDebug toggles debug logging at runtime (called on config reload).
func (s *Server) SetDebug(enabled bool) {
	s.logPrompts.Store(enabled)
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (sr *statusRecorder) WriteHeader(code int) {
	sr.status = code
	sr.ResponseWriter.WriteHeader(code)
}

func (sr *statusRecorder) Flush() {
	if f, ok := sr.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Printf("[HTTP] %d %s %s (%dms)", rec.status, r.Method, r.URL.Path, time.Since(start).Milliseconds())
	})
}
