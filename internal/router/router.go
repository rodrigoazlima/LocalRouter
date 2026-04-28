package router

import (
	"context"
	"errors"
	"log"
	"strings"
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

type Config struct {
	DefaultModel    string
	RecoveryWindows map[string]time.Duration
}

type Router struct {
	mu          sync.RWMutex
	providers   map[string]provider.Provider
	registry    *registry.Registry
	state       *state.Manager
	reportState *state.StateManager // extended state manager for reporting (may be nil)
	limits      *limits.Tracker
	modelLimits *limits.Tracker // per-(provider,model) limits; may be nil
	metrics     *metrics.Collector
	cfg         Config
}

func New(
	providers map[string]provider.Provider,
	reg *registry.Registry,
	st *state.Manager,
	reportSt *state.StateManager,
	lim *limits.Tracker,
	modelLim *limits.Tracker,
	m *metrics.Collector,
	cfg Config,
) *Router {
	return &Router{
		providers:   providers,
		registry:    reg,
		state:       st,
		reportState: reportSt,
		limits:      lim,
		modelLimits: modelLim,
		metrics:     m,
		cfg:         cfg,
	}
}

func (r *Router) Update(providers map[string]provider.Provider, reg *registry.Registry, reportSt *state.StateManager, lim *limits.Tracker, modelLim *limits.Tracker, cfg Config) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers = providers
	r.registry = reg
	r.reportState = reportSt
	r.limits = lim
	r.modelLimits = modelLim
	r.cfg = cfg
}

// Providers returns a snapshot of the current provider map.
func (r *Router) Providers() map[string]provider.Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]provider.Provider, len(r.providers))
	for k, v := range r.providers {
		out[k] = v
	}
	return out
}

func (r *Router) resolve(model string) []registry.Entry {
	r.mu.RLock()
	reg := r.registry
	defaultModel := r.cfg.DefaultModel
	r.mu.RUnlock()

	switch model {
	case "":
		if defaultModel == "" {
			return reg.GlobalList()
		}
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
	modelLimits := r.modelLimits
	r.mu.RUnlock()

	for _, e := range entries {
		if r.state.GetState(e.ProviderID) != state.StateAvailable {
			continue
		}
		p, ok := providers[e.ProviderID]
		if !ok {
			continue
		}
		// Check per-model limits before provider-level (avoids spurious provider increments).
		if modelLimits != nil {
			modelKey := e.ProviderID + "/" + e.ModelID
			if modelLimits.IsBlocked(modelKey) {
				continue // upstream cooldown active for this model
			}
			if exhausted, _ := modelLimits.Record(modelKey); exhausted {
				continue // model rate-limited; try next entry, don't block provider
			}
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
// All errors use the provider's configured recovery_window (default 1 h).
func classifyError(_ error, recoveryWindow time.Duration) time.Duration {
	return recoveryWindow
}

// isModelLevelError returns true for HTTP status codes that indicate a model-specific
// failure where other models on the same provider may still succeed (e.g. upstream
// rate-limit on one model, or request too large for a specific model).
func isModelLevelError(err error) bool {
	var httpErr *provider.HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode == 429 || httpErr.StatusCode == 413
	}
	return false
}

// acquireConcurrency attempts to acquire provider and model concurrency slots.
// Returns (modelKey, releaseAll) where releaseAll must be called on every exit path.
// If acquisition fails, returns ("", nil) — caller should skip and continue.
func (r *Router) acquireConcurrency(entry registry.Entry) (modelKey string, release func()) {
	r.mu.RLock()
	modelLimits := r.modelLimits
	r.mu.RUnlock()

	if !r.limits.TryAcquireConcurrency(entry.ProviderID) {
		r.metrics.RecordConcurrencyRejected(entry.ProviderID)
		return "", nil
	}
	r.metrics.AddConcurrentActive(entry.ProviderID, 1)

	mKey := entry.ProviderID + "/" + entry.ModelID
	if modelLimits != nil && !modelLimits.TryAcquireConcurrency(mKey) {
		r.limits.ReleaseConcurrency(entry.ProviderID)
		r.metrics.AddConcurrentActive(entry.ProviderID, -1)
		r.metrics.RecordConcurrencyRejected(entry.ProviderID)
		return "", nil
	}

	rel := func() {
		r.limits.ReleaseConcurrency(entry.ProviderID)
		r.metrics.AddConcurrentActive(entry.ProviderID, -1)
		if modelLimits != nil {
			modelLimits.ReleaseConcurrency(mKey)
		}
	}
	return mKey, rel
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

		_, release := r.acquireConcurrency(entry)
		if release == nil {
			// concurrency full for this provider or model
			entries = filterProvider(entries, entry.ProviderID)
			continue
		}

		reqCopy := *req
		reqCopy.Model = entry.ModelID
		applyModelParams(&reqCopy, entry)

		resp, err := p.Complete(ctx, &reqCopy)
		release()
		if err != nil {
			log.Printf("[%s] %s failed: %v", rid, p.ID(), err)
			if r.reportState != nil {
				r.reportState.RecordRequestFailure(p.ID(), err)
			}
			r.metrics.Failures.Add(1)
			if isModelLevelError(err) {
				entries = filterEntry(entries, entry.ProviderID, entry.ModelID)
				r.mu.RLock()
				modelLimits := r.modelLimits
				r.mu.RUnlock()
				if modelLimits != nil {
					recoveryWindow := r.cfg.RecoveryWindows[p.ID()]
					if recoveryWindow == 0 {
						recoveryWindow = 60 * time.Second
					}
					cooldown := retryAfterDuration(err, recoveryWindow)
					modelKey := entry.ProviderID + "/" + entry.ModelID
					modelLimits.Block(modelKey, time.Now().Add(cooldown))
					log.Printf("[%s] %s/%s cooling down for %s", rid, p.ID(), entry.ModelID, cooldown)
				}
			} else {
				if entry.IsRemote {
					recoveryWindow := r.cfg.RecoveryWindows[p.ID()]
					blockDur := classifyError(err, recoveryWindow)
					r.state.Block(p.ID(), blockDur)
					if r.reportState != nil {
						r.reportState.RecordProbeResult(p.ID(), false, 0, err)
					}
					r.metrics.ProviderBlockEvents.Add(1)
				}
				entries = filterProvider(entries, p.ID())
			}
			continue
		}

		if r.reportState != nil {
			r.reportState.RecordRequestSuccess(p.ID())
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

// Stream routes a streaming request and returns the resolved model name alongside the chunk channel.
func (r *Router) Stream(ctx context.Context, req *provider.Request) (string, <-chan provider.Chunk, error) {
	rid := reqid.From(ctx)

	entries := r.resolve(req.Model)
	if len(entries) == 0 {
		r.metrics.NoCapacity.Add(1)
		return "", nil, ErrModelNotFound
	}

	for len(entries) > 0 {
		p, entry, err := r.selectProvider(entries)
		if err != nil {
			r.metrics.NoCapacity.Add(1)
			return "", nil, ErrAllProvidersFailed
		}

		_, release := r.acquireConcurrency(entry)
		if release == nil {
			entries = filterProvider(entries, entry.ProviderID)
			continue
		}

		reqCopy := *req
		reqCopy.Model = entry.ModelID
		applyModelParams(&reqCopy, entry)

		ch, err := p.Stream(ctx, &reqCopy)
		if err != nil {
			release()
			log.Printf("[%s] %s failed: %v", rid, p.ID(), err)
			if r.reportState != nil {
				r.reportState.RecordRequestFailure(p.ID(), err)
			}
			r.metrics.Failures.Add(1)
			if isModelLevelError(err) {
				entries = filterEntry(entries, entry.ProviderID, entry.ModelID)
				r.mu.RLock()
				modelLimits := r.modelLimits
				r.mu.RUnlock()
				if modelLimits != nil {
					recoveryWindow := r.cfg.RecoveryWindows[p.ID()]
					if recoveryWindow == 0 {
						recoveryWindow = 60 * time.Second
					}
					cooldown := retryAfterDuration(err, recoveryWindow)
					modelKey := entry.ProviderID + "/" + entry.ModelID
					modelLimits.Block(modelKey, time.Now().Add(cooldown))
					log.Printf("[%s] %s/%s cooling down for %s", rid, p.ID(), entry.ModelID, cooldown)
				}
			} else {
				if entry.IsRemote {
					recoveryWindow := r.cfg.RecoveryWindows[p.ID()]
					blockDur := classifyError(err, recoveryWindow)
					r.state.Block(p.ID(), blockDur)
					r.metrics.ProviderBlockEvents.Add(1)
				}
				entries = filterProvider(entries, p.ID())
			}
			continue
		}

		// Wait for first chunk before committing to this provider.
		// If the stream closes empty or yields an error, try next provider.
		first, ok := <-ch
		if !ok {
			release()
			log.Printf("[%s] %s empty stream, trying next", rid, p.ID())
			if r.reportState != nil {
				r.reportState.RecordRequestFailure(p.ID(), errors.New("empty response"))
			}
			r.metrics.Failures.Add(1)
			entries = filterProvider(entries, p.ID())
			continue
		}
		if first.Err != nil {
			release()
			log.Printf("[%s] %s stream error on first chunk: %v", rid, p.ID(), first.Err)
			r.metrics.Failures.Add(1)
			if entry.IsRemote {
				recoveryWindow := r.cfg.RecoveryWindows[p.ID()]
				blockDur := classifyError(first.Err, recoveryWindow)
				r.state.Block(p.ID(), blockDur)
				if r.reportState != nil {
					r.reportState.RecordProbeResult(p.ID(), false, 0, first.Err)
				}
				r.metrics.ProviderBlockEvents.Add(1)
			}
			entries = filterProvider(entries, p.ID())
			continue
		}

		// Prepend the buffered first chunk back onto a new channel.
		// release is called by the goroutine when the upstream channel closes.
		out := make(chan provider.Chunk, 1)
		out <- first
		go func() {
			defer close(out)
			defer release()
			for chunk := range ch {
				out <- chunk
			}
		}()

		if r.reportState != nil {
			r.reportState.RecordRequestSuccess(p.ID())
		}
		r.metrics.Requests.Add(1)
		if entry.IsRemote {
			r.metrics.RemoteRequests.Add(1)
		} else {
			r.metrics.LocalRequests.Add(1)
		}
		log.Printf("[%s] → %s model=%q stream=true", rid, p.ID(), reqCopy.Model)
		return reqCopy.Model, out, nil
	}

	r.metrics.NoCapacity.Add(1)
	return "", nil, ErrAllProvidersFailed
}

// applyModelParams overlays model-level params from the registry entry onto the request.
// Config-level params take precedence over any request-level values.
func applyModelParams(req *provider.Request, e registry.Entry) {
	if e.APIKey != "" {
		req.APIKey = e.APIKey
	}
	if e.Temperature != nil {
		req.Temperature = e.Temperature
	}
	if e.TopP != nil {
		req.TopP = e.TopP
	}
	if e.MaxTokens != nil {
		req.MaxTokens = e.MaxTokens
	}
	if e.Seed != nil {
		req.Seed = e.Seed
	}
}

// retryAfterDuration parses "retry in X.Xs" from a 429 body (Google API format).
// Falls back to fallback when not found or unparseable.
func retryAfterDuration(err error, fallback time.Duration) time.Duration {
	var httpErr *provider.HTTPError
	if !errors.As(err, &httpErr) {
		return fallback
	}
	const marker = "retry in "
	idx := strings.Index(httpErr.Body, marker)
	if idx < 0 {
		return fallback
	}
	raw := httpErr.Body[idx+len(marker):]
	end := strings.IndexAny(raw, " \n\t\"\\")
	if end >= 0 {
		raw = raw[:end]
	}
	raw = strings.TrimRight(raw, ".,;")
	d, parseErr := time.ParseDuration(raw)
	if parseErr != nil || d <= 0 {
		return fallback
	}
	if d < time.Second {
		d = time.Second
	}
	return d
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

func filterEntry(entries []registry.Entry, providerID, modelID string) []registry.Entry {
	result := make([]registry.Entry, 0, len(entries))
	for _, e := range entries {
		if e.ProviderID == providerID && e.ModelID == modelID {
			continue
		}
		result = append(result, e)
	}
	return result
}
