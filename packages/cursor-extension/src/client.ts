import * as http from 'node:http';
import * as dto from './contracts';
import { readToken, Settings } from './security';

export class APIError extends Error { constructor(message: string, readonly status: number) { super(message); } }
export class Client {
  private requests = new Set<http.ClientRequest>();
  private closed = false;
  constructor(readonly settings: Settings) {}
  dispose(): void { this.closed = true; for (const request of this.requests) { request.destroy(new Error('Fugato connection closed')); } this.requests.clear(); }
  async call(method: string, endpoint: string, body?: unknown, signal?: AbortSignal, timeout = 30000, readOnly = method === 'GET'): Promise<unknown> {
    for (let attempt = 0; ; attempt++) {
      const token = await readToken(this.settings.stateDirectory);
      try { return await this.request(method, endpoint, body, token, signal, timeout); }
      catch (error) {
        if (!(error instanceof APIError) || error.status !== 401 || !readOnly || attempt > 0) { throw error; }
      }
    }
  }
  private request(method: string, endpoint: string, body: unknown, token: string, signal: AbortSignal | undefined, timeout: number): Promise<unknown> {
    if (this.closed || signal?.aborted) { return Promise.reject(new Error('Fugato request cancelled')); }
    return new Promise((resolve, reject) => {
      const data = body === undefined ? undefined : JSON.stringify(body);
      const req = http.request({ hostname: '127.0.0.1', port: this.settings.apiPort, path: endpoint, method, agent: false, signal,
        headers: { Authorization: `Bearer ${token}`, ...(data ? { 'Content-Type': 'application/json', 'Content-Length': Buffer.byteLength(data) } : {}) } }, res => {
        clearTimeout(connectTimer);
        const chunks: Buffer[] = []; let size = 0;
        res.on('data', (chunk: Buffer) => {
          size += chunk.length;
          if (size > 2 * 1024 * 1024) { req.destroy(new Error('Daemon response exceeds 2 MiB')); return; }
          chunks.push(chunk);
        });
        res.on('error', reject);
        res.on('end', () => {
          const status = res.statusCode || 0; let value: unknown;
          try { value = JSON.parse(Buffer.concat(chunks).toString('utf8')); }
          catch { reject(new APIError(status === 404 ? 'Daemon update required: extension API unavailable' : 'Malformed JSON from Fugato daemon', status)); return; }
          if (status < 200 || status >= 300) {
            const message = typeof (value as {error?: unknown})?.error === 'string' ? (value as {error: string}).error : `Daemon HTTP ${status}`;
            reject(new APIError(message.split(token).join('[redacted]'), status));
          } else { resolve(value); }
        });
      });
      this.requests.add(req);
      const timer = setTimeout(() => req.destroy(new Error('Fugato request timed out; refresh to check the outcome')), timeout);
      const connectTimer = setTimeout(() => req.destroy(new Error('Fugato daemon connection timed out')), 5000);
      req.on('socket', socket => socket.once('connect', () => clearTimeout(connectTimer)));
      req.on('error', reject);
      req.on('close', () => { clearTimeout(timer); clearTimeout(connectTimer); this.requests.delete(req); });
      req.end(data);
    });
  }
  async info(): Promise<dto.Info> {
    try { return dto.info(await this.call('GET', '/v1/info')); }
    catch (error) { if (error instanceof APIError && error.status === 404) { throw new APIError('Daemon update required: extension API unavailable', 404); } throw error; }
  }
  async list(): Promise<dto.Runtime[]> {
    const value = await this.call('GET', '/v1/agent/runtimes');
    if (!Array.isArray(value)) { throw new Error('Malformed runtime list'); }
    return value.map(dto.runtime);
  }
  async get(id: string): Promise<dto.Runtime> {
    const value = dto.runtime(await this.call('GET', this.route(id)));
    if (value.id !== id) { throw new Error('Runtime identity mismatch'); } return value;
  }
  async worktrees(path: string): Promise<dto.Repository> { return dto.repository(await this.call('POST', '/v1/repositories/worktrees', { path }, undefined, 30000, true)); }
  async resolve(path: string): Promise<dto.Project> { return dto.project(await this.call('POST', '/v1/projects/resolve', { path }, undefined, 30000, true)); }
  async create(project: dto.Project, signal?: AbortSignal): Promise<dto.Runtime> {
    const response = await this.call('POST', '/v1/runtimes/create', { path: project.project_dir, expected_runtime_id: project.id }, signal, 360000);
    let runtime: dto.Runtime;
    try { runtime = dto.runtime(response); }
    catch (error) { throw new Error(`Environment creation response could not be validated: ${error instanceof Error ? error.message : String(error)}. Creation may have completed; refresh and select the environment to review its state`); }
    if (runtime.id !== project.id || runtime.repository !== project.repository || runtime.worktree !== project.worktree || runtime.project_dir !== project.project_dir) { throw new Error('Created environment identity mismatch'); }
    return runtime;
  }
  async createWorktree(path: string, branch: string, target_path: string, new_branch: boolean): Promise<dto.Repository> {
    return dto.repository(await this.call('POST', '/v1/repositories/worktrees/create', { path, branch, target_path, new_branch }, undefined, 30000));
  }
  async removeWorktree(path: string, worktree: string): Promise<void> {
    if (dto.object(await this.call('POST', '/v1/repositories/worktrees/remove', { path, worktree })).removed !== true) { throw new Error('Malformed worktree removal response'); }
  }
  async start(project: dto.Project, signal?: AbortSignal): Promise<void> {
    if (project.registered) { await this.call('POST', this.route(project.id, '/start'), undefined, signal, 360000); }
    else { await this.call('POST', '/v1/runtimes', { path: project.project_dir, expected_runtime_id: project.id }, signal, 360000); }
  }
  async stop(id: string, signal?: AbortSignal): Promise<void> { await this.call('POST', this.route(id, '/stop'), undefined, signal, 360000); }
  async destroy(id: string, signal?: AbortSignal): Promise<void> {
    try {
      const value = dto.object(await this.call('POST', `/v1/runtimes/${dto.id(id)}/destroy`, { volumes: false }, signal, 360000));
      if (dto.id(value.id) !== id || value.volumes !== false) { throw new Error('Destroy response identity or data-retention mismatch'); }
    } catch (error) {
      if (error instanceof APIError && [404, 405].includes(error.status)) { throw new APIError('Daemon update required: environment destruction endpoint unavailable. Update the installed CLI, restart its daemon, then Refresh', error.status); }
      if (error instanceof APIError && error.status === 400 && /unknown runtime/.test(error.message)) { throw new APIError('Environment no longer registered; refresh and choose an existing environment', 400); }
      throw error;
    }
  }
  async logs(id: string, service?: string, serviceKind?: 'compose' | 'native'): Promise<{text: string; truncated: boolean}> {
    const value = dto.object(await this.call('POST', this.route(id, '/logs'), { lines: 200, ...(service ? { service, ...(serviceKind ? { service_kind: serviceKind } : {}) } : {}) }, undefined, 30000, true));
    if (dto.runtime(value.runtime).id !== id || typeof value.truncated !== 'boolean') { throw new Error('Malformed log response'); }
    return { text: dto.string(value.text), truncated: value.truncated };
  }
  async verify(id: string, kind: 'http' | 'browser', path: string, signal?: AbortSignal, endpoint?: string): Promise<dto.VerifyResult> {
    const result = dto.verifyResult(await this.call('POST', this.route(id, `/verify/${kind}`), { path: dto.verifyPath(path), timeout_ms: 60000, ...(endpoint ? { endpoint } : {}) }, signal, 70000));
    if (result.runtime.id !== id) { throw new Error('Verification identity mismatch'); } return result;
  }
  async latest(id: string): Promise<dto.Latest> { return dto.latest(await this.call('GET', this.route(id, '/verifications/latest')), id); }
  async projects(worktree: string): Promise<dto.Discovery> {
    return dto.discovery(await this.call('GET', `/v1/projects?path=${encodeURIComponent(worktree)}`), worktree);
  }
  async ports(id: string): Promise<dto.PortBinding[]> {
    return dto.bindings(await this.call('GET', `/v1/runtimes/${dto.id(id)}/ports`));
  }
  async services(id: string): Promise<dto.RuntimeService[]> {
    return dto.services(await this.call('GET', `/v1/runtimes/${dto.id(id)}/services`));
  }
  async named(project: dto.Candidate, kind: 'run' | 'check', name: string, signal?: AbortSignal): Promise<unknown> {
    return this.call('POST', `/v1/projects/${kind}`, { path: project.project_dir, name }, signal, 360000);
  }
  private route(id: string, suffix = ''): string { return `/v1/agent/runtimes/${dto.id(id)}${suffix}`; }
}
