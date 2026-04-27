/**
 * Rate-limit (429) handling.
 *
 * Startup probe path:  any failure → blocked for recovery_window (default 1 h).
 * Request-time path:   router.classifyError(429) → blocked for recovery_window (default 1 h).
 *
 * Both paths result in ~1 h block; TTL window must be [3540, 3600].
 */

import { test, expect } from '@playwright/test';
import { startMockServer, getFreePort, MockServer } from '../helpers/mock-server';
import { startRouter, stopRouter, rawPost, RouterProcess } from '../helpers/localrouter';
import { writeConfig, removeConfig } from '../helpers/config-gen';
import { waitForHealth, fetchMetrics, findRemote } from '../helpers/poll';

interface Ctx { rp: RouterProcess | null; mocks: MockServer[]; cfgFile: string; }
function ctx(): Ctx { return { rp: null, mocks: [], cfgFile: '' }; }
async function teardown(c: Ctx): Promise<void> {
  if (c.rp) { await stopRouter(c.rp); c.rp = null; }
  for (const m of c.mocks) { await m.close(); }
  c.mocks.length = 0;
  if (c.cfgFile) { removeConfig(c.cfgFile); c.cfgFile = ''; }
}

test('startup probe 429 → blocked for recovery_window (~1 h)', async () => {
  const c = ctx();
  try {
    const rl = await startMockServer('rate-limit-429');
    c.mocks.push(rl);
    const routerPort = await getFreePort();

    c.cfgFile = writeConfig({
      locals: [],
      remotes: [{ id: 'r-ratelimit', port: rl.port }],
      fallbackEnabled: true,
    });

    c.rp = await startRouter(c.cfgFile, routerPort);
    const { base } = c.rp;

    const health = await waitForHealth(base, { remoteProviders: { 'r-ratelimit': 'blocked' } });
    const remote = findRemote(health, 'r-ratelimit');
    expect(remote!.status).toBe('blocked');
    expect(remote!.ttl_remaining).toBeGreaterThan(3540);
    expect(remote!.ttl_remaining).toBeLessThanOrEqual(3600);
  } finally {
    await teardown(c);
  }
});

test('request-time 429 → blocked for recovery_window and metrics increment', async () => {
  const c = ctx();
  try {
    const rl = await startMockServer('completion-429');
    c.mocks.push(rl);
    const routerPort = await getFreePort();

    c.cfgFile = writeConfig({
      locals: [],
      remotes: [{ id: 'r-comp-429', port: rl.port }],
      fallbackEnabled: true,
    });

    c.rp = await startRouter(c.cfgFile, routerPort);
    const { base } = c.rp;

    await waitForHealth(base, { remoteProviders: { 'r-comp-429': 'missing' } });

    const payload = JSON.stringify({
      model: 'mock-model',
      messages: [{ role: 'user', content: 'ping' }],
      stream: false,
    });

    const { status } = await rawPost(`${base}/v1/chat/completions`, payload);
    // 429 → blocked → no remaining providers → 503.
    expect(status).toBe(503);

    const health = await waitForHealth(base, { remoteProviders: { 'r-comp-429': 'blocked' } });
    const remote = findRemote(health, 'r-comp-429');
    expect(remote!.status).toBe('blocked');
    expect(remote!.ttl_remaining).toBeGreaterThan(3540);
    expect(remote!.ttl_remaining).toBeLessThanOrEqual(3600);

    const metrics = await fetchMetrics(base);
    expect(metrics.failures).toBeGreaterThanOrEqual(1);
    expect(metrics.provider_block_events).toBeGreaterThanOrEqual(1);
    expect(metrics.no_capacity).toBeGreaterThanOrEqual(1);
  } finally {
    await teardown(c);
  }
});

test('rate-limited remote is skipped on next request', async () => {
  const c = ctx();
  try {
    const rl = await startMockServer('completion-429');
    const ok = await startMockServer('healthy-openai');
    c.mocks.push(rl, ok);
    const routerPort = await getFreePort();

    c.cfgFile = writeConfig({
      locals: [],
      remotes: [
        { id: 'r-rl', port: rl.port },
        { id: 'r-ok', port: ok.port },
      ],
      fallbackEnabled: true,
    });

    c.rp = await startRouter(c.cfgFile, routerPort);
    const { base } = c.rp;

    await waitForHealth(base, { remoteProviders: { 'r-rl': 'missing', 'r-ok': 'missing' } });

    const payload = JSON.stringify({
      model: 'mock-model',
      messages: [{ role: 'user', content: 'hello' }],
      stream: false,
    });

    // First request: r-rl → 429 → blocked, falls to r-ok → 200.
    const first = await rawPost(`${base}/v1/chat/completions`, payload);
    expect(first.status).toBe(200);

    await waitForHealth(base, { remoteProviders: { 'r-rl': 'blocked', 'r-ok': 'missing' } });

    // Second request: r-rl is skipped (blocked), r-ok handles it.
    const second = await rawPost(`${base}/v1/chat/completions`, payload);
    expect(second.status).toBe(200);

    const metrics = await fetchMetrics(base);
    expect(metrics.remote_requests).toBe(2);
    expect(metrics.failures).toBe(1);    // only first request's r-rl failure
    expect(metrics.provider_block_events).toBe(1);
  } finally {
    await teardown(c);
  }
});
