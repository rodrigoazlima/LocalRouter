/**
 * Routing integrity after startup initialization.
 *
 * Verifies:
 * - Blocked remotes are skipped (not attempted) during routing.
 * - Available remotes receive traffic in config order.
 * - Healthy local nodes are preferred over remotes.
 * - 503 when all providers are blocked or unavailable.
 * - Local node request-time failures increment failures but do NOT block in state.
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

const CHAT_PAYLOAD = JSON.stringify({
  model: 'mock-model',
  messages: [{ role: 'user', content: 'hello' }],
  stream: false,
});

test('blocked remote (first in config) is skipped; available remote (second) handles request', async () => {
  const c = ctx();
  try {
    const blocked = await startMockServer('server-error-500'); // HealthCheck → 500 → blocked
    const available = await startMockServer('healthy-openai');
    c.mocks.push(blocked, available);
    const routerPort = await getFreePort();

    c.cfgFile = writeConfig({
      locals: [],
      remotes: [
        { id: 'r-blocked', port: blocked.port },
        { id: 'r-available', port: available.port },
      ],
      fallbackEnabled: true,
    });

    c.rp = await startRouter(c.cfgFile, routerPort);
    const { base } = c.rp;

    await waitForHealth(base, {
      remoteProviders: { 'r-blocked': 'blocked', 'r-available': 'missing' },
    });

    const { status, body } = await rawPost(`${base}/v1/chat/completions`, CHAT_PAYLOAD);
    expect(status).toBe(200);
    expect(JSON.parse(body).choices[0].message.content).toBe('Hello from mock.');

    const metrics = await fetchMetrics(base);
    expect(metrics.remote_requests).toBe(1);
    // r-blocked was already blocked before the request; router skips without a new failure event.
    expect(metrics.provider_block_events).toBe(0);
  } finally {
    await teardown(c);
  }
});

test('all remotes blocked → 503 no-capacity', async () => {
  const c = ctx();
  try {
    const blocked1 = await startMockServer('auth-401');
    const blocked2 = await startMockServer('rate-limit-429');
    c.mocks.push(blocked1, blocked2);
    const routerPort = await getFreePort();

    c.cfgFile = writeConfig({
      locals: [],
      remotes: [
        { id: 'rb-1', port: blocked1.port },
        { id: 'rb-2', port: blocked2.port },
      ],
      fallbackEnabled: true,
    });

    c.rp = await startRouter(c.cfgFile, routerPort);
    const { base } = c.rp;

    await waitForHealth(base, {
      remoteProviders: { 'rb-1': 'blocked', 'rb-2': 'blocked' },
    });

    const { status } = await rawPost(`${base}/v1/chat/completions`, CHAT_PAYLOAD);
    expect(status).toBe(503);

    const metrics = await fetchMetrics(base);
    expect(metrics.no_capacity).toBeGreaterThanOrEqual(1);
  } finally {
    await teardown(c);
  }
});

test('healthy local node is preferred over unblocked remote', async () => {
  const c = ctx();
  try {
    const localNode = await startMockServer('healthy-openai');
    const remoteNode = await startMockServer('healthy-openai');
    c.mocks.push(localNode, remoteNode);
    const routerPort = await getFreePort();

    c.cfgFile = writeConfig({
      locals: [
        { id: 'local-ok', type: 'openai-compatible', port: localNode.port, timeoutMs: 1000 },
      ],
      remotes: [{ id: 'r-ok', port: remoteNode.port }],
      fallbackEnabled: true,
    });

    c.rp = await startRouter(c.cfgFile, routerPort);
    const { base } = c.rp;

    await waitForHealth(base, {
      localNodes: { 'local-ok': 'ready' },
      remoteProviders: { 'r-ok': 'missing' },
    });

    const { status } = await rawPost(`${base}/v1/chat/completions`, CHAT_PAYLOAD);
    expect(status).toBe(200);

    const metrics = await fetchMetrics(base);
    expect(metrics.local_requests).toBe(1);
    expect(metrics.remote_requests).toBe(0);
  } finally {
    await teardown(c);
  }
});

test('unavailable local → fallback to remote', async () => {
  const c = ctx();
  try {
    const localPort = await getFreePort(); // nothing listening
    const remoteNode = await startMockServer('healthy-openai');
    c.mocks.push(remoteNode);
    const routerPort = await getFreePort();

    c.cfgFile = writeConfig({
      locals: [
        { id: 'local-down', type: 'openai-compatible', port: localPort, timeoutMs: 300 },
      ],
      remotes: [{ id: 'r-ok', port: remoteNode.port }],
      fallbackEnabled: true,
    });

    c.rp = await startRouter(c.cfgFile, routerPort);
    const { base } = c.rp;

    await waitForHealth(base, {
      localNodes: { 'local-down': 'unavailable' },
      remoteProviders: { 'r-ok': 'missing' },
    });

    const { status } = await rawPost(`${base}/v1/chat/completions`, CHAT_PAYLOAD);
    expect(status).toBe(200);

    const metrics = await fetchMetrics(base);
    expect(metrics.local_requests).toBe(0);
    expect(metrics.remote_requests).toBe(1);
    // Node was not ready → router skipped it → no failures.
    expect(metrics.failures).toBe(0);
  } finally {
    await teardown(c);
  }
});

test('fallback_enabled=false → 503 when all locals unavailable', async () => {
  const c = ctx();
  try {
    const localPort = await getFreePort();
    const routerPort = await getFreePort();

    c.cfgFile = writeConfig({
      locals: [
        { id: 'local-only', type: 'openai-compatible', port: localPort, timeoutMs: 300 },
      ],
      remotes: [],
      fallbackEnabled: false,
    });

    c.rp = await startRouter(c.cfgFile, routerPort);
    const { base } = c.rp;

    await waitForHealth(base, {
      localNodes: { 'local-only': 'unavailable' },
      localStatus: 'unavailable',
    });

    const { status } = await rawPost(`${base}/v1/chat/completions`, CHAT_PAYLOAD);
    expect(status).toBe(503);
  } finally {
    await teardown(c);
  }
});

test('local node failure during routing does not add cache block entry', async () => {
  const c = ctx();
  try {
    // HealthCheck passes → ready; Complete() → 401.
    // Local failures are NOT added to the cache (only remotes are).
    const localNode = await startMockServer('completion-401');
    const remoteNode = await startMockServer('healthy-openai');
    c.mocks.push(localNode, remoteNode);
    const routerPort = await getFreePort();

    c.cfgFile = writeConfig({
      locals: [
        { id: 'local-comp-fail', type: 'openai-compatible', port: localNode.port, timeoutMs: 1000 },
      ],
      remotes: [{ id: 'r-ok', port: remoteNode.port }],
      fallbackEnabled: true,
    });

    c.rp = await startRouter(c.cfgFile, routerPort);
    const { base } = c.rp;

    await waitForHealth(base, {
      localNodes: { 'local-comp-fail': 'ready' },
      remoteProviders: { 'r-ok': 'missing' },
    });

    // Local returns 401 → failures++, router falls to remote → 200.
    const { status } = await rawPost(`${base}/v1/chat/completions`, CHAT_PAYLOAD);
    expect(status).toBe(200);

    const metrics = await fetchMetrics(base);
    expect(metrics.failures).toBe(1);
    expect(metrics.remote_requests).toBe(1);
    expect(metrics.provider_block_events).toBe(0); // locals never block in cache
  } finally {
    await teardown(c);
  }
});
