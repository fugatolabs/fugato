const path = require('node:path');
const fs = require('node:fs/promises');
const os = require('node:os');
const { runTests, resolveCliArgsFromVSCodeExecutablePath } = require('@vscode/test-electron');
const { promisify } = require('node:util');
const { execFile } = require('node:child_process');
(async () => {
  const directory = await fs.mkdtemp(path.join(os.tmpdir(), 'fugato-host-'));
  try {
    let extensionDevelopmentPath = path.resolve(__dirname, '..');
    if (process.env.FUGATO_VSIX_PATH) {
      if (!process.env.VSCODE_EXECUTABLE_PATH) { throw new Error('Set VSCODE_EXECUTABLE_PATH for VSIX installation tests'); }
      const [cli, ...args] = resolveCliArgsFromVSCodeExecutablePath(process.env.VSCODE_EXECUTABLE_PATH, { reuseMachineInstall: true });
      const installed = await promisify(execFile)(cli, [...args, '--user-data-dir', path.join(directory, 'user'), '--extensions-dir', path.join(directory, 'extensions'), '--install-extension', path.resolve(process.env.FUGATO_VSIX_PATH), '--force'], { timeout: 60000 });
      console.log(installed.stdout);
      const entries = await fs.readdir(path.join(directory, 'extensions'));
      const folder = entries.find(name => name.startsWith('fugato-local.fugato-'));
      if (!folder) { throw new Error('Installed Fugato extension was not found'); }
      extensionDevelopmentPath = path.join(directory, 'extensions', folder);
      console.log(`Testing installed VSIX at ${extensionDevelopmentPath}`);
    }
    await runTests({
      version: process.env.VSCODE_VERSION || '1.116.0',
      ...(process.env.VSCODE_EXECUTABLE_PATH ? { vscodeExecutablePath: process.env.VSCODE_EXECUTABLE_PATH } : {}),
      extensionDevelopmentPath,
      extensionTestsPath: path.resolve(__dirname, '../out/test/host.js'),
      launchArgs: [directory, '--user-data-dir', path.join(directory, 'user'), '--extensions-dir', path.join(directory, 'extensions'), '--disable-workspace-trust', '--skip-welcome', '--skip-release-notes', '--disable-gpu', ...(process.platform === 'linux' ? ['--no-sandbox'] : [])],
      extensionTestsEnv: { FUGATO_STATE_DIR: path.join(directory, 'missing-state') }
    });
  } finally { await fs.rm(directory,{recursive:true,force:true}); }
})().catch(error => { console.error(error); process.exit(1); });
