import * as path from 'node:path';

export interface Runtime {
  id: string; repository: string; worktree: string; project_dir: string;
  commit: string; desired: string; observed: string; preview_url?: string;
  last_error?: string; metadata_error?: string; health?: string;
}
export interface Project { id: string; repository: string; worktree: string; project_dir: string; registered: boolean }
export interface Worktree { path: string; branch?: string; missing: boolean }
export interface Repository { repository: string; worktrees: Worktree[]; truncated: boolean }
export interface Info { api_version: string; instance_id: string; features: Record<string, boolean> }
export type LifecycleFeature = 'create_environment' | 'destroy_environment' | 'worktree_lifecycle';
export function requireFeature(info: Info, feature: LifecycleFeature): void {
  if (info.features[feature] === true) { return; }
  const action = { create_environment: 'creating environments', destroy_environment: 'destroying environments', worktree_lifecycle: 'creating or removing worktrees' }[feature];
  throw new Error(`Daemon update required for ${action}. Update the installed Fugato CLI and restart its daemon, then Refresh. Updating the VSIX or choosing Start Daemon does not replace an already-running daemon`);
}
export interface VerifyResult { runtime: Runtime; url: string; ok: boolean; duration_ms: number; issues: unknown[] | null; screenshot_path?: string; [key: string]: unknown }
export interface Report { report_id: string; kind: 'http' | 'browser'; started_at: string; completed_at: string; result: VerifyResult }
export interface Latest { http: Report | null; browser: Report | null }

export function object(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) { throw new Error('Malformed daemon response: expected object'); }
  return value as Record<string, unknown>;
}
export function string(value: unknown): string {
  if (typeof value !== 'string') { throw new Error('Malformed daemon response: expected string'); }
  return value;
}
export function id(value: unknown): string {
  const result = string(value);
  if (!/^[a-f0-9]{16}$/.test(result)) { throw new Error('Malformed daemon runtime ID'); }
  return result;
}
export function absolute(value: unknown): string {
  const result = string(value);
  if (!path.isAbsolute(result) || result.includes('\0')) { throw new Error('Daemon path must be absolute'); }
  return result;
}
function bool(value: unknown): boolean {
  if (typeof value !== 'boolean') { throw new Error('Malformed daemon response: expected boolean'); }
  return value;
}
export function runtime(value: unknown): Runtime {
  const v = object(value);
  for (const name of ['preview_url', 'last_error', 'metadata_error', 'health']) { if (v[name] !== undefined) { string(v[name]); } }
  return { ...v, id: id(v.id), repository: absolute(v.repository), worktree: absolute(v.worktree), project_dir: absolute(v.project_dir), commit: string(v.commit), desired: string(v.desired), observed: string(v.observed) } as Runtime;
}
export function project(value: unknown): Project {
  const v = object(value);
  return { id: id(v.id), repository: absolute(v.repository), worktree: absolute(v.worktree), project_dir: absolute(v.project_dir), registered: bool(v.registered) };
}
export function repository(value: unknown): Repository {
  const v = object(value);
  if (!Array.isArray(v.worktrees) || v.worktrees.length > 1000) { throw new Error('Malformed worktree list'); }
  return { repository: absolute(v.repository), truncated: bool(v.truncated), worktrees: v.worktrees.map(w => {
    const item = object(w);
    return { path: absolute(item.path), branch: item.branch === undefined ? undefined : string(item.branch), missing: bool(item.missing) };
  }) };
}
export function info(value: unknown): Info {
  const v = object(value); const features = object(v.features);
  if (v.api_version !== 'v1' || ['repository_worktrees', 'project_resolution', 'latest_verification', 'environments'].some(key => features[key] !== true)) {
    throw new Error('Daemon update required: install a Fugato daemon with extension support');
  }
  return { api_version: 'v1', instance_id: string(v.instance_id), features: features as Record<string, boolean> };
}
export function verifyResult(value: unknown): VerifyResult {
  const v = object(value);
  if (typeof v.duration_ms !== 'number' || !Number.isFinite(v.duration_ms) || v.duration_ms < 0 || (v.issues !== null && !Array.isArray(v.issues))) { throw new Error('Malformed verification report'); }
  if (v.screenshot_path !== undefined) { string(v.screenshot_path); }
  return { ...v, runtime: runtime(v.runtime), url: string(v.url), ok: bool(v.ok), duration_ms: v.duration_ms, issues: v.issues as unknown[] | null };
}
export function latest(value: unknown, runtimeID: string): Latest {
  const v = object(value);
  const parse = (kind: 'http' | 'browser'): Report | null => {
    if (v[kind] === null) { return null; }
    const r = object(v[kind]); const result = verifyResult(r.result);
    if (r.kind !== kind || result.runtime.id !== runtimeID) { throw new Error('Verification identity mismatch'); }
    const started_at = string(r.started_at), completed_at = string(r.completed_at);
    if (!Number.isFinite(Date.parse(started_at)) || !Number.isFinite(Date.parse(completed_at))) { throw new Error('Malformed report timestamp'); }
    return { ...r, report_id: string(r.report_id), kind, started_at, completed_at, result };
  };
  return { http: parse('http'), browser: parse('browser') };
}
export function within(root: string, candidate: string): boolean {
  const relative = path.relative(root, candidate);
  return relative === '' || (!path.isAbsolute(relative) && relative !== '..' && !relative.startsWith(`..${path.sep}`));
}
export function verifyPath(value: string): string {
  if (!value.startsWith('/') || value.startsWith('//') || /[\\\r\n#]/.test(value) || value.length > 2048) { throw new Error('Use a relative URL path beginning with /'); }
  const pathname = value.split('?')[0];
  if (path.posix.normalize(pathname) !== pathname) { throw new Error('Use a clean relative URL path'); }
  return value;
}

export interface Endpoint { name: string; service?: string; command?: string; port: number; protocol: string; browser: boolean }
export interface Candidate { id?: string; project_dir: string; config_file?: string; compose_files: string[]; kind: string; configured: boolean; error?: string; commands: { name: string; mode: string }[]; checks: string[]; check_details?: { name: string; requires_running: boolean; reason?: string }[]; endpoints: Endpoint[] }
export interface Discovery { worktree: string; projects: Candidate[]; truncated: boolean }
export interface PortBinding { name?: string; command?: string; service: string; container_port: number; protocol: string; host_ip: string; host_port: number; browser?: boolean; url?: string }
export interface RuntimeService { name: string; kind: 'compose' | 'native'; state: string; health?: string; ports: PortBinding[] }
function list(value: unknown, limit = 1000): unknown[] {
  if (!Array.isArray(value) || value.length > limit) { throw new Error('Malformed or oversized Fugato list'); }
  return value;
}
function name(value: unknown): string {
  const result = string(value);
  if (!result || result.length > 256 || /[\x00-\x1f\x7f]/.test(result)) { throw new Error('Malformed Fugato name'); }
  return result;
}
function port(value: unknown): number {
  if (typeof value !== 'number' || !Number.isInteger(value) || value < 0 || value > 65535) { throw new Error('Malformed Fugato port'); }
  return value;
}
export function discovery(value: unknown, expectedWorktree: string): Discovery {
  const v = object(value); const worktree = absolute(v.worktree);
  if (worktree !== expectedWorktree) { throw new Error('Project discovery worktree mismatch'); }
  const projects = list(v.projects).map(raw => {
    const p = object(raw); const project_dir = absolute(p.project_dir);
    if (!within(worktree, project_dir)) { throw new Error('Discovered project is outside the worktree'); }
    const config_file = p.config_file ? absolute(p.config_file) : undefined;
    if (config_file && (path.dirname(config_file) !== project_dir || path.basename(config_file) !== '.fugato.yaml')) { throw new Error('Unexpected project configuration path'); }
    const compose_files = list(p.compose_files || []).map(absolute);
    if (compose_files.some(file => !within(worktree, file))) { throw new Error('Compose file is outside the worktree'); }
    const commands = list(p.commands || []).map(rawCommand => {
      const c = object(rawCommand); const mode = string(c.mode);
      if (!['service', 'task', 'interactive'].includes(mode)) { throw new Error('Unknown command mode'); }
      return { name: name(c.name), mode };
    });
    const endpoints = list(p.endpoints || []).map(rawEndpoint => {
      const e = object(rawEndpoint); const protocol = string(e.protocol);
      if (!['http', 'https', 'tcp', 'udp'].includes(protocol)) { throw new Error('Unknown endpoint protocol'); }
      return { name: name(e.name), service: e.service ? name(e.service) : undefined, command: e.command ? name(e.command) : undefined, port: port(e.port), protocol, browser: e.browser === undefined ? false : bool(e.browser) };
    });
    const checks = list(p.checks || []).map(name);
    const check_details = p.check_details === undefined ? undefined : list(p.check_details).map(rawCheck => {
      const check = object(rawCheck); const checkName = name(check.name);
      if (!checks.includes(checkName)) { throw new Error('Unexpected check metadata'); }
      return { name: checkName, requires_running: bool(check.requires_running), reason: check.reason === undefined ? undefined : string(check.reason) };
    });
    return { id: p.id ? id(p.id) : undefined, project_dir, config_file, compose_files, kind: string(p.kind), configured: bool(p.configured), error: p.error ? string(p.error) : undefined, commands, checks, check_details, endpoints };
  });
  return { worktree, projects, truncated: bool(v.truncated) };
}
export function bindings(value: unknown): PortBinding[] {
  return list(value).map(raw => {
    const p = object(raw);
    return { name: p.name ? name(p.name) : undefined, command: p.command ? name(p.command) : undefined, service: p.service === undefined ? '' : string(p.service), container_port: port(p.container_port), protocol: string(p.protocol), host_ip: string(p.host_ip), host_port: port(p.host_port), browser: p.browser === undefined ? false : bool(p.browser), url: p.url ? string(p.url) : undefined };
  });
}
export function services(value: unknown): RuntimeService[] {
  return list(value, 256).map(raw => {
    const s = object(raw); const kind = string(s.kind);
    if (kind !== 'compose' && kind !== 'native') { throw new Error('Unknown service kind'); }
    return { name: name(s.name), kind, state: name(s.state), health: s.health ? name(s.health) : undefined, ports: bindings(s.ports) };
  });
}
export function servicesFromBindings(ports: PortBinding[]): RuntimeService[] {
  const result = new Map<string, RuntimeService>();
  for (const port of ports) {
    const kind = port.command ? 'native' : 'compose'; const name = port.command || port.service || port.name || 'Service';
    const key = `${kind}:${name}`;
    if (!result.has(key)) { result.set(key, { name, kind, state: 'running', ports: [] }); }
    result.get(key)!.ports.push(port);
  }
  return [...result.values()];
}
// Service actions re-fetch their binding and accept only local host addresses.
export function serviceAddress(binding: PortBinding, runtimeID: string): string {
  id(runtimeID);
  if (binding.host_ip !== '127.0.0.1' || port(binding.host_port) < 1) { throw new Error('Service has no available loopback host port'); }
  if (binding.browser) { return endpointURL(binding, runtimeID); }
  if (['http', 'https'].includes(binding.protocol)) { return `${binding.protocol}://127.0.0.1:${binding.host_port}/`; }
  if (!['tcp', 'udp'].includes(binding.protocol)) { throw new Error('Unknown service port protocol'); }
  return `127.0.0.1:${binding.host_port}`;
}
export function endpointURL(binding: PortBinding, runtimeID: string): string {
  id(runtimeID); name(binding.name);
  if (!binding.browser || !binding.host_port || binding.host_ip !== '127.0.0.1') { throw new Error('Endpoint is not an available loopback browser preview'); }
  const raw = string(binding.url);
  const named = /^http:\/\/([a-z0-9-]+)\.([a-f0-9]{16})\.localhost:([0-9]{1,5})\/$/.exec(raw);
  if (named && named[1] === binding.name && named[2] === runtimeID && Number(named[3]) >= 1 && Number(named[3]) <= 65535) { return raw; }
  // HTTPS retains its direct TLS endpoint; the HTTP proxy does not terminate TLS.
  if (raw === `https://127.0.0.1:${binding.host_port}/`) { return raw; }
  throw new Error('Preview URL does not match the selected runtime and endpoint binding');
}
