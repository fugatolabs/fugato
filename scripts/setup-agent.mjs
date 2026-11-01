#!/usr/bin/env node
// Project-scoped skill discovery, workflow instructions, and local MCP setup.
import fs from 'node:fs';
import path from 'node:path';
import os from 'node:os';
import { fileURLToPath } from 'node:url';
import { parseArgs } from 'node:util';

export const profiles = {
  codex: { skill: '.agents/skills', instructions: 'AGENTS.md', config: '.codex/config.toml' },
  'claude-code': { skill: '.claude/skills', instructions: 'CLAUDE.md', config: '.mcp.json', key: 'mcpServers' },
  cursor: { skill: '.agents/skills', instructions: '.cursor/rules/fugato.mdc', config: '.cursor/mcp.json', key: 'mcpServers' },
  'github-copilot': { skill: '.agents/skills', instructions: '.github/copilot-instructions.md', config: '.vscode/mcp.json', key: 'servers' },
  'gemini-cli': { skill: '.agents/skills', instructions: 'GEMINI.md', config: '.gemini/settings.json', key: 'mcpServers' },
};
const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const start = '<!-- fugato-workflow:start -->';
const end = '<!-- fugato-workflow:end -->';
const quote = value => "'" + value.replaceAll("'", "'\\''") + "'";
const object = value => value !== null && typeof value === 'object' && !Array.isArray(value);

function regular(file) {
  try {
    const stat = fs.lstatSync(file);
    if (!stat.isFile()) throw new Error(`Expected a regular file: ${file}`);
    return fs.readFileSync(file);
  } catch (error) { if (error.code === 'ENOENT') return undefined; throw error; }
}

function safeTarget(root, relative) {
  const target = path.join(root, relative);
  for (let current = target; current !== root; current = path.dirname(current)) {
    try { if (fs.lstatSync(current).isSymbolicLink()) throw new Error(`Refusing symlink: ${current}`); }
    catch (error) { if (error.code !== 'ENOENT') throw error; }
  }
  return target;
}

function findBinary(name, binDir) {
  for (const dir of binDir ? [binDir] : (process.env.PATH || '').split(path.delimiter)) {
    const file = path.resolve(dir, name);
    try { fs.accessSync(file, fs.constants.X_OK); if (fs.statSync(file).isFile()) return file; }
    catch { /* Try the next PATH directory. */ }
  }
  throw new Error(`Install ${name} first, or supply --bin-dir.`);
}

function skillFiles(root, prefix = '') {
  return fs.readdirSync(path.join(root, prefix), { withFileTypes: true }).flatMap(entry => {
    const name = path.join(prefix, entry.name);
    if (entry.isSymbolicLink()) throw new Error(`Skill contains a symlink: ${name}`);
    if (entry.isDirectory()) return skillFiles(root, name);
    if (!entry.isFile()) throw new Error(`Unsupported skill entry: ${name}`);
    return [name];
  });
}

export function setupAgent({ agent, project = '.', binDir, skillDir, dryRun = false }) {
  if (!Object.hasOwn(profiles, agent)) throw new Error(`Choose --agent ${Object.keys(profiles).join('|')}`);
  if (!['darwin', 'linux'].includes(process.platform)) throw new Error('Fugato supports macOS and Linux.');
  const profile = profiles[agent];
  const root = fs.realpathSync(project);
  if (!fs.statSync(root).isDirectory()) throw new Error('Project must be an existing directory.');
  const source = skillDir || [path.join(scriptDir, 'skills/fugato-workflow'), path.join(scriptDir, '../skills/fugato-workflow')].find(dir => fs.existsSync(path.join(dir, 'SKILL.md')));
  if (!source) throw new Error('Missing bundled fugato-workflow skill. Use the complete release or source checkout.');
  const cli = findBinary('fugato', binDir);
  const mcp = findBinary('fugato-mcp', binDir);
  const configHome = process.platform === 'darwin' ? path.join(os.homedir(), 'Library/Application Support') : (process.env.XDG_CONFIG_HOME || path.join(os.homedir(), '.config'));
  const state = process.env.FUGATO_STATE_DIR || path.join(configHome, 'fugato');
  if (!path.isAbsolute(state)) throw new Error('Fugato state directory must be absolute.');
  const apiPort = process.env.FUGATO_API_PORT || '46655';
  const proxyPort = process.env.FUGATO_PROXY_PORT || '46656';
  for (const port of [apiPort, proxyPort]) if (!/^\d+$/.test(port) || +port < 1 || +port > 65535) throw new Error('Fugato ports must be between 1 and 65535.');
  if (+apiPort === +proxyPort) throw new Error('Fugato API and preview ports must differ.');
  const launcher = safeTarget(root, '.fugato-agent/mcp-launch.sh');
  const plan = [];
  function add(relative, data, preserve = false) {
    const file = safeTarget(root, relative);
    const before = regular(file);
    const after = Buffer.from(data);
    if (before?.equals(after)) return;
    if (before && !preserve) throw new Error(`Existing file differs; review it before reinstalling: ${file}`);
    plan.push({ file, before, after });
  }
  for (const file of skillFiles(source)) add(path.join(profile.skill, 'fugato-workflow', file), fs.readFileSync(path.join(source, file)));
  if (!fs.existsSync(path.join(source, 'SKILL.md')) || !fs.existsSync(path.join(source, 'references/mcp.md'))) throw new Error('Incomplete workflow skill.');
  add('.fugato-agent/mcp-launch.sh', `#!/bin/sh\nset -eu\nexport FUGATO_STATE_DIR=${quote(state)}\nexport FUGATO_API_PORT=${quote(apiPort)}\nexport FUGATO_PROXY_PORT=${quote(proxyPort)}\nexport FUGATO_API_URL=${quote('http://127.0.0.1:' + apiPort)}\nexport FUGATO_API_TOKEN_FILE=${quote(path.join(state, 'token'))}\n${quote(cli)} ls >/dev/null\nexec ${quote(mcp)} "$@"\n`);

  const instructionFile = safeTarget(root, profile.instructions);
  const prior = regular(instructionFile)?.toString() || '';
  const block = `${start}\nFor tasks that configure, run, inspect, verify, or stop this project's local environments, read ${profile.skill}/fugato-workflow/SKILL.md and follow its referenced guidance. Use the Fugato MCP server for supported runtime inspection, lifecycle, logs, and verification; use the CLI for configuration, preparation, and named tasks/checks. Match the exact project path and runtime before acting. Respect the user's requested scope and the agent's permissions. Report checks actually run and any failures.\n${end}`;
  let instructions;
  if (prior.includes(start) || prior.includes(end)) {
    if (prior.split(start).length !== 2 || prior.split(end).length !== 2 || prior.indexOf(end) < prior.indexOf(start)) throw new Error(`Malformed Fugato instruction markers: ${instructionFile}`);
    instructions = prior.slice(0, prior.indexOf(start)) + block + prior.slice(prior.indexOf(end) + end.length);
  } else {
    if (agent === 'cursor' && prior) throw new Error('Existing fugato.mdc is not managed by this installer.');
    instructions = (prior ? prior.trimEnd() + '\n\n' : agent === 'cursor' ? '---\nalwaysApply: true\n---\n\n' : '') + block + '\n';
  }
  add(profile.instructions, instructions, true);

  const configFile = safeTarget(root, profile.config);
  const configText = regular(configFile)?.toString() || '';
  if (agent === 'codex') {
    const table = `\n[mcp_servers.fugato]\ncommand = "/bin/sh"\nargs = [${JSON.stringify(launcher)}]\n`;
    if (!configText.endsWith(table)) {
      // Do not guess at arbitrary existing TOML layouts or replace an existing server.
      if (configText.includes('mcp_servers') && (configText.includes('fugato') || /\[\s*["']?mcp_servers["']?\s*\]|^\s*["']?mcp_servers["']?\s*=/m.test(configText))) throw new Error(`Review existing MCP TOML before adding Fugato: ${configFile}`);
      add(profile.config, configText + table, true);
    }
  } else {
    let config;
    try { config = configText ? JSON.parse(configText) : {}; }
    catch { throw new Error(`Expected strict JSON; preserve and convert comments/trailing commas before setup: ${configFile}`); }
    if (!object(config) || (config[profile.key] !== undefined && !object(config[profile.key]))) throw new Error(`Invalid MCP configuration: ${configFile}`);
    config[profile.key] ??= {};
    const server = { command: '/bin/sh', args: [launcher] };
    if (agent !== 'gemini-cli') server.type = 'stdio';
    const existing = config[profile.key].fugato;
    if (existing && JSON.stringify(existing) !== JSON.stringify(server)) throw new Error(`Existing fugato MCP server differs: ${configFile}`);
    config[profile.key].fugato = server;
    add(profile.config, JSON.stringify(config, null, 2) + '\n', true);
  }
  // Validate all targets before changing anything. Recheck each file before writing.
  if (!dryRun) for (const change of plan) {
    safeTarget(root, path.relative(root, change.file));
    const current = regular(change.file);
    if (current === undefined ? change.before !== undefined : !current.equals(change.before || Buffer.alloc(0))) throw new Error(`File changed during setup: ${change.file}`);
    fs.mkdirSync(path.dirname(change.file), { recursive: true });
    const temporary = `${change.file}.fugato-${process.pid}.tmp`;
    const mode = current ? fs.statSync(change.file).mode & 0o777 : 0o600;
    let created = false;
    try {
      fs.writeFileSync(temporary, change.after, { flag: 'wx', mode });
      created = true;
      fs.renameSync(temporary, change.file);
    } finally { if (created && fs.existsSync(temporary)) fs.unlinkSync(temporary); }
  }
  return { agent, project: root, files: plan.map(item => path.relative(root, item.file)), launcher, dryRun };
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const { values } = parseArgs({ options: { agent: { type: 'string' }, project: { type: 'string' }, 'bin-dir': { type: 'string' }, 'dry-run': { type: 'boolean' }, help: { type: 'boolean' } } });
    if (values.help) console.log(`Usage: node setup-agent.mjs --agent NAME [--project DIR] [--bin-dir DIR] [--dry-run]\nAgents: ${Object.keys(profiles).join(', ')}\nInstalls project skill, persistent instructions, and MCP configuration. No global settings are changed.`);
    else {
      const result = setupAgent({ agent: values.agent, project: values.project, binDir: values['bin-dir'], dryRun: values['dry-run'] });
      console.log(JSON.stringify(result, null, 2));
      console.log('Restart your agent in this project and review its MCP trust prompt. Verify the fugato-workflow skill and fugato MCP tools are visible.');
      console.log(`Connection check (starts the local daemon): /bin/sh ${quote(result.launcher)} --check`);
      console.log('MCP configuration and launcher contain machine-specific paths. Re-run setup after moving the project; do not share them unchanged.');
    }
  } catch (error) { console.error(`Fugato agent setup: ${error.message}`); process.exitCode = 1; }
}
