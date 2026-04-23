package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rodrigoazlima/localrouter/internal/cache"
	"github.com/rodrigoazlima/localrouter/internal/config"
	"github.com/rodrigoazlima/localrouter/internal/health"
	"github.com/rodrigoazlima/localrouter/internal/metrics"
	"github.com/rodrigoazlima/localrouter/internal/provider"
	"github.com/rodrigoazlima/localrouter/internal/provider/factory"
	"github.com/rodrigoazlima/localrouter/internal/router"
	"github.com/rodrigoazlima/localrouter/internal/server"
	"github.com/rodrigoazlima/localrouter/internal/startup"
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	c := cache.New()
	m := metrics.New()

	locals, err := buildLocals(cfg)
	if err != nil {
		log.Fatalf("build local providers: %v", err)
	}
	remotes, err := buildRemotes(cfg)
	if err != nil {
		log.Fatalf("build remote providers: %v", err)
	}

	latency := int64(cfg.Routing.LatencyThresholdMs)
	if latency == 0 {
		latency = 2000
	}
	mon := health.New(m, latency)
	for _, n := range cfg.Local.Nodes {
		p, err := factory.NewFromNode(n)
		if err != nil {
			log.Fatalf("build health checker for %s: %v", n.ID, err)
		}
		mon.AddNode(n.ID, p, n.TimeoutMs, 10000)
	}

	go startup.Run(context.Background(), locals, remotes, mon, c, 10000)

	r := router.New(locals, remotes, c, mon, m, cfg.Routing.FallbackEnabled)
	srv := server.New(r, mon, c, m)

	watcher, err := config.NewWatcher(*cfgPath, cfg, func(oldCfg, newCfg *config.Config) {
		newLocals, err := buildLocals(newCfg)
		if err != nil {
			log.Printf("reload: build locals: %v", err)
			return
		}
		newRemotes, err := buildRemotes(newCfg)
		if err != nil {
			log.Printf("reload: build remotes: %v", err)
			return
		}
		r.Update(newLocals, newRemotes, newCfg.Routing.FallbackEnabled)

		oldNodes := make(map[string]bool)
		for _, n := range oldCfg.Local.Nodes {
			oldNodes[n.ID] = true
		}
		for _, n := range newCfg.Local.Nodes {
			if !oldNodes[n.ID] {
				p, err := factory.NewFromNode(n)
				if err != nil {
					log.Printf("reload: build node %s: %v", n.ID, err)
					continue
				}
				mon.AddNode(n.ID, p, n.TimeoutMs, 10000)
			}
		}
		newNodes := make(map[string]bool)
		for _, n := range newCfg.Local.Nodes {
			newNodes[n.ID] = true
		}
		for _, n := range oldCfg.Local.Nodes {
			if !newNodes[n.ID] {
				mon.RemoveNode(n.ID)
			}
		}
	})
	if err != nil {
		log.Fatalf("start config watcher: %v", err)
	}
	defer watcher.Stop()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("listening on %s", srv.Addr)
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

func buildLocals(cfg *config.Config) ([]provider.Provider, error) {
	out := make([]provider.Provider, 0, len(cfg.Local.Nodes))
	for _, n := range cfg.Local.Nodes {
		p, err := factory.NewFromNode(n)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

func buildRemotes(cfg *config.Config) ([]provider.Provider, error) {
	out := make([]provider.Provider, 0, len(cfg.Remote.Providers))
	for _, p := range cfg.Remote.Providers {
		prov, err := factory.NewFromRemote(p)
		if err != nil {
			return nil, err
		}
		out = append(out, prov)
	}
	return out, nil
}
