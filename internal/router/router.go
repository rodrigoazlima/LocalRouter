package router

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/rodrigoazlima/localrouter/internal/limits"
	"github.com/rodrigoazlima/localrouter/internal/metrics"
	"github.com/rodrigoazlima/localrouter/internal/provider"
	"github.com/rodrigoazlima/localrouter/internal/registry"
	"github.com/rodrigoazlima/localrouter/internal/reqid"
	"github.com/rodrigoazlima/localrouter/internal/state"
)

var (
	ErrModelNotFound      = errors.New("model not found or no providers configured")
	ErrAllProvidersFailed = errors.New("all providers failed or unavailable")
)

// tierADuration is the short block duration applied to transient failures and startup probe failures.
const tierADuration = time.Hour

// tierBDuration is the long block duration applied to auth failures (HTTP 401/403) at request time.
const tierBDuration = 24 * time.Hour

type Config struct {
	DefaultModel    string
	RecoveryWindows map[string]time.Duration
}

type Router struct {
	mu        sync.RWMutex
	providers map[string]provider.Provider
	registry  *registry.Registry
	state     *state.Manager
	limits    *limits.Tracker
	metrics   *metrics.Collector
	cfg       Config
}

func New(
	providers map[string]provider.Provider,
	reg *registry.Registry,
	st *state.Manager,
	lim *limits.Tracker,
	m *metrics.Collector,
	cfg Config,
) *Router {
	return &Router{
		providers: providers,
		registry:  reg,
		state:     st,
		limits:    lim,
		metrics:   m,
		cfg:       cfg,
	}
}

func (r *Router) Update(providers map[string]provider.Provider, reg *registry.Registry, lim *limits.Tracker, cfg Config) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers = providers
	r.registry = reg
	r.limits = lim
	r.cfg = cfg
}

func (r *Router) resolve(model string) []registry.Entry {
	r.mu.RLock()
	reg := r.registry
	defaultModel := r.cfg.DefaultModel
	r.mu.RUnlock()

	switch model {
	case "":
		return reg.ForModel(defaultModel)
	case "auto":
		return reg.GlobalList()
	default:
		return reg.ForModel(model)
	}
}

func (r *Router) selectProvider(entries []registry.Entry) (provider.Provider, registry.Entry, error) {
	r.mu.RLock()
	providers := r.providers
	r.mu.RUnlock()

	for _, e := range entries {
		if r.state.GetState(e.ProviderID) != state.StateAvailable {
			continue
		}
		p, ok := providers[e.ProviderID]
		if !ok {
			continue
		}
		exhausted, resetAt := r.limits.Record(e.ProviderID)
		if exhausted {
			r.state.SetExhausted(e.ProviderID, resetAt)
			r.metrics.ProviderExhaustedEvents.Add(1)
			continue
		}
		return p, e, nil
	}
	return nil, registry.Entry{}, ErrAllProvidersFailed
}

// classifyError returns the block duration for a provider error.
// HTTP 401/403 at request time → TierB (24 h); everything else → TierA (1 h).
func classifyError(err error) time.Duration {
	var httpErr *provider.HTTPError
	if errors.As(err, &httpErr) {
		if httpErr.StatusCode == 401 || httpErr.StatusCode == 403 {
			return tierBDuration
		}
	}
	return tierADuration
}

func (r *Router) Route(ctx context.Context, req *provider.Request) (*provider.Response, error) {
	rid := reqid.From(ctx)
	entries := r.resolve(req.Model)
	if len(entries) == 0 {
		r.metrics.NoCapacity.Add(1)
		return nil, ErrModelNotFound
	}

	for len(entries) > 0 {
		p, entry, err := r.selectProvider(entries)
		if err != nil {
			r.metrics.NoCapacity.Add(1)
			return nil, ErrAllProvidersFailed
		}

		reqCopy := *req
		reqCopy.Model = entry.ModelID

		resp, err := p.Complete(ctx, &reqCopy)
		if err != nil {
			log.Printf("[%s] %s failed: %v", rid, p.ID(), err)
			r.metrics.Failures.Add(1)
			if entry.IsRemote {
				r.metrics.Tier2Failures.Add(1)
			} else {
				r.metrics.Tier1Failures.Add(1)
			}
			blockDur := classifyError(err)
			r.state.Block(p.ID(), blockDur)
			r.metrics.ProviderBlockEvents.Add(1)
			entries = filterProvider(entries, p.ID())
			continue
		}

		r.metrics.Requests.Add(1)
		if entry.IsRemote {
			r.metrics.RemoteRequests.Add(1)
		} else {
			r.metrics.LocalRequests.Add(1)
		}
		log.Printf("[%s] → %s model=%q", rid, p.ID(), resp.Model)
		return resp, nil
	}

	r.metrics.NoCapacity.Add(1)
	return nil, ErrAllProvidersFailed
}

func (r *Router) Stream(ctx context.Context, req *provider.Request) (<-chan provider.Chunk, error) {
	rid := reqid.From(ctx)
	entries := r.resolve(req.Model)
	if len(entries) == 0 {
		r.metrics.NoCapacity.Add(1)
		return nil, ErrModelNotFound
	}

	for len(entries) > 0 {
		p, entry, err := r.selectProvider(entries)
		if err != nil {
			r.metrics.NoCapacity.Add(1)
			return nil, ErrAllProvidersFailed
		}

		reqCopy := *req
		reqCopy.Model = entry.ModelID

		ch, err := p.Stream(ctx, &reqCopy)
		if err != nil {
			log.Printf("[%s] %s failed: %v", rid, p.ID(), err)
			r.metrics.Failures.Add(1)
			if entry.IsRemote {
				r.metrics.Tier2Failures.Add(1)
			} else {
				r.metrics.Tier1Failures.Add(1)
			}
			blockDur := classifyError(err)
			r.state.Block(p.ID(), blockDur)
			r.metrics.ProviderBlockEvents.Add(1)
			entries = filterProvider(entries, p.ID())
			continue
		}

		r.metrics.Requests.Add(1)
		if entry.IsRemote {
			r.metrics.RemoteRequests.Add(1)
		} else {
			r.metrics.LocalRequests.Add(1)
		}
		log.Printf("[%s] → %s model=%q stream=true", rid, p.ID(), reqCopy.Model)
		return ch, nil
	}

	r.metrics.NoCapacity.Add(1)
	return nil, ErrAllProvidersFailed
}

func filterProvider(entries []registry.Entry, providerID string) []registry.Entry {
	result := make([]registry.Entry, 0, len(entries))
	for _, e := range entries {
		if e.ProviderID != providerID {
			result = append(result, e)
		}
	}
	return result
}
