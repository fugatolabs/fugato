import { spawn } from 'node:child_process';
import * as os from 'node:os';
import { Settings } from './security';

// Discovery and doctor never use CLI commands that implicitly start the daemon.
export async function localJSON(binary: string, args: string[], settings: Settings, signal?: AbortSignal): Promise<unknown> {
  return new Promise((resolve, reject) => {
    const child = spawn(binary, args, { cwd: os.tmpdir(), shell: false, signal,
      env: { ...process.env, FUGATO_STATE_DIR: settings.stateDirectory, FUGATO_API_PORT: String(settings.apiPort) }, stdio: ['ignore', 'pipe', 'pipe'] });
    const chunks: Buffer[] = []; const errors: Buffer[] = []; let size = 0; let limit = false;
    const consume = (target: Buffer[]) => (chunk: Buffer) => {
      size += chunk.length;
      if (size > 2 * 1024 * 1024) { limit = true; child.kill('SIGKILL'); return; }
      target.push(chunk);
    };
    child.stdout.on('data', consume(chunks)); child.stderr.on('data', consume(errors));
    const timer = setTimeout(() => child.kill('SIGKILL'), 30000);
    child.once('error', reject);
    child.once('close', code => {
      clearTimeout(timer);
      if (limit) { reject(new Error('Fugato CLI response exceeds 2 MiB')); return; }
      const output = Buffer.concat(chunks).toString('utf8');
      // Doctor intentionally emits a structured failure report with a nonzero status.
      if (code !== 0 && args[0] !== 'doctor') { reject(new Error(Buffer.concat(errors).toString('utf8').slice(0, 4096) || `Fugato CLI exited with ${code}`)); return; }
      try { resolve(JSON.parse(output)); } catch { reject(new Error('Fugato CLI returned invalid JSON; update the installed CLI')); }
    });
  });
}
