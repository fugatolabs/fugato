import * as fs from 'node:fs/promises';
import { constants } from 'node:fs';
import * as path from 'node:path';
import * as os from 'node:os';
import { absolute, id, within } from './contracts';

export interface Settings { stateDirectory: string; apiPort: number; executablePath: string }
export function settings(values: Partial<Settings>, env: NodeJS.ProcessEnv = process.env, platform = process.platform): Settings {
  const stateDirectory = values.stateDirectory || env.FUGATO_STATE_DIR || (platform === 'darwin' ? path.join(os.homedir(), 'Library', 'Application Support', 'fugato') : path.join(env.XDG_CONFIG_HOME || path.join(os.homedir(), '.config'), 'fugato'));
  absolute(stateDirectory);
  const apiPort = values.apiPort || Number(env.FUGATO_API_PORT || 46655);
  if (!Number.isInteger(apiPort) || apiPort < 1 || apiPort > 65535) { throw new Error('Fugato API port must be between 1 and 65535'); }
  const executablePath = values.executablePath || '';
  if (executablePath) { absolute(executablePath); }
  return { stateDirectory, apiPort, executablePath };
}
export async function readToken(stateDirectory: string): Promise<string> {
  const filename = path.join(stateDirectory, 'token');
  const handle = await fs.open(filename, constants.O_RDONLY | constants.O_NOFOLLOW);
  try {
    const stat = await handle.stat();
    if (!stat.isFile() || stat.uid !== process.getuid?.() || (stat.mode & 0o077) !== 0 || stat.size > 65 || stat.size < 64) { throw new Error('Fugato token must be a current-user regular file with user-only permissions'); }
    const buffer = Buffer.alloc(66);
    const { bytesRead } = await handle.read(buffer, 0, buffer.length, 0);
    const token = buffer.subarray(0, bytesRead).toString('utf8').trim();
    if (bytesRead > 65 || !/^[a-f0-9]{64}$/.test(token)) { throw new Error('Invalid Fugato token format'); }
    return token;
  } finally { await handle.close(); }
}
export async function screenshot(stateDirectory: string, runtimeID: string, filename: string): Promise<string> {
  id(runtimeID); absolute(filename);
  const root = path.join(await fs.realpath(stateDirectory), 'artifacts', runtimeID);
  const canonical = await fs.realpath(filename);
  if (!within(root, canonical) || path.extname(canonical).toLowerCase() !== '.png') { throw new Error('Screenshot must be a PNG inside this runtime’s artifact directory'); }
  const handle = await fs.open(canonical, constants.O_RDONLY | constants.O_NOFOLLOW);
  try {
    const stat = await handle.stat();
    if (!stat.isFile() || stat.size > 8 * 1024 * 1024) { throw new Error('Screenshot is unavailable or exceeds 8 MiB'); }
    const magic = Buffer.alloc(8); await handle.read(magic, 0, 8, 0);
    if (!magic.equals(Buffer.from([137,80,78,71,13,10,26,10]))) { throw new Error('Screenshot is not a PNG'); }
  } finally { await handle.close(); }
  return canonical;
}
export async function executable(configured: string, workspaces: string[], hostPath = process.env.PATH || ''): Promise<string> {
  const candidates = configured ? [absolute(configured)] : hostPath.split(path.delimiter).filter(p => path.isAbsolute(p)).map(p => path.join(p, 'fugato'));
  for (const candidate of candidates) {
    try {
      const real = await fs.realpath(candidate);
      if (workspaces.some(root => within(root, candidate) || within(root, real))) { continue; }
      const stat = await fs.stat(real);
      if (!stat.isFile()) { continue; }
      await fs.access(real, constants.X_OK);
      return real;
    } catch { /* Try another host PATH entry. */ }
  }
  throw new Error('Install Fugato outside your workspace, or set an absolute user-level fugato.executablePath');
}
export function plain(value: string): string { return value.replace(/\x1b\[[0-?]*[ -/]*[@-~]/g, '').replace(/[\x00-\x08\x0b-\x1f\x7f]/g, ''); }
