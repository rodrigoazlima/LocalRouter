import { rawGet, sleep } from './localrouter';

// ── response shapes ──────────────────────────────────────────────────────────

export interface NodeInfo {
  id: string;
  status: string; // "ready" | "degraded" | "unavailable"
  latency_ms: number;
}

export interface RemoteInfo {
  id: string;
  status: string; // "available" | "blocked" | "exhausted" | "unhealthy"
  ttl_remaining?: number;
}

export interface HealthResponse {
  local: {
    status: string; // "healthy" | "degraded" | "unavailable"
    nodes: NodeInfo[];
  };
  remote: RemoteInfo[];
}

export interface MetricsSnapshot {
  requests: number;
  failures: number;
  local_requests: number;
  remote_requests: number;
  no_capacity: number;
  provider_block_events: number;
  streams_started: number;
  streams_completed: number;
  streams_disconnected: number;
  stream_duration_ms: number;
  nodes: Record<string, { checks_ok: number; checks_fail: number; latency_ms: number }>;
}

// ── raw server shape ─────────────────────────────────────────────────────────

interface RawProvider {
  id: string;
  state: string; // "available" | "blocked" | "exhausted" | "unhealthy"
  latency_ms?: number;
  blocked_until?: string;
}


function mapToNodeStatus(state: string): string {
  if (state === 'available') return 'ready';
  if (state === 'unhealthy') return 'unavailable';
  return 'degraded';
}

function ttlSeconds(blockedUntil: string): number {
  return Math.max(0, Math.round((new Date(blockedUntil).getTime() - Date.now()) / 1000));
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
function transformHealth(raw: any): HealthResponse {
  // New format: { status, providers: [...] }
  if (Array.isArray(raw?.providers)) {
    const providers: RawProvider[] = raw.providers;

    const remote: RemoteInfo[] = providers
      .filter((p) => p.state !== 'available')
      .map((p) => ({
        id: p.id,
        status: p.state,
        ttl_remaining: p.blocked_until ? ttlSeconds(p.blocked_until) : undefined,
      }));

    const nodes: NodeInfo[] = providers.map((p) => ({
      id: p.id,
      status: mapToNodeStatus(p.state),
      latency_ms: p.latency_ms ?? 0,
    }));

    const anyAvailable = providers.some((p) => p.state === 'available');
    const localStatus = anyAvailable ? 'healthy' : 'unavailable';

    return { local: { status: localStatus, nodes }, remote };
  }

  // Legacy format: { local: { status, nodes }, remote: [...] }
  if (raw?.local) {
    return {
      local: {
        status: raw.local.status ?? 'unavailable',
        nodes: Array.isArray(raw.local.nodes) ? raw.local.nodes : [],
      },
      remote: Array.isArray(raw.remote) ? raw.remote : [],
    };
  }

  return { local: { status: 'unavailable', nodes: [] }, remote: [] };
}

// ── fetch helpers ────────────────────────────────────────────────────────────

export async function fetchHealth(base: string): Promise<HealthResponse> {
  const { body } = await rawGet(`${base}/health`);
  return transformHealth(JSON.parse(body));
}

export async function fetchMetrics(base: string): Promise<MetricsSnapshot> {
  const { body } = await rawGet(`${base}/metrics`);
  return JSON.parse(body) as MetricsSnapshot;
}

// ── polling ──────────────────────────────────────────────────────────────────

export interface ExpectedState {
  /** id → expected status ("ready" | "degraded" | "unavailable") */
  localNodes?: Record<string, string>;
  /** id → expected status ("blocked" | "available" | "missing")
   *  "missing" = provider is in available state (not in the blocked/failed list) */
  remoteProviders?: Record<string, string>;
  /** expected overall local status ("healthy" | "degraded" | "unavailable") */
  localStatus?: string;
}

/**
 * Polls /health every 100 ms until all expected conditions are met or timeout.
 * Throws if conditions are not met within `timeoutMs`.
 */
export async function waitForHealth(
  base: string,
  expected: ExpectedState,
  timeoutMs = 12_000,
): Promise<HealthResponse> {
  const deadline = Date.now() + timeoutMs;

  while (Date.now() < deadline) {
    let health: HealthResponse;
    try {
      health = await fetchHealth(base);
    } catch {
      await sleep(100);
      continue;
    }

    if (matches(health, expected)) return health;
    await sleep(100);
  }

  let last: HealthResponse | undefined;
  try { last = await fetchHealth(base); } catch { /* ignore */ }
  throw new Error(
    `waitForHealth timed out after ${timeoutMs}ms.\n` +
    `Expected: ${JSON.stringify(expected, null, 2)}\n` +
    `Last health: ${JSON.stringify(last, null, 2)}`,
  );
}

function matches(health: HealthResponse, expected: ExpectedState): boolean {
  const localStatus = health?.local?.status;
  const localNodes = health?.local?.nodes ?? [];
  const remote = health?.remote ?? [];

  if (expected.localStatus && localStatus !== expected.localStatus) return false;

  if (expected.localNodes) {
    for (const [id, wantStatus] of Object.entries(expected.localNodes)) {
      const node = localNodes.find((n) => n.id === id);
      if (!node) return false;
      if (node.status !== wantStatus) return false;
    }
  }

  if (expected.remoteProviders) {
    for (const [id, wantStatus] of Object.entries(expected.remoteProviders)) {
      if (wantStatus === 'missing') {
        // "missing" = provider is available (not in the remote blocked list)
        const found = remote.find((r) => r.id === id);
        if (found) return false;
      } else {
        const found = remote.find((r) => r.id === id);
        if (!found) return false;
        if (found.status !== wantStatus) return false;
      }
    }
  }

  return true;
}

// ── convenience accessors ────────────────────────────────────────────────────

export function findNode(health: HealthResponse, id: string): NodeInfo | undefined {
  return (health?.local?.nodes ?? []).find((n) => n.id === id);
}

export function findRemote(health: HealthResponse, id: string): RemoteInfo | undefined {
  return (health?.remote ?? []).find((r) => r.id === id);
}
