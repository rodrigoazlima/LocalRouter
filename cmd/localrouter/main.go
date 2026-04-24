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

	providers, limCfgs, recWindows, err := buildProviders(cfg, mon)
	if err != nil {
		log.Fatalf("build providers: %v", err)
	}

	runStartupProbes(context.Background(), providers, mon, 10000)

	reg := registry.Build(cfg.Providers, cfg.Routing.DefaultModel)
	lim := limits.New(limCfgs)
	st := state.New(mon)

	rCfg := router.Config{
		DefaultModel:    cfg.Routing.DefaultModel,
		RecoveryWindows: recWindows,
	}
	r := router.New(providers, reg, st, lim, m, rCfg)
	srv := server.New(r, mon, st, reg, m, ":"+*port)

	logAvailableProviders(cfg, st, reg)

	watcher, err := config.NewWatcher(*cfgPath, cfg, func(oldCfg, newCfg *config.Config) {
		newProviders, newLimCfgs, newRecWindows, err := buildProviders(newCfg, mon)
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
		newRCfg := router.Config{
			DefaultModel:    newCfg.Routing.DefaultModel,
			RecoveryWindows: newRecWindows,
		}
		r.Update(newProviders, newReg, newLim, newRCfg)
		log.Printf("[RELOAD] config reloaded")
	})
	if err != nil {
		log.Fatalf("start config watcher: %v", err)
	}
	defer watcher.Stop()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("[INIT] listening on %s", srv.Addr)
		if err := srv.ListenAndServe(); err != nil {
			log.Printf("server: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down...")
	shutCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	srv.Shutdown(shutCtx) //nolint:errcheck
	mon.Stop()
}

func buildProviders(cfg *config.Config, mon *health.Monitor) (
	map[string]provider.Provider,
	map[string]limits.Config,
	map[string]time.Duration,
	error,
) {
	providers := make(map[string]provider.Provider, len(cfg.Providers))
	limCfgs := make(map[string]limits.Config)
	recWindows := make(map[string]time.Duration)

	for _, p := range cfg.Providers {
		if p.Skipped {
			log.Printf("[DEBUG] %s: skipped (api_key set but resolves empty)", p.ID)
			continue
		}
		prov, err := factory.New(p)
		if err != nil {
			return nil, nil, nil, err
		}
		providers[p.ID] = prov

		mon.AddNode(p.ID, prov, providerTimeoutMs(p), 10000)

		if p.Limits != nil {
			limCfgs[p.ID] = limits.Config{
				Requests: p.Limits.Requests,
				Window:   p.Limits.WindowDur(),
			}
		}
		recWindows[p.ID] = p.RecoveryWindowDur()
	}
	return providers, limCfgs, recWindows, nil
}

func runStartupProbes(ctx context.Context, providers map[string]provider.Provider, mon *health.Monitor, timeoutMs int) {
	var wg sync.WaitGroup
	for _, p := range providers {
		wg.Add(1)
		go func(p provider.Provider) {
			defer wg.Done()
			pCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
			defer cancel()
			start := time.Now()
			if err := p.HealthCheck(pCtx); err != nil {
				log.Printf("[INIT] %s: probe failed: %v", p.ID(), err)
				return
			}
			mon.SetReady(p.ID())
			log.Printf("[INIT] %s: probe OK (%dms)", p.ID(), time.Since(start).Milliseconds())
		}(p)
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
		log.Printf("[INIT] %s: %s — %s", id, s, modelList)
	}
	log.Printf("[INIT] default model: %s", cfg.Routing.DefaultModel)
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
