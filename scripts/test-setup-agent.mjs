import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { spawnSync } from 'node:child_process';
import { setupAgent, profiles } from './setup-agent.mjs';

function fixture(t) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'fugato-setup-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const project = path.join(root, "project with ' quote");
  const binDir = path.join(root, 'bin with space');
  fs.mkdirSync(project); fs.mkdirSync(binDir);
  fs.writeFileSync(path.join(binDir, 'fugato'), '#!/bin/sh\necho daemon-output\n', { mode: 0o755 });
  fs.writeFileSync(path.join(binDir, 'fugato-mcp'), '#!/bin/sh\nprintf \'%s\\n\' "mcp:$1" "$FUGATO_API_URL" "$FUGATO_API_TOKEN_FILE"\n', { mode: 0o755 });
  return { root, project, binDir };
}

for (const [agent, profile] of Object.entries(profiles)) test(`${agent}: install, preserve settings, discover references, and repeat safely`, t => {
  const options = { ...fixture(t), agent };
  const configFile = path.join(options.project, profile.config);
  fs.mkdirSync(path.dirname(configFile), { recursive: true });
  fs.writeFileSync(configFile, agent === 'codex' ? '# keep my settings\nmodel = "example"\n[mcp_servers.other]\ncommand = "other"\n' : JSON.stringify({ keep: true, [profile.key]: { other: { command: 'other' } } }));
  const instructions = path.join(options.project, profile.instructions);
  if (agent !== 'cursor') { fs.mkdirSync(path.dirname(instructions), { recursive: true }); fs.writeFileSync(instructions, 'Keep project conventions.\n'); }
  const plan = setupAgent({ ...options, dryRun: true });
  assert.ok(plan.files.length > 5);
  assert.equal(fs.existsSync(path.join(options.project, profile.skill)), false);
  const result = setupAgent(options);
  assert.equal(setupAgent(options).files.length, 0);
  assert.ok(fs.existsSync(path.join(options.project, profile.skill, 'fugato-workflow/references/mcp.md')));
  assert.match(fs.readFileSync(instructions, 'utf8'), /fugato-workflow\/SKILL.md/);
  if (agent !== 'cursor') assert.match(fs.readFileSync(instructions, 'utf8'), /Keep project conventions/);
  if (agent === 'codex') assert.match(fs.readFileSync(configFile, 'utf8'), /\[mcp_servers.other\]/);
  else assert.equal(JSON.parse(fs.readFileSync(configFile))[profile.key].other.command, 'other');
  const launched = spawnSync('/bin/sh', [result.launcher, '--check'], { encoding: 'utf8' });
  assert.equal(launched.status, 0, launched.stderr);
  assert.match(launched.stdout, /^mcp:--check\nhttp:\/\/127.0.0.1:/);
  assert.doesNotMatch(launched.stdout, /daemon-output/);
});

test('conflicting MCP config causes no partial skill or instruction installation', t => {
  const options = { ...fixture(t), agent: 'cursor' };
  fs.mkdirSync(path.join(options.project, '.cursor'));
  fs.writeFileSync(path.join(options.project, '.cursor/mcp.json'), '{"mcpServers":{"fugato":{"command":"mine"}}}');
  assert.throws(() => setupAgent(options), /Existing fugato/);
  assert.equal(fs.existsSync(path.join(options.project, '.agents')), false);
  assert.equal(fs.existsSync(path.join(options.project, '.fugato-agent')), false);
});
test('malformed settings and symlink destinations are rejected without writes', t => {
  const options = { ...fixture(t), agent: 'cursor' };
  fs.mkdirSync(path.join(options.project, '.cursor'));
  const configFile = path.join(options.project, '.cursor/mcp.json');
  fs.writeFileSync(configFile, '{ bad');
  assert.throws(() => setupAgent(options), /Expected strict JSON/);
  fs.unlinkSync(configFile);
  fs.symlinkSync(options.root, path.join(options.project, '.agents'));
  assert.throws(() => setupAgent(options), /Refusing symlink/);
});
test('edited skill files are preserved and reported', t => {
  const options = { ...fixture(t), agent: 'codex' };
  setupAgent(options);
  const skill = path.join(options.project, '.agents/skills/fugato-workflow/SKILL.md');
  fs.writeFileSync(skill, 'My customized skill');
  assert.throws(() => setupAgent(options), /Existing file differs/);
  assert.equal(fs.readFileSync(skill, 'utf8'), 'My customized skill');
});
test('quoted Codex MCP table conflicts are preserved', t => {
  const options = { ...fixture(t), agent: 'codex' };
  fs.mkdirSync(path.join(options.project, '.codex'));
  const file = path.join(options.project, '.codex/config.toml');
  const original = '["mcp_servers"."fugato"]\ncommand = "mine"\n';
  fs.writeFileSync(file, original);
  assert.throws(() => setupAgent(options), /Review existing MCP TOML/);
  assert.equal(fs.readFileSync(file, 'utf8'), original);
  assert.equal(fs.existsSync(path.join(options.project, '.agents')), false);
});
test('launcher stops when the daemon cannot be started', t => {
  const options = { ...fixture(t), agent: 'codex' };
  fs.writeFileSync(path.join(options.binDir, 'fugato'), '#!/bin/sh\nexit 7\n', { mode: 0o755 });
  const result = setupAgent(options);
  const launched = spawnSync('/bin/sh', [result.launcher, '--check'], { encoding: 'utf8' });
  assert.equal(launched.status, 7);
  assert.equal(launched.stdout, '');
});
