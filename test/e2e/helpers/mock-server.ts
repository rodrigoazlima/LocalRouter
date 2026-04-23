import * as http from 'http';
import * as net from 'net';

/**
 * Behaviors that control what a mock server returns.
 *
 * healthy-openai     GET /v1/models → 200; POST /v1/chat/completions → 200
 * healthy-ollama     GET /api/tags  → 200; POST /v1/chat/completions → 200
 * auth-401           every request  → 401
 * auth-403           every request  → 403
 * rate-limit-429     every request  → 429
 * server-error-500   every request  → 500
 * slow-response      GET /v1/models delays `delayMs` ms before responding (caller's timeout fires first)
 * completion-401     GET /v1/models → 200; POST /v1/chat/completions → 401
 * completion-429     GET /v1/models → 200; POST /v1/chat/completions → 429
 */
export type Behavior =
  | 'healthy-openai'
  | 'healthy-ollama'
  | 'auth-401'
  | 'auth-403'
  | 'rate-limit-429'
  | 'server-error-500'
  | 'slow-response'
  | 'completion-401'
  | 'completion-429';

export interface MockServer {
  readonly port: number;
  close(): Promise<void>;
}

// ── canned responses ────────────────────────────────────────────────────────

const MODELS_OK = JSON.stringify({
  object: 'list',
  data: [{ id: 'mock-model', object: 'model', created: 1699000000, owned_by: 'mock' }],
});

const OLLAMA_OK = JSON.stringify({
  models: [{ name: 'llama2:latest', modified_at: '2024-01-01T00:00:00Z', size: 3825819519 }],
});

const COMPLETION_OK = JSON.stringify({
  id: 'chatcmpl-mock001',
  object: 'chat.completion',
  model: 'mock-model',
  choices: [{ index: 0, message: { role: 'assistant', content: 'Hello from mock.' }, finish_reason: 'stop' }],
  usage: { prompt_tokens: 10, completion_tokens: 5, total_tokens: 15 },
});

function errJSON(msg: string): string {
  return JSON.stringify({ error: { message: msg, type: 'error' } });
}

// ── request dispatcher ──────────────────────────────────────────────────────

function dispatch(
  behavior: Behavior,
  req: http.IncomingMessage,
  res: http.ServerResponse,
  delayMs: number,
): void {
  const url = req.url ?? '/';
  const method = req.method ?? 'GET';

  if (behavior === 'slow-response') {
    setTimeout(() => {
      if (!res.writableEnded) {
        try { res.writeHead(200, { 'Content-Type': 'application/json' }); res.end(MODELS_OK); } catch { /* closed */ }
      }
    }, delayMs);
    return;
  }

  if (behavior === 'auth-401') { res.writeHead(401, ct()); return void res.end(errJSON('Unauthorized')); }
  if (behavior === 'auth-403') { res.writeHead(403, ct()); return void res.end(errJSON('Forbidden')); }
  if (behavior === 'rate-limit-429') { res.writeHead(429, ct()); return void res.end(errJSON('Too Many Requests')); }
  if (behavior === 'server-error-500') { res.writeHead(500, ct()); return void res.end(errJSON('Internal Server Error')); }

  if (behavior === 'healthy-openai') {
    if (url === '/v1/models' && method === 'GET') { res.writeHead(200, ct()); return void res.end(MODELS_OK); }
    if (url === '/v1/chat/completions' && method === 'POST') { return drainThen(req, res, 200, COMPLETION_OK); }
    res.writeHead(404); res.end(); return;
  }

  if (behavior === 'healthy-ollama') {
    if (url === '/api/tags' && method === 'GET') { res.writeHead(200, ct()); return void res.end(OLLAMA_OK); }
    if (url === '/v1/chat/completions' && method === 'POST') { return drainThen(req, res, 200, COMPLETION_OK); }
    res.writeHead(404); res.end(); return;
  }

  if (behavior === 'completion-401') {
    if (url === '/v1/models' && method === 'GET') { res.writeHead(200, ct()); return void res.end(MODELS_OK); }
    if (url === '/v1/chat/completions' && method === 'POST') { return drainThen(req, res, 401, errJSON('Unauthorized')); }
    res.writeHead(404); res.end(); return;
  }

  if (behavior === 'completion-429') {
    if (url === '/v1/models' && method === 'GET') { res.writeHead(200, ct()); return void res.end(MODELS_OK); }
    if (url === '/v1/chat/completions' && method === 'POST') { return drainThen(req, res, 429, errJSON('Too Many Requests')); }
    res.writeHead(404); res.end(); return;
  }

  res.writeHead(500); res.end(errJSON('unknown behavior'));
}

function ct(): Record<string, string> {
  return { 'Content-Type': 'application/json' };
}

function drainThen(req: http.IncomingMessage, res: http.ServerResponse<http.IncomingMessage>, status: number, body: string): void {
  req.resume();
  req.on('end', () => {
    if (!res.writableEnded) {
      try { res.writeHead(status, ct()); res.end(body); } catch { /* closed */ }
    }
  });
}

// ── public factory ──────────────────────────────────────────────────────────

export function startMockServer(behavior: Behavior, delayMs = 2000): Promise<MockServer> {
  const server = http.createServer((req, res) => {
    dispatch(behavior, req, res, delayMs);
  });

  return new Promise<MockServer>((resolve, reject) => {
    server.listen(0, '127.0.0.1', () => {
      const addr = server.address() as net.AddressInfo;
      resolve({
        port: addr.port,
        close: () => new Promise<void>((ok) => {
          server.closeAllConnections?.();
          server.close(() => ok());
        }),
      });
    });
    server.on('error', reject);
  });
}

/** Returns a port number that is free right now but has no server bound to it. */
export function getFreePort(): Promise<number> {
  return new Promise<number>((resolve, reject) => {
    const tmp = net.createServer();
    tmp.listen(0, '127.0.0.1', () => {
      const port = (tmp.address() as net.AddressInfo).port;
      tmp.close(() => resolve(port));
    });
    tmp.on('error', reject);
  });
}
