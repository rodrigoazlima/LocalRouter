/**
 * Startup with mixed local + remote endpoints.
 *
 * Verifies that the startup probe correctly classifies:
 *  - healthy local nodes  → "ready"
 *  - connection-refused local nodes → "unavailable"
 *  - healthy remote providers → NOT blocked (absent from /health remote[])
 *  - failing remote providers → "blocked" (TierA, ~3600 s TTL)
 *
 * Cache state reflects probe results before any LLM request is made.
 */

import { test, expect } from '@playwright/test';
import { startMockServer, getFreePort, MockServer } from '../helpers/mock-server';
import { startRouter, stopRouter, RouterProcess } from '../helpers/localrouter';
import { writeConfig, removeConfig } from '../helpers/config-gen';
import { waitForHealth, fetchHealth, findNode, findRemote } from '../helpers/poll';

interface Ctx {
  rp: RouterProcess | null;
  mocks: MockServer[];
  cfgFile: string;
}

function ctx(): Ctx { return { rp: null, mocks: [], cfgFile: '' }; }

async function teardown(c: Ctx): Promise<void> {
  if (c.rp) { await stopRouter(c.rp); c.rp = null; }
  for (const m of c.mocks) { await m.close(); }
  c.mocks.length = 0;
  if (c.cfgFile) { removeConfig(c.cfgFile); c.cfgFile = ''; }
}

test('valid local nodes are marked ready after startup probe', async () => {
  const c = ctx();
  try {
    const openaiNode = await startMockServer('healthy-openai');
    const ollamaNode = await startMockServer('healthy-ollama');
    const refusedPort = await getFreePort();
    const remoteOk = await startMockServer('healthy-openai');
    const remoteFail = await startMockServer('server-error-500');
    c.mocks.push(openaiNode, ollamaNode, remoteOk, remoteFail);

    const routerPort = await getFreePort();
    c.cfgFile = writeConfig({
      locals: [
        { id: 'local-oai-ok', type: 'openai-compatible', port: openaiNode.port, timeoutMs: 500 },
        { id: 'local-ollama-ok', type: 'ollama', port: ollamaNode.port, timeoutMs: 500 },
        { id: 'local-refused', type: 'openai-compatible', port: refusedPort, timeoutMs: 500 },
      ],
      remotes: [
        { id: 'remote-ok', port: remoteOk.port },
        { id: 'remote-fail-500', port: remoteFail.port },
      ],
      fallbackEnabled: true,
    });

    c.rp = await startRouter(c.cfgFile, routerPort);
    const { base } = c.rp;

    const health = await waitForHealth(base, {
      localNodes: {
        'local-oai-ok': 'ready',
        'local-ollama-ok': 'ready',
        'local-refused': 'unavailable',
      },
      remoteProviders: {
        'remote-ok': 'missing',
        'remote-fail-500': 'blocked',
      },
      localStatus: 'healthy',
    });

    expect(health.local.status).toBe('healthy');
    expect(health.local.nodes).toHaveLength(3);

    expect(findNode(health, 'local-oai-ok')?.status).toBe('ready');
    expect(findNode(health, 'local-ollama-ok')?.status).toBe('ready');
    expect(findNode(health, 'local-refused')?.status).toBe('unavailable');

    // Healthy remote: Unblock() deleted cache entry → absent from remote[].
    expect(findRemote(health, 'remote-ok')).toBeUndefined();

    // Failed remote: Block(TierA) → blocked with ~1 h TTL.
    const failRemote = findRemote(health, 'remote-fail-500');
    expect(failRemote).toBeDefined();
    expect(failRemote!.status).toBe('blocked');
    expect(failRemote!.ttl_remaining).toBeGreaterThan(3550);
    expect(failRemote!.ttl_remaining).toBeLessThan(3610);
  } finally {
    await teardown(c);
  }
});

test('all-unavailable locals set overall status to unavailable', async () => {
  const c = ctx();
  try {
    const port1 = await getFreePort();
    const port2 = await getFreePort();
    const routerPort = await getFreePort();

    c.cfgFile = writeConfig({
      locals: [
        { id: 'node-a', type: 'openai-compatible', port: port1, timeoutMs: 300 },
        { id: 'node-b', type: 'openai-compatible', port: port2, timeoutMs: 300 },
      ],
      remotes: [],
      fallbackEnabled: false,
    });

    c.rp = await startRouter(c.cfgFile, routerPort);
    const { base } = c.rp;

    const health = await waitForHealth(base, {
      localNodes: { 'node-a': 'unavailable', 'node-b': 'unavailable' },
      localStatus: 'unavailable',
    });

    expect(health.local.status).toBe('unavailable');
    expect(health.remote).toHaveLength(0);
  } finally {
    await teardown(c);
  }
});

test('multiple failing remotes are all blocked after startup probe', async () => {
  const c = ctx();
  try {
    const fail1 = await startMockServer('auth-401');
    const fail2 = await startMockServer('rate-limit-429');
    const fail3 = await startMockServer('server-error-500');
    c.mocks.push(fail1, fail2, fail3);
    const routerPort = await getFreePort();

    c.cfgFile = writeConfig({
      locals: [],
      remotes: [
        { id: 'r-401', port: fail1.port },
        { id: 'r-429', port: fail2.port },
        { id: 'r-500', port: fail3.port },
      ],
      fallbackEnabled: true,
    });

    c.rp = await startRouter(c.cfgFile, routerPort);
    const { base } = c.rp;

    const health = await waitForHealth(base, {
      remoteProviders: {
        'r-401': 'blocked',
        'r-429': 'blocked',
        'r-500': 'blocked',
      },
    });

    expect(health.remote).toHaveLength(3);
    for (const r of health.remote) {
      expect(r.status).toBe('blocked');
      // All via startup probe openai-compat path → TierA (1 h).
      expect(r.ttl_remaining).toBeGreaterThan(3500);
      expect(r.ttl_remaining).toBeLessThanOrEqual(3600);
    }
  } finally {
    await teardown(c);
  }
});
