/**
 * Timeout handling during startup probe.
 *
 * When a local node's mock server delays longer than the node's configured
 * timeout_ms, the openaicompat http.Client fires first → probe fails →
 * node stays StateUnavailable.
 *
 * For remote providers: no per-provider timeout_ms in config; the 10 s global
 * startup probe context cuts off long delays → blocked for recovery_window.
 */

import { test, expect } from '@playwright/test';
import { startMockServer, getFreePort, MockServer } from '../helpers/mock-server';
import { startRouter, stopRouter, RouterProcess } from '../helpers/localrouter';
import { writeConfig, removeConfig } from '../helpers/config-gen';
import { waitForHealth, findNode, findRemote } from '../helpers/poll';

interface Ctx { rp: RouterProcess | null; mocks: MockServer[]; cfgFile: string; }
function ctx(): Ctx { return { rp: null, mocks: [], cfgFile: '' }; }
async function teardown(c: Ctx): Promise<void> {
  if (c.rp) { await stopRouter(c.rp); c.rp = null; }
  for (const m of c.mocks) { await m.close(); }
  c.mocks.length = 0;
  if (c.cfgFile) { removeConfig(c.cfgFile); c.cfgFile = ''; }
}

test('local node that times out during startup probe is marked unavailable', async () => {
  const c = ctx();
  try {
    // Delay = 2000 ms; node timeout_ms = 300 ms → http.Client fires at 300 ms.
    const slow = await startMockServer('slow-response', 2000);
    c.mocks.push(slow);
    const routerPort = await getFreePort();

    c.cfgFile = writeConfig({
      locals: [
        { id: 'local-timeout', type: 'openai-compatible', port: slow.port, timeoutMs: 300 },
      ],
      remotes: [],
      fallbackEnabled: false,
    });

    c.rp = await startRouter(c.cfgFile, routerPort);
    const { base } = c.rp;

    const health = await waitForHealth(base, {
      localNodes: { 'local-timeout': 'unavailable' },
      localStatus: 'unavailable',
    }, 8_000);

    expect(findNode(health, 'local-timeout')?.status).toBe('unavailable');
    expect(health.local.status).toBe('unavailable');
  } finally {
    await teardown(c);
  }
});

test('healthy local alongside timeout node → overall status healthy', async () => {
  const c = ctx();
  try {
    const slow = await startMockServer('slow-response', 2000);
    const ok = await startMockServer('healthy-openai');
    c.mocks.push(slow, ok);
    const routerPort = await getFreePort();

    c.cfgFile = writeConfig({
      locals: [
        { id: 'local-slow', type: 'openai-compatible', port: slow.port, timeoutMs: 300 },
        { id: 'local-ok', type: 'openai-compatible', port: ok.port, timeoutMs: 1000 },
      ],
      remotes: [],
      fallbackEnabled: false,
    });

    c.rp = await startRouter(c.cfgFile, routerPort);
    const { base } = c.rp;

    const health = await waitForHealth(base, {
      localNodes: { 'local-slow': 'unavailable', 'local-ok': 'ready' },
      localStatus: 'healthy',
    }, 8_000);

    expect(health.local.status).toBe('healthy');
    expect(findNode(health, 'local-slow')?.status).toBe('unavailable');
    expect(findNode(health, 'local-ok')?.status).toBe('ready');
  } finally {
    await teardown(c);
  }
});

test('remote provider that times out during startup probe is blocked for recovery_window', async () => {
  // openaicompat remote uses 30 s http.Client; startup probe wraps in 10 s context.
  // Use a 15 s mock delay so the 10 s probe context fires first.
  // NOTE: this test takes ~10 s.
  const c = ctx();
  test.setTimeout(30_000);
  try {
    const slow = await startMockServer('slow-response', 15_000);
    c.mocks.push(slow);
    const routerPort = await getFreePort();

    c.cfgFile = writeConfig({
      locals: [],
      remotes: [{ id: 'r-timeout', port: slow.port }],
      fallbackEnabled: true,
    });

    c.rp = await startRouter(c.cfgFile, routerPort);
    const { base } = c.rp;

    const health = await waitForHealth(base, {
      remoteProviders: { 'r-timeout': 'blocked' },
    }, 20_000);

    const remote = findRemote(health, 'r-timeout');
    expect(remote!.status).toBe('blocked');
    // Context cancellation → blocked for recovery_window (default 1 h).
    expect(remote!.ttl_remaining).toBeGreaterThan(3500);
    expect(remote!.ttl_remaining).toBeLessThanOrEqual(3600);
  } finally {
    await teardown(c);
  }
});

test('connection refused on remote → blocked for recovery_window immediately', async () => {
  const c = ctx();
  try {
    const refusedPort = await getFreePort();
    const routerPort = await getFreePort();

    c.cfgFile = writeConfig({
      locals: [],
      remotes: [{ id: 'r-refused', port: refusedPort }],
      fallbackEnabled: true,
    });

    c.rp = await startRouter(c.cfgFile, routerPort);
    const { base } = c.rp;

    const health = await waitForHealth(base, {
      remoteProviders: { 'r-refused': 'blocked' },
    });

    const remote = findRemote(health, 'r-refused');
    expect(remote!.status).toBe('blocked');
    expect(remote!.ttl_remaining).toBeGreaterThan(3500);
    expect(remote!.ttl_remaining).toBeLessThanOrEqual(3600);
  } finally {
    await teardown(c);
  }
});
