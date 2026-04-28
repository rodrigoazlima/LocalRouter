package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/rodrigoazlima/localrouter/internal/config"
	"github.com/rodrigoazlima/localrouter/internal/health"
	"github.com/rodrigoazlima/localrouter/internal/limits"
	"github.com/rodrigoazlima/localrouter/internal/metrics"
	"github.com/rodrigoazlima/localrouter/internal/provider"
	"github.com/rodrigoazlima/localrouter/internal/provider/factory"
	"github.com/rodrigoazlima/localrouter/internal/registry"
	"github.com/rodrigoazlima/localrouter/internal/router"
	"github.com/rodrigoazlima/localrouter/internal/server"
	"github.com/rodrigoazlima/localrouter/internal/state"
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "path to config file")
	port := flag.String("port", "8080", "HTTP listen port")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	m := metrics.New()

	latency := int64(cfg.Routing.LatencyThresholdMs)
	if latency == 0 {
		latency = 2000
	}
	mon := health.New(m, latency)

	providers, limCfgs, modelLimCfgs, recWindows, err := buildProviders(cfg, mon)
	if err != nil {
		log.Fatalf("build providers: %v", err)
	}

	reg := registry.Build(cfg.Providers, cfg.Routing.DefaultModel)
	lim := limits.New(limCfgs)
	lim.SetConcurrencyLimits(concurrencyLimits(cfg))
	modelLim := limits.New(modelLimCfgs)
	modelLim.SetConcurrencyLimits(modelConcurrencyLimits(cfg))

	// Create both managers - original for routing, new one for reporting
	st := state.New(mon)             // Original state manager for routing
	sr := state.NewStateManager(mon) // Extended state manager for reporting

	// Restore persisted routing state (blocked/exhausted/limit windows/active requests) from disk.
	if savedStates, err := state.LoadAllProviderStates(); err == nil {
		now := time.Now()
		for id, ps := range savedStates {
			if ps.BlockedUntil != nil && now.Before(*ps.BlockedUntil) {
				st.BlockUntil(id, *ps.BlockedUntil)
			}
			if ps.ExhaustedUntil != nil && now.Before(*ps.ExhaustedUntil) {
				st.SetExhausted(id, *ps.ExhaustedUntil)
			}
			if len(ps.LimitWindows) > 0 {
				ws := make([]limits.WindowState, len(ps.LimitWindows))
				for i, w := range ps.LimitWindows {
					ws[i] = limits.WindowState{Count: w.Count, ResetAt: w.ResetAt}
				}
				lim.RestoreWindows(id, ws)
			}
			if ps.ActiveRequests > 0 {
				lim.RestoreActiveRequests(id, ps.ActiveRequests)
			}
		}
	}

	// Persist state changes to disk so they survive restarts.
	st.SetSaveHook(func(id string, blockedUntil, exhaustedUntil time.Time) {
		if err := state.UpdateRoutingState(id, blockedUntil, exhaustedUntil); err != nil {
			log.Printf("[STATE] save %s: %v", id, err)
		}
	})
	lim.SetSaveHook(func(id string, ws []limits.WindowState) {
		saves := make([]state.LimitWindowSave, len(ws))
		for i, w := range ws {
			saves[i] = state.LimitWindowSave{Count: w.Count, ResetAt: w.ResetAt}
		}
		if err := state.UpdateLimitWindows(id, saves); err != nil {
			log.Printf("[STATE] save limit %s: %v", id, err)
		}
	})

	// Build set of remote provider IDs for startup probe blocking logic.
	remoteSet := make(map[string]bool, len(reg.RemoteIDs()))
	for _, id := range reg.RemoteIDs() {
		remoteSet[id] = true
	}

	rCfg := router.Config{
		DefaultModel:    cfg.Routing.DefaultModel,
		RecoveryWindows: recWindows,
	}
	// Router uses original state manager for routing decisions
	r := router.New(providers, reg, st, sr, lim, modelLim, m, rCfg)

	// Server needs the new StateManager for reporting
	srv := server.NewWithReport(r, mon, st, sr, reg, m, lim, modelLim, ":"+*port)

	watcher, err := config.NewWatcher(*cfgPath, cfg, func(oldCfg, newCfg *config.Config) {
		newProviders, newLimCfgs, newModelLimCfgs, newRecWindows, err := buildProviders(newCfg, mon)
		if err != nil {
			log.Printf("reload: build providers: %v", err)
			return
		}

		oldIDs := providerIDSet(oldCfg)
		for _, p := range newCfg.Providers {
			if p.Skipped {
				continue
			}
			if !oldIDs[p.ID] {
				prov, err := factory.New(p)
				if err != nil {
					log.Printf("reload: build provider %s: %v", p.ID, err)
					continue
				}
				mon.AddNode(p.ID, prov, providerTimeoutMs(p), 10000)
			}
		}
		newIDs := providerIDSet(newCfg)
		for _, p := range oldCfg.Providers {
			if !newIDs[p.ID] {
				mon.RemoveNode(p.ID)
			}
		}

		newReg := registry.Build(newCfg.Providers, newCfg.Routing.DefaultModel)
		newLim := limits.New(newLimCfgs)
		newLim.SetConcurrencyLimits(concurrencyLimits(newCfg))
		newModelLim := limits.New(newModelLimCfgs)
		newModelLim.SetConcurrencyLimits(modelConcurrencyLimits(newCfg))
		newRCfg := router.Config{
			DefaultModel:    newCfg.Routing.DefaultModel,
			RecoveryWindows: newRecWindows,
		}
		r.Update(newProviders, newReg, sr, newLim, newModelLim, newRCfg)
		log.Printf("[RELOAD] config reloaded")
	})
	if err != nil {
		log.Fatalf("start config watcher: %v", err)
	}
	defer watcher.Stop()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Print(`
   _                    _ ____             _
  | |    ___   ___ __ _| |  _ \ ___  _   _| |_ ___ _ __
  | |   / _ \ / __/ _` + "`" + ` | | |_) / _ \| | | | __/ _ \ '__'
  | |__| (_) | (_| (_| | |  _ < (_) | |_| | ||  __/ |
  |_____\___/ \___\__,_|_|_| \_\___/ \__,_|\__\___|_|
`)
		log.Printf("[INIT] listening on %s", srv.Addr)
		if err := srv.ListenAndServe(); err != nil {
			log.Printf("server: %v", err)
		}
	}()

	if cfg.Logging.IsDebug() {
		log.Printf("[INIT] log level: DEBUG")
	}
	runStartupProbes(context.Background(), providers, mon, st, remoteSet, recWindows, 10000)
	discoverModels(context.Background(), providers, reg, cfg.Routing.DefaultModel)
	logAvailableProviders(cfg, st, reg)

	<-ctx.Done()
	log.Println("shutting down...")
	shutCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	srv.Shutdown(shutCtx) //nolint:errcheck
	mon.Stop()
}

func buildProviders(cfg *config.Config, mon *health.Monitor) (
	map[string]provider.Provider,
	map[string][]limits.Config,
	map[string][]limits.Config,
	map[string]time.Duration,
	error,
) {
	providers := make(map[string]provider.Provider, len(cfg.Providers))
	limCfgs := make(map[string][]limits.Config)
	modelLimCfgs := make(map[string][]limits.Config)
	recWindows := make(map[string]time.Duration)

	for _, p := range cfg.Providers {
		if p.Skipped {
			log.Printf("[DEBUG] %s: skipped (api_key resolves empty)", p.ID)
			continue
		}
		prov, err := factory.New(p)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		providers[p.ID] = prov

		mon.AddNode(p.ID, prov, providerTimeoutMs(p), 10000)

		if len(p.Limits) > 0 {
			cfgs := make([]limits.Config, len(p.Limits))
			for i, e := range p.Limits {
				cfgs[i] = limits.Config{
					Requests: e.Requests,
					Window:   e.WindowDur(),
				}
			}
			limCfgs[p.ID] = cfgs
		}
		for _, m := range p.Models {
			if len(m.Limits) > 0 {
				key := p.ID + "/" + m.ID
				cfgs := make([]limits.Config, len(m.Limits))
				for i, e := range m.Limits {
					cfgs[i] = limits.Config{
						Requests: e.Requests,
						Window:   e.WindowDur(),
					}
				}
				modelLimCfgs[key] = cfgs
			}
		}
		recWindows[p.ID] = p.RecoveryWindowDur()
	}
	return providers, limCfgs, modelLimCfgs, recWindows, nil
}

// runStartupProbes probes all providers concurrently.
// On success: marks the provider ready in the health monitor.
// On failure for remote providers: blocks for the configured recovery_window.
func runStartupProbes(
	ctx context.Context,
	providers map[string]provider.Provider,
	mon *health.Monitor,
	st *state.Manager,
	remoteIDs map[string]bool,
	recWindows map[string]time.Duration,
	timeoutMs int,
) {
	var wg sync.WaitGroup
	for id, p := range providers {
		wg.Add(1)
		go func(id string, p provider.Provider) {
			defer wg.Done()
			pCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
			defer cancel()
			start := time.Now()
			if err := p.HealthCheck(pCtx); err != nil {
				log.Printf("[INIT] %s: probe failed: %v", id, err)
				if remoteIDs[id] {
					d := recWindows[id]
					if d <= 0 {
						d = time.Hour
					}
					st.Block(id, d)
					log.Printf("[INIT] %s: blocked for %s", id, d)
				}
				return
			}
			mon.SetReady(id)
			log.Printf("[INIT] %s: probe OK (%dms)", id, time.Since(start).Milliseconds())
		}(id, p)
	}
	wg.Wait()
}

func discoverModels(ctx context.Context, providers map[string]provider.Provider, reg *registry.Registry, defaultModel string) {
	var wg sync.WaitGroup
	for id, p := range providers {
		lister, ok := p.(provider.ModelLister)
		if !ok {
			continue
		}
		wg.Add(1)
		go func(id string, lister provider.ModelLister) {
			defer wg.Done()
			pCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			models, err := lister.ListModels(pCtx)
			if err != nil {
				log.Printf("[INIT] %s: discover models failed: %v", id, err)
				return
			}
			reg.SetDiscoveredModels(id, models, defaultModel)
			log.Printf("[INIT] %s: discovered %d models", id, len(models))
		}(id, lister)
	}
	wg.Wait()
}

func logAvailableProviders(cfg *config.Config, st *state.Manager, reg *registry.Registry) {
	for _, id := range reg.ProviderIDs() {
		s := st.GetState(id)
		var modelList string
		for i, e := range reg.ForProviderID(id) {
			if i > 0 {
				modelList += " "
			}
			modelList += e.ModelID + "(p=" + strconv.Itoa(e.Priority) + ")"
		}
		if modelList == "" {
			modelList = "(any)"
		}
		log.Printf("[INIT] %s: %s — %s", id, s, modelList)
	}
	if cfg.Routing.DefaultModel != "" {
		log.Printf("[INIT] default model: %s", cfg.Routing.DefaultModel)
	}
}

// updateProviderState updates the report state manager with probe/request results
func updateProviderState(sr *state.StateManager, id string, success bool, latencyMs int64, err error) {
	if err != nil && !success {
		sr.RecordProbeResult(id, false, latencyMs, err)
	} else {
		sr.RecordProbeResult(id, true, latencyMs, nil)
	}
}

func providerIDSet(cfg *config.Config) map[string]bool {
	out := make(map[string]bool, len(cfg.Providers))
	for _, p := range cfg.Providers {
		out[p.ID] = true
	}
	return out
}

func providerTimeoutMs(p config.ProviderConfig) int {
	if p.TimeoutMs > 0 {
		return p.TimeoutMs
	}
	return 30000
}

// concurrencyLimits extracts provider-level concurrent_requests from the first limit entry.
func concurrencyLimits(cfg *config.Config) map[string]int {
	out := make(map[string]int)
	for _, p := range cfg.Providers {
		if p.Skipped {
			continue
		}
		for _, e := range p.Limits {
			if e.ConcurrentRequests > 0 {
				out[p.ID] = e.ConcurrentRequests
				break
			}
		}
	}
	return out
}

// modelConcurrencyLimits extracts per-model concurrent_requests.
func modelConcurrencyLimits(cfg *config.Config) map[string]int {
	out := make(map[string]int)
	for _, p := range cfg.Providers {
		if p.Skipped {
			continue
		}
		for _, m := range p.Models {
			for _, e := range m.Limits {
				if e.ConcurrentRequests > 0 {
					out[p.ID+"/"+m.ID] = e.ConcurrentRequests
					break
				}
			}
		}
	}
	return out
}
