package startup

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/rodrigoazlima/localrouter/internal/cache"
	"github.com/rodrigoazlima/localrouter/internal/health"
	"github.com/rodrigoazlima/localrouter/internal/provider"
)

// Run concurrently probes all locals and remotes via HealthCheck.
// Designed to be called as a goroutine: go startup.Run(...).
// Does not check cache or monitor state before probing.
// Does not make LLM requests — all HealthCheck implementations use GET /models-style endpoints.
func Run(ctx context.Context, locals []provider.Provider, remotes []provider.Provider, mon *health.Monitor, c *cache.Cache, timeoutMs int) {
	timeout := time.Duration(timeoutMs) * time.Millisecond
	var wg sync.WaitGroup

	for _, p := range locals {
		wg.Add(1)
		p := p
		go func() {
			defer wg.Done()
			probeCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			start := time.Now()
			err := p.HealthCheck(probeCtx)
			ms := time.Since(start).Milliseconds()
			if err != nil {
				log.Printf("startup probe: local %s: FAIL: %v", p.ID(), err)
				return
			}
			log.Printf("startup probe: local %s: OK (%dms)", p.ID(), ms)
			mon.SetReady(p.ID())
		}()
	}

	for _, p := range remotes {
		wg.Add(1)
		p := p
		go func() {
			defer wg.Done()
			probeCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			start := time.Now()
			err := p.HealthCheck(probeCtx)
			ms := time.Since(start).Milliseconds()
			if err != nil {
				log.Printf("startup probe: remote %s: FAIL: %v", p.ID(), err)
				var httpErr *provider.HTTPError
				if errors.As(err, &httpErr) && (httpErr.StatusCode == 401 || httpErr.StatusCode == 403) {
					c.Block(p.ID(), cache.TierB)
				} else {
					c.Block(p.ID(), cache.TierA)
				}
				return
			}
			log.Printf("startup probe: remote %s: OK (%dms)", p.ID(), ms)
			c.Unblock(p.ID())
		}()
	}

	wg.Wait()
}
