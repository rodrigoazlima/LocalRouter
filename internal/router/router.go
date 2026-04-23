// internal/router/router.go
package router

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"

	"github.com/rodrigoazlima/localrouter/internal/cache"
	"github.com/rodrigoazlima/localrouter/internal/metrics"
	"github.com/rodrigoazlima/localrouter/internal/provider"
	"github.com/rodrigoazlima/localrouter/internal/reqid"
)

var ErrAllProvidersFailed = errors.New("all providers failed or unavailable")

// ReadinessChecker is the subset of health.Monitor used by the router.
type ReadinessChecker interface {
	IsReady(id string) bool
}

type Router struct {
	mu              sync.RWMutex
	locals          []provider.Provider
	remotes         []provider.Provider
	cache           *cache.Cache
	health          ReadinessChecker
	metrics         *metrics.Collector
	fallbackEnabled bool
}

func New(locals, remotes []provider.Provider, c *cache.Cache, h ReadinessChecker, m *metrics.Collector, fallbackEnabled bool) *Router {
	return &Router{locals: locals, remotes: remotes, cache: c, health: h, metrics: m, fallbackEnabled: fallbackEnabled}
}

func (r *Router) Update(locals, remotes []provider.Provider, fallbackEnabled bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.locals = locals
	r.remotes = remotes
	r.fallbackEnabled = fallbackEnabled
}

func (r *Router) Route(ctx context.Context, req *provider.Request) (*provider.Response, error) {
	r.mu.RLock()
	locals := r.locals
	remotes := r.remotes
	fallback := r.fallbackEnabled
	r.mu.RUnlock()

	rid := reqid.From(ctx)
	for _, p := range locals {
		if !r.health.IsReady(p.ID()) {
			continue
		}
		resp, err := p.Complete(ctx, req)
		if err == nil {
			r.metrics.LocalRequests.Add(1)
			r.cache.Reset4xx(p.ID())
			log.Printf("[%s] → %s (%s) model=%q tier=local", rid, p.ID(), p.Endpoint(), resp.Model)
			return resp, nil
		}
		log.Printf("[%s] %s failed: %v", rid, p.ID(), err)
		r.metrics.Tier1Failures.Add(1)
	}

	if !fallback {
		r.metrics.NoCapacity.Add(1)
		return nil, ErrAllProvidersFailed
	}

	for _, p := range remotes {
		if r.cache.IsBlocked(p.ID()) {
			continue
		}
		resp, err := p.Complete(ctx, req)
		if err == nil {
			r.metrics.RemoteRequests.Add(1)
			r.cache.Reset4xx(p.ID())
			log.Printf("[%s] → %s (%s) model=%q tier=remote", rid, p.ID(), p.Endpoint(), resp.Model)
			return resp, nil
		}
		log.Printf("[%s] %s failed: %v", rid, p.ID(), err)
		r.metrics.Tier2Failures.Add(1)
		tier := r.classifyError(p.ID(), err)
		r.cache.Block(p.ID(), tier)
		r.metrics.ProviderBlockEvents.Add(1)
	}

	r.metrics.NoCapacity.Add(1)
	return nil, ErrAllProvidersFailed
}

func (r *Router) Stream(ctx context.Context, req *provider.Request) (<-chan provider.Chunk, error) {
	r.mu.RLock()
	locals := r.locals
	remotes := r.remotes
	fallback := r.fallbackEnabled
	r.mu.RUnlock()

	rid := reqid.From(ctx)
	for _, p := range locals {
		if !r.health.IsReady(p.ID()) {
			continue
		}
		ch, err := p.Stream(ctx, req)
		if err == nil {
			r.metrics.LocalRequests.Add(1)
			log.Printf("[%s] → %s (%s) model=%q tier=local stream=true", rid, p.ID(), p.Endpoint(), req.Model)
			return ch, nil
		}
		log.Printf("[%s] %s failed: %v", rid, p.ID(), err)
		r.metrics.Tier1Failures.Add(1)
	}

	if !fallback {
		r.metrics.NoCapacity.Add(1)
		return nil, ErrAllProvidersFailed
	}

	for _, p := range remotes {
		if r.cache.IsBlocked(p.ID()) {
			continue
		}
		ch, err := p.Stream(ctx, req)
		if err == nil {
			r.metrics.RemoteRequests.Add(1)
			r.cache.Reset4xx(p.ID())
			log.Printf("[%s] → %s (%s) model=%q tier=remote stream=true", rid, p.ID(), p.Endpoint(), req.Model)
			return ch, nil
		}
		log.Printf("[%s] %s failed: %v", rid, p.ID(), err)
		r.metrics.Tier2Failures.Add(1)
		tier := r.classifyError(p.ID(), err)
		r.cache.Block(p.ID(), tier)
		r.metrics.ProviderBlockEvents.Add(1)
	}

	r.metrics.NoCapacity.Add(1)
	return nil, ErrAllProvidersFailed
}

func (r *Router) classifyError(providerID string, err error) cache.Tier {
	var httpErr *provider.HTTPError
	if !errors.As(err, &httpErr) {
		return cache.TierA
	}
	switch httpErr.StatusCode {
	case 429, 529:
		return cache.TierA
	case 401, 403:
		return cache.TierB
	}
	body := strings.ToLower(httpErr.Body)
	if strings.Contains(body, "rate limit") || strings.Contains(body, "overloaded") {
		return cache.TierA
	}
	if httpErr.StatusCode >= 400 && httpErr.StatusCode < 500 {
		return r.cache.Track4xxAndGetTier(providerID)
	}
	return cache.TierA
}
