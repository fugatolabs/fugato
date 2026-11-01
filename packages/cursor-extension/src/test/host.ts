import * as assert from 'node:assert/strict';
import * as vscode from 'vscode';
import * as fs from 'node:fs/promises';
import * as path from 'node:path';
import * as http from 'node:http';
import { Runtime } from '../contracts';

export async function run(): Promise<void> {
  const extension = vscode.extensions.getExtension('fugato-local.fugato');
  assert.ok(extension, 'Fugato extension should be installed in the development host');
  await extension.activate();
  const commands = await vscode.commands.getCommands(true);
  for (const command of ['refresh','selectWorktree','createEnvironment','createRuntime','chooseDirectory','createWorktree','removeWorktree','openWorktree','restartRuntime','startRuntime','stopRuntime','destroyRuntime','openPreview','copyPreviewLink','showLogs','verifyHTTP','verifyBrowser','showLatestVerification','openScreenshot','startDaemon','selectProject','checkSetup','editConfiguration','showPorts','runCommand','runCheck','openService','copyServiceAddress']) { assert.ok(commands.includes(`fugato.${command}`), `${command} registered`); }
  await vscode.commands.executeCommand('fugato.refresh');
  assert.equal(extension.isActive, true, 'Absent daemon must remain a setup state');

  const state = process.env.FUGATO_STATE_DIR!;
  await fs.mkdir(state, { recursive: true });
  await fs.writeFile(path.join(state, 'token'), 'a'.repeat(64), { mode: 0o600 });
  const root = vscode.workspace.workspaceFolders![0].uri.fsPath;
  const repository = path.join(root, '.git');
  const a: Runtime = { id: 'aaaaaaaaaaaaaaaa', repository, worktree: root, project_dir: root, commit: 'abc', desired: 'running', observed: 'running', preview_url: 'http://website.aaaaaaaaaaaaaaaa.localhost:46656/' };
  const b: Runtime = { ...a, id: 'bbbbbbbbbbbbbbbb', desired: 'stopped', observed: 'stopped', worktree: path.join(root, 'sibling'), project_dir: path.join(root, 'sibling'), preview_url: 'http://website.bbbbbbbbbbbbbbbb.localhost:46656/' };
  await fs.mkdir(b.worktree, { recursive: true });
  await fs.writeFile(path.join(b.worktree, '.fugato.yaml'), 'version: 1\n');
  let registered = false;
  let inventoryReads = 0;
  const mutations: string[] = [];
  const server = http.createServer((req, res) => {
    assert.equal(req.headers.authorization, `Bearer ${'a'.repeat(64)}`);
    const route = req.url!;
    const reply = (value: unknown) => { res.setHeader('Content-Type', 'application/json'); res.end(JSON.stringify(value)); };
    if (route === '/v1/info') { reply({ api_version: 'v1', instance_id: 'host-test', features: { repository_worktrees: true, project_resolution: true, latest_verification: true, environments: true, service_inventory: true, create_environment: true, destroy_environment: true, worktree_lifecycle: true } }); }
    else if (route === '/v1/agent/runtimes') { reply(registered ? [a,b] : [a]); }
    else if (route === '/v1/repositories/worktrees') { reply({ repository, worktrees: [{path: root, branch: 'main', missing: false}, {path: b.worktree, branch: 'feature', missing: false}], truncated: false }); }
    else if (route.startsWith('/v1/projects?')) {
      const worktree = new URL(route, 'http://localhost').searchParams.get('path')!;
      const paths = worktree === root ? [root, path.join(root, 'app')] : [b.worktree];
      reply({ worktree, projects: paths.map(project_dir => ({ id: project_dir === b.worktree ? b.id : a.id, project_dir, config_file: path.join(project_dir, '.fugato.yaml'), compose_files: [], kind: 'native', configured: true, commands: [], checks: [], endpoints: [{name: 'website', command: 'frontend', port: 0, protocol: 'http', browser: true}] })), truncated: false });
    }
    else if (route === '/v1/projects/resolve') { reply({ ...b, registered: false }); }
    else if (route.endsWith('/services')) {
      inventoryReads++;
      const id = route.split('/')[3];
      reply([{name:'frontend',kind:'native',state:'running',ports:[{name:'website',command:'frontend',service:'',container_port:0,protocol:'http',host_ip:'127.0.0.1',host_port:3001,browser:true,url:`http://website.${id}.localhost:46656/`}]}, {name:'db',kind:'compose',state:'running',ports:[{service:'db',container_port:5432,protocol:'tcp',host_ip:'',host_port:0}]}]);
    }
    else if (route === '/v1/runtimes/create') {
      let body = ''; req.on('data', chunk => { body += chunk; }); req.on('end', () => { assert.deepEqual(JSON.parse(body), { path: b.project_dir, expected_runtime_id: b.id }); mutations.push('create-b'); registered = true; reply(b); });
    } else if (route.endsWith('/stop')) { mutations.push(route); b.desired = 'stopped'; b.observed = 'stopped'; reply(b); }
    else if (route.endsWith('/start')) { mutations.push(route); b.desired = 'running'; b.observed = 'running'; reply(b); }
    else if (route.endsWith('/logs')) { reply({ runtime: b, text: '\u001b[31mexample log\u001b[0m', truncated: false }); }
    else if (route.endsWith('/verifications/latest')) {
      reply({ http: { report_id: 'test-report', kind: 'http', started_at: new Date().toISOString(), completed_at: new Date().toISOString(), result: { runtime: b, url: b.preview_url, ok: false, duration_ms: 12, issues: [{ kind: 'http', text: 'Example failure' }] } }, browser: null });
    } else if (route.endsWith(a.id)) { reply(a); }
    else if (route.endsWith(b.id)) { reply(b); }
    else { res.writeHead(404); reply({ error: 'unknown route' }); }
  });
  await new Promise<void>(resolve => server.listen(0, '127.0.0.1', resolve));
  try {
    const port = (server.address() as {port: number}).port;
    await vscode.workspace.getConfiguration('fugato').update('apiPort', port, vscode.ConfigurationTarget.Global);
    await vscode.commands.executeCommand('fugato.refresh');
    assert.equal(mutations.length, 0, 'Activation and discovery must not mutate runtimes');
    await vscode.commands.executeCommand('fugato.selectWorktree', { target: { repository, worktree: root } });
    assert.equal(mutations.length, 0, 'Selecting a multi-project worktree must not start anything');
    await vscode.commands.executeCommand('fugato.selectProject', { target: { repository, worktree: root, projectDir: path.join(root, 'app') } });
    assert.equal(mutations.length, 0, 'Selecting a discovered project must not start anything');
    const row = { target: { repository, worktree: b.worktree } };
    await vscode.commands.executeCommand('fugato.createEnvironment', row);
    await vscode.commands.executeCommand('fugato.selectProject', { target: { repository, worktree: b.worktree, projectDir: b.project_dir } });
    await vscode.commands.executeCommand('fugato.createRuntime');
    assert.deepEqual(mutations, ['create-b']);
    assert.equal(b.observed, 'stopped', 'Create registers an environment without starting its workloads');
    const runtimeRow = { target: { repository, worktree: b.worktree, runtime: b } };
    await vscode.commands.executeCommand('fugato.startRuntime', runtimeRow);
    await vscode.commands.executeCommand('fugato.selectWorktree', runtimeRow);
    assert.ok(inventoryReads > 0, 'Selecting a running environment loads service ports automatically');
    assert.deepEqual(mutations, ['create-b', `/v1/agent/runtimes/${b.id}/start`], 'Selecting a running runtime only opens details');
    await vscode.commands.executeCommand('fugato.editConfiguration', runtimeRow);
    const configurationPath = await fs.realpath(path.join(b.worktree, '.fugato.yaml'));
    assert.ok(vscode.workspace.textDocuments.some(document => document.uri.fsPath === configurationPath), 'Edit Configuration opens selected project YAML');
    await vscode.commands.executeCommand('fugato.stopRuntime', runtimeRow);
    assert.equal(a.observed, 'running', 'Stopping sibling leaves original runtime running');
    await vscode.commands.executeCommand('fugato.startRuntime', runtimeRow);
    await vscode.commands.executeCommand('fugato.showLogs', runtimeRow);
    await vscode.commands.executeCommand('fugato.showLatestVerification', runtimeRow);
    const document = vscode.workspace.textDocuments.find(document => document.uri.scheme === 'fugato-report');
    assert.ok(document, 'Latest report is an editor document');
    assert.equal(JSON.parse(document.getText()).http.result.ok, false, 'Failing MCP/daemon reports remain failing reports');
    assert.deepEqual(mutations, ['create-b', `/v1/agent/runtimes/${b.id}/start`, `/v1/agent/runtimes/${b.id}/stop`, `/v1/agent/runtimes/${b.id}/start`]);
    console.log('Fugato host smoke passed: discovery, stopped creation, selection, independent start/stop, logs, failing report.');
  } finally { await new Promise<void>(resolve => { server.closeAllConnections(); server.close(() => resolve()); }); }
}
