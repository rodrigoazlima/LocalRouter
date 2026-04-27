/**
 * Cache state correctness.
 *
 * Verifies:
 * - startup probe failure → blocked for recovery_window (default 1 h = ~3600 s)
 * - request-time failure → blocked for recovery_window (default 1 h = ~3600 s)
 * - provider_block_events counter increments per block
 * - no_capacity increments when all providers are blocked
 * - successful startup probe leaves provider absent from remote[]
 */

import { test, expect } from '@playwright/test';
import { startMockServer, getFreePort, MockServer } from '../helpers/mock-server';
import { startRouter, stopRouter, rawPost, RouterProcess } from '../helpers/localrouter';
import { writeConfig, removeConfig } from '../helpers/config-gen';
import {
  waitForHealth,
  fetchMetrics,
  findRemote,
} from '../helpers/poll';

interface Ctx { rp: RouterProcess | null; mocks: MockServer[]; cfgFile: string; }
function ctx(): Ctx { return { rp: null, mocks: [], cfgFile: '' }; }
async function teardown(c: Ctx): Promise<void> {
  if (c.rp) { await stopRouter(c.rp); c.rp = null; }
  for (const m of c.mocks) { await m.close(); }
  c.mocks.length = 0;
  if (c.cfgFile) { removeConfig(c.cfgFile); c.cfgFile = ''; }
}

const CHAT = JSON.stringify({
  model: 'mock-model',
  messages: [{ role: 'user', content: 'test' }],
  stream: false,
});

test('startup probe failure → blocked for recovery_window (~3600 s default)', async () => {
  const c = ctx();
  try {
    const fail = await startMockServer('server-error-500');
    c.mocks.push(fail);
    const routerPort = await getFreePort();

    c.cfgFile = writeConfig({
      locals: [],
      remotes: [{ id: 'r-tiera', port: fail.port }],
      fallbackEnabled: true,
    });

    c.rp = await startRouter(c.cfgFile, routerPort);
    const { base } = c.rp;

    const health = await waitForHealth(base, { remoteProviders: { 'r-tiera': 'blocked' } });
    const remote = findRemote(health, 'r-tiera')!;
    expect(remote.status).toBe('blocked');
    expect(remote.ttl_remaining).toBeGreaterThan(3540);
    expect(remote.ttl_remaining).toBeLessThanOrEqual(3600);
  } finally {
    await teardown(c);
  }
});

test('request-time 401 → blocked for recovery_window (~3600 s default)', async () => {
  const c = ctx();
  try {
    const fail = await startMockServer('completion-401');
    c.mocks.push(fail);
    const routerPort = await getFreePort();

    c.cfgFile = writeConfig({
      locals: [],
      remotes: [{ id: 'r-req-401', port: fail.port }],
      fallbackEnabled: true,
    });

    c.rp = await startRouter(c.cfgFile, routerPort);
    const { base } = c.rp;

    await waitForHealth(base, { remoteProviders: { 'r-req-401': 'missing' } });
    await rawPost(`${base}/v1/chat/completions`, CHAT);

    const health = await waitForHealth(base, { remoteProviders: { 'r-req-401': 'blocked' } });
    const remote = findRemote(health, 'r-req-401')!;
    expect(remote.status).toBe('blocked');
    expect(remote.ttl_remaining).toBeGreaterThan(3540);
    expect(remote.ttl_remaining).toBeLessThanOrEqual(3600);
  } finally {
    await teardown(c);
  }
});

test('startup-blocked and request-blocked providers both use recovery_window', async () => {
  const c = ctx();
  try {
    // r-startup-fail: blocked at startup (probe fails)
    // r-req-fail: HealthCheck passes, Complete → 401 → blocked at request time
    // Both use recovery_window = 1 h (default).
    const startupFail = await startMockServer('rate-limit-429');
    const reqFail = await startMockServer('completion-401');
    c.mocks.push(startupFail, reqFail);
    const routerPort = await getFreePort();

    c.cfgFile = writeConfig({
      locals: [],
      remotes: [
        { id: 'r-startup-fail', port: startupFail.port },
        { id: 'r-req-fail', port: reqFail.port },
      ],
      fallbackEnabled: true,
    });

    c.rp = await startRouter(c.cfgFile, routerPort);
    const { base } = c.rp;

    await waitForHealth(base, {
      remoteProviders: { 'r-startup-fail': 'blocked', 'r-req-fail': 'missing' },
    });

    // r-startup-fail is already blocked; router tries r-req-fail → 401 → blocked.
    await rawPost(`${base}/v1/chat/completions`, CHAT);

    const health = await waitForHealth(base, {
      remoteProviders: { 'r-startup-fail': 'blocked', 'r-req-fail': 'blocked' },
    });

    const startupBlocked = findRemote(health, 'r-startup-fail')!;
    const reqBlocked = findRemote(health, 'r-req-fail')!;

    // Both use recovery_window (default 1 h); startup-blocked has less TTL remaining.
    expect(startupBlocked.ttl_remaining!).toBeLessThanOrEqual(3600);
    expect(reqBlocked.ttl_remaining!).toBeGreaterThan(3540);
    expect(reqBlocked.ttl_remaining!).toBeLessThanOrEqual(3600);
  } finally {
    await teardown(c);
  }
});

test('provider_block_events increments for each request-time failure', async () => {
  const c = ctx();
  try {
    const fail1 = await startMockServer('completion-401');
    const fail2 = await startMockServer('completion-429');
    const ok = await startMockServer('healthy-openai');
    c.mocks.push(fail1, fail2, ok);
    const routerPort = await getFreePort();

    c.cfgFile = writeConfig({
      locals: [],
      remotes: [
        { id: 'r-f1', port: fail1.port },
        { id: 'r-f2', port: fail2.port },
        { id: 'r-ok', port: ok.port },
      ],
      fallbackEnabled: true,
    });

    c.rp = await startRouter(c.cfgFile, routerPort);
    const { base } = c.rp;

    await waitForHealth(base, {
      remoteProviders: { 'r-f1': 'missing', 'r-f2': 'missing', 'r-ok': 'missing' },
    });

    // r-f1 → 401, r-f2 → 429, r-ok → 200.
    const { status } = await rawPost(`${base}/v1/chat/completions`, CHAT);
    expect(status).toBe(200);

    const metrics = await fetchMetrics(base);
    expect(metrics.provider_block_events).toBe(2);
    expect(metrics.failures).toBe(2);
    expect(metrics.remote_requests).toBe(1);
  } finally {
    await teardown(c);
  }
});

test('successful startup probe removes cache block (Unblock path)', async () => {
  const c = ctx();
  try {
    const ok = await startMockServer('healthy-openai');
    c.mocks.push(ok);
    const routerPort = await getFreePort();

    c.cfgFile = writeConfig({
      locals: [],
      remotes: [{ id: 'r-ok-clean', port: ok.port }],
      fallbackEnabled: true,
    });

    c.rp = await startRouter(c.cfgFile, routerPort);
    const { base } = c.rp;

    // Unblock() deletes the cache entry → absent from remote[].
    const health = await waitForHealth(base, {
      remoteProviders: { 'r-ok-clean': 'missing' },
    });

    expect(findRemote(health, 'r-ok-clean')).toBeUndefined();

    const { status } = await rawPost(`${base}/v1/chat/completions`, CHAT);
    expect(status).toBe(200);

    const metrics = await fetchMetrics(base);
    expect(metrics.remote_requests).toBe(1);
    expect(metrics.provider_block_events).toBe(0);
  } finally {
    await teardown(c);
  }
});

test('/metrics no_capacity increments for each 503 (all blocked)', async () => {
  const c = ctx();
  try {
    const fail = await startMockServer('server-error-500');
    c.mocks.push(fail);
    const routerPort = await getFreePort();

    c.cfgFile = writeConfig({
      locals: [],
      remotes: [{ id: 'r-all-blocked', port: fail.port }],
      fallbackEnabled: true,
    });

    c.rp = await startRouter(c.cfgFile, routerPort);
    const { base } = c.rp;

    await waitForHealth(base, { remoteProviders: { 'r-all-blocked': 'blocked' } });

    for (let i = 0; i < 3; i++) {
      const { status } = await rawPost(`${base}/v1/chat/completions`, CHAT);
      expect(status).toBe(503);
    }

    const metrics = await fetchMetrics(base);
    expect(metrics.no_capacity).toBe(3);
    // Provider was already blocked at startup; router skipped it without attempting → 0 failures.
    expect(metrics.failures).toBe(0);
  } finally {
    await teardown(c);
  }
});
