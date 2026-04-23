import * as fs from 'fs';
import * as path from 'path';

const TMP_DIR = path.join(__dirname, '..', '.tmp');

export interface LocalNode {
  id: string;
  type: 'ollama' | 'openai-compatible';
  port: number;
  timeoutMs: number;
  apiKey?: string;
}

export interface RemoteProvider {
  id: string;
  port: number;
  apiKey?: string;
}

export interface RouterConfig {
  locals: LocalNode[];
  remotes: RemoteProvider[];
  fallbackEnabled?: boolean;
  latencyThresholdMs?: number;
}

export function writeConfig(cfg: RouterConfig): string {
  fs.mkdirSync(TMP_DIR, { recursive: true });
  const name = `cfg-${Date.now()}-${Math.random().toString(36).slice(2)}.yaml`;
  const filePath = path.join(TMP_DIR, name);
  fs.writeFileSync(filePath, toYaml(cfg), 'utf-8');
  return filePath;
}

export function removeConfig(filePath: string): void {
  try { fs.unlinkSync(filePath); } catch { /* already gone */ }
}

function toYaml(cfg: RouterConfig): string {
  const fallback = cfg.fallbackEnabled !== false;
  const latency = cfg.latencyThresholdMs ?? 2000;

  const lines: string[] = ['local:', '  nodes:'];

  if (cfg.locals.length === 0) {
    lines.push('    []');
  } else {
    for (const n of cfg.locals) {
      lines.push(`    - id: ${n.id}`);
      lines.push(`      type: ${n.type}`);
      lines.push(`      endpoint: "http://127.0.0.1:${n.port}"`);
      lines.push(`      timeout_ms: ${n.timeoutMs}`);
      if (n.apiKey) lines.push(`      api_key: "${n.apiKey}"`);
    }
  }

  lines.push('remote:', '  providers:');

  if (cfg.remotes.length === 0) {
    lines.push('    []');
  } else {
    for (const r of cfg.remotes) {
      lines.push(`    - id: ${r.id}`);
      lines.push(`      type: openai-compatible`);
      lines.push(`      endpoint: "http://127.0.0.1:${r.port}"`);
      lines.push(`      api_key: "${r.apiKey ?? 'mock-api-key'}"`);
    }
  }

  lines.push(
    'routing:',
    `  latency_threshold_ms: ${latency}`,
    `  fallback_enabled: ${fallback}`,
    '',
  );

  return lines.join('\n');
}
