/**
 * Authentication failure handling.
 *
 * Two distinct code paths produce auth-related blocks:
 *
 * 1. Startup probe path (openai-compat HealthCheck):
 *    HealthCheck returns a plain fmt.Errorf, NOT *provider.HTTPError.
 *    → startup probe always falls into the "else" branch → TierA (1 h).
 *
 * 2. Request-time path (router.Route → Complete):
 *    Complete() returns *provider.HTTPError{StatusCode:401/403}.
 *    → router.classifyError → TierB (24 h).
 */

import { test, expect } from '@playwright/test';
import { startMockServer, getFreePort, MockServer } from '../helpers/mock-server';
import { startRouter, stopRouter, rawPost, RouterProcess } from '../helpers/localrouter';
import { writeConfig, removeConfig } from '../helpers/config-gen';
import { waitForHealth, fetchHealth, findRemote } from '../helpers/poll';

interface Ctx { rp: RouterProcess | null; mocks: MockServer[]; cfgFile: string; }
function ctx(): Ctx { return { rp: null, mocks: [], cfgFile: '' }; }
async function teardown(c: Ctx): Promise<void> {
  if (c.rp) { await stopRouter(c.rp); c.rp = null; }
  for (const m of c.mocks) { await m.close(); }
  c.mocks.length = 0;
  if (c.cfgFile) { removeConfig(c.cfgFile); c.cfgFile = ''; }
}

test('startup probe 401 → TierA block (openai-compat HealthCheck path)', async () => {
  const c = ctx();
  try {
    const authFail = await startMockServer('auth-401');
    c.mocks.push(authFail);
    const routerPort = await getFreePort();

    c.cfgFile = writeConfig({
      locals: [],
      remotes: [{ id: 'r-auth-fail', port: authFail.port }],
      fallbackEnabled: true,
    });

    c.rp = await startRouter(c.cfgFile, routerPort);
    const { base } = c.rp;

    const health = await waitForHealth(base, { remoteProviders: { 'r-auth-fail': 'blocked' } });
    const remote = findRemote(health, 'r-auth-fail');
    expect(remote).toBeDefined();
    expect(remote!.status).toBe('blocked');
    // openaicompat.HealthCheck returns plain error → TierA = 1 h.
    expect(remote!.ttl_remaining).toBeGreaterThan(3540);
    expect(remote!.ttl_remaining).toBeLessThanOrEqual(3600);
  } finally {
    await teardown(c);
  }
});

test('startup probe 403 → TierA block (openai-compat HealthCheck path)', async () => {
  const c = ctx();
  try {
    const authFail = await startMockServer('auth-403');
    c.mocks.push(authFail);
    const routerPort = await getFreePort();

    c.cfgFile = writeConfig({
      locals: [],
      remotes: [{ id: 'r-forbidden', port: authFail.port }],
      fallbackEnabled: true,
    });

    c.rp = await startRouter(c.cfgFile, routerPort);
    const { base } = c.rp;

    const health = await waitForHealth(base, { remoteProviders: { 'r-forbidden': 'blocked' } });
    const remote = findRemote(health, 'r-forbidden');
    expect(remote!.status).toBe('blocked');
    expect(remote!.ttl_remaining).toBeGreaterThan(3540);
    expect(remote!.ttl_remaining).toBeLessThanOrEqual(3600);
  } finally {
    await teardown(c);
  }
});

test('request-time 401 → TierB block (router.classifyError path)', async () => {
  const c = ctx();
  try {
    // HealthCheck passes → not blocked at startup.
    // Complete() returns 401 → router.classifyError → TierB (24 h).
    const completionFail = await startMockServer('completion-401');
    c.mocks.push(completionFail);
    const routerPort = await getFreePort();

    c.cfgFile = writeConfig({
      locals: [],
      remotes: [{ id: 'r-completion-401', port: completionFail.port }],
      fallbackEnabled: true,
    });

    c.rp = await startRouter(c.cfgFile, routerPort);
    const { base } = c.rp;

    // HealthCheck passed → not blocked yet.
    await waitForHealth(base, { remoteProviders: { 'r-completion-401': 'missing' } });

    const payload = JSON.stringify({
      model: 'mock-model',
      messages: [{ role: 'user', content: 'hello' }],
      stream: false,
    });

    const { status } = await rawPost(`${base}/v1/chat/completions`, payload);
    // Provider returns 401 → blocked → no remaining providers → 503.
    expect(status).toBe(503);

    const health = await waitForHealth(base, { remoteProviders: { 'r-completion-401': 'blocked' } });
    const remote = findRemote(health, 'r-completion-401');
    expect(remote!.status).toBe('blocked');
    // TierB = 24 hours.
    expect(remote!.ttl_remaining).toBeGreaterThan(86_350);
    expect(remote!.ttl_remaining).toBeLessThanOrEqual(86_400);
  } finally {
    await teardown(c);
  }
});

test('request-time 401 blocks failing provider but leaves healthy provider usable', async () => {
  const c = ctx();
  try {
    const completionFail = await startMockServer('completion-401');
    const healthyRemote = await startMockServer('healthy-openai');
    c.mocks.push(completionFail, healthyRemote);
    const routerPort = await getFreePort();

    // Failing provider first in config → router tries it, gets 401, blocks it, then falls to healthy.
    c.cfgFile = writeConfig({
      locals: [],
      remotes: [
        { id: 'r-will-fail', port: completionFail.port },
        { id: 'r-healthy', port: healthyRemote.port },
      ],
      fallbackEnabled: true,
    });

    c.rp = await startRouter(c.cfgFile, routerPort);
    const { base } = c.rp;

    await waitForHealth(base, { remoteProviders: { 'r-will-fail': 'missing', 'r-healthy': 'missing' } });

    const payload = JSON.stringify({
      model: 'mock-model',
      messages: [{ role: 'user', content: 'hello' }],
      stream: false,
    });

    // r-will-fail → 401 → blocked, then r-healthy → 200.
    const { status, body } = await rawPost(`${base}/v1/chat/completions`, payload);
    expect(status).toBe(200);
    expect(JSON.parse(body).choices[0].message.content).toBe('Hello from mock.');

    const health = await fetchHealth(base);
    expect(findRemote(health, 'r-will-fail')?.status).toBe('blocked');
    expect(findRemote(health, 'r-healthy')).toBeUndefined(); // still no cache entry
  } finally {
    await teardown(c);
  }
});
