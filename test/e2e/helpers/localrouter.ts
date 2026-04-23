import { execSync, spawn, ChildProcess } from 'child_process';
import * as http from 'http';
import * as fs from 'fs';
import * as path from 'path';

const PROJECT_ROOT = path.resolve(__dirname, '../../..');
const BIN_DIR = path.join(__dirname, '..', '.bin');
const BIN_NAME = process.platform === 'win32' ? 'localrouter-e2e.exe' : 'localrouter-e2e';

export const BINARY_PATH = path.join(BIN_DIR, BIN_NAME);

export async function buildBinary(): Promise<void> {
  fs.mkdirSync(BIN_DIR, { recursive: true });
  execSync(`go build -o "${BINARY_PATH}" ./cmd/localrouter/`, {
    cwd: PROJECT_ROOT,
    stdio: 'inherit',
    timeout: 120_000,
  });
}

export interface RouterProcess {
  proc: ChildProcess;
  port: number;
  base: string;
}

export async function startRouter(configPath: string, port: number): Promise<RouterProcess> {
  const proc = spawn(BINARY_PATH, ['--config', configPath, '--port', String(port)], {
    stdio: ['ignore', 'pipe', 'pipe'],
  });

  proc.stdout?.on('data', (d: Buffer) => process.stdout.write(`[router:${port}] ${d}`));
  proc.stderr?.on('data', (d: Buffer) => process.stderr.write(`[router:${port}] ${d}`));

  proc.on('error', (err: Error) => {
    console.error('[router] failed to start:', err.message);
  });

  const base = `http://localhost:${port}`;
  await waitUntilReady(base, 15_000);
  return { proc, port, base };
}

export function routerBase(port: number): string {
  return `http://localhost:${port}`;
}

export async function stopRouter(rp: RouterProcess): Promise<void> {
  const { proc } = rp;
  if (proc.killed || proc.exitCode !== null) return;
  proc.kill();
  await new Promise<void>((resolve) => {
    const t = setTimeout(resolve, 3_000);
    proc.once('exit', () => { clearTimeout(t); resolve(); });
  });
}

async function waitUntilReady(base: string, timeoutMs: number): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      const { status } = await rawGet(`${base}/health`);
      if (status >= 200 && status < 600) return;
    } catch { /* not yet */ }
    await sleep(100);
  }
  throw new Error(`LocalRouter did not start within ${timeoutMs}ms on ${base}`);
}

// ── low-level HTTP helpers used by poll.ts too ──────────────────────────────

export function rawGet(url: string): Promise<{ status: number; body: string }> {
  return new Promise((resolve, reject) => {
    const req = http.get(url, (res) => {
      let body = '';
      res.on('data', (d: Buffer) => (body += d.toString()));
      res.on('end', () => resolve({ status: res.statusCode ?? 0, body }));
    });
    req.on('error', reject);
    req.setTimeout(5_000, () => { req.destroy(new Error('timeout')); });
  });
}

export function rawPost(url: string, body: string): Promise<{ status: number; body: string }> {
  return new Promise((resolve, reject) => {
    const buf = Buffer.from(body, 'utf-8');
    const u = new URL(url);
    const options: http.RequestOptions = {
      hostname: u.hostname,
      port: u.port,
      path: u.pathname,
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'Content-Length': buf.length },
    };
    const req = http.request(options, (res) => {
      let out = '';
      res.on('data', (d: Buffer) => (out += d.toString()));
      res.on('end', () => resolve({ status: res.statusCode ?? 0, body: out }));
    });
    req.on('error', reject);
    req.setTimeout(10_000, () => { req.destroy(new Error('timeout')); });
    req.end(buf);
  });
}

export function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
