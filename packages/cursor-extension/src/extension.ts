import * as vscode from 'vscode';
import * as path from 'node:path';
import * as os from 'node:os';
import * as fs from 'node:fs/promises';
import { spawn, ChildProcess } from 'node:child_process';
import { Client, APIError } from './client';
import { Project, Runtime, Latest, Candidate, Discovery, RuntimeService, PortBinding, Info, LifecycleFeature, requireFeature, object, discovery, repository, endpointURL, within, verifyPath, servicesFromBindings, serviceAddress } from './contracts';
import { settings, Settings, screenshot, executable, plain } from './security';
import { Group, Target, groups, revalidate } from './model';
import { localJSON } from './local';
import { Row, Tree, Details, selectionIdentity } from './views';
import { Sidebar } from './sidebar';

class Reports implements vscode.TextDocumentContentProvider, vscode.Disposable {
  private emitter = new vscode.EventEmitter<vscode.Uri>();
  readonly onDidChange = this.emitter.event;
  private documents = new Map<string, { uri: vscode.Uri; value: string }>();
  provideTextDocumentContent(uri: vscode.Uri): string { return this.documents.get(uri.toString())?.value || 'No retained report; run verification.'; }
  async show(id: string, value: unknown): Promise<void> {
    const uri = vscode.Uri.from({ scheme: 'fugato-report', path: `/${id}.json` });
    this.documents.set(uri.toString(), { uri, value: JSON.stringify(value, null, 2) });
    this.emitter.fire(uri);
    await vscode.window.showTextDocument(await vscode.workspace.openTextDocument(uri), { preview: true });
  }
  invalidate(reason: string): void {
    for (const doc of this.documents.values()) {
      const previous = JSON.parse(doc.value);
      doc.value = JSON.stringify({ stale: true, reason, previous_report: previous.stale ? previous.previous_report : previous }, null, 2);
      this.emitter.fire(doc.uri);
    }
  }
  clear(reason: string): void {
    for (const doc of this.documents.values()) { doc.value = JSON.stringify({ unavailable: reason }, null, 2); this.emitter.fire(doc.uri); }
  }
  destroyed(id: string): void {
    for (const doc of this.documents.values()) {
      if (doc.uri.path === `/${id}.json`) { doc.value = JSON.stringify({ unavailable: 'Environment destroyed; retained reports are no longer current.' }, null, 2); this.emitter.fire(doc.uri); }
    }
  }
  dispose(): void { this.documents.clear(); this.emitter.dispose(); }
}

class Extension implements vscode.Disposable {
  private tree = new Tree();
  private details = new Details();
  private sidebar: Sidebar;
  private get view() { return this.sidebar.connection; }
  private get detailView() { return this.sidebar.selection; }
  private selectedWatchers: vscode.Disposable[] = []; private watchedWorktree?: string;
  private discovery?: Discovery; private selectionGeneration = 0; private discoveryTimer?: NodeJS.Timeout;
  private creating = false;
  private pendingCreation?: Project;
  private checkResults?: { key: string; results: Map<string, string> };
  private remembered: Record<string, string>;
  private serviceState?: { key: string; services: RuntimeService[]; error?: string; publishedOnly?: boolean };
  private serviceLoad?: { key: string; promise: Promise<void> };
  private supportsServices = false;
  private supportsServiceLogs = false;
  private daemonInfo?: Info;
  private status = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Left, 30);
  private output = vscode.window.createOutputChannel('Fugato Logs');
  private reports = new Reports();
  private client?: Client; private config?: Settings; private selected?: Target;
  private savedSelection?: string;
  private timer?: NodeJS.Timeout; private refreshInFlight?: Promise<void>; private generation = 0;
  private instance?: string; private failures = 0; private paused = false; private disposed = false;
  private controllers = new Set<AbortController>(); private children = new Set<ChildProcess>();
  constructor(private context: vscode.ExtensionContext) {
    this.sidebar = new Sidebar(context.extensionUri, this.tree, this.details);
    this.remembered = context.workspaceState.get<Record<string, string>>('fugato.environments', {}) || {};
    this.selected = context.workspaceState.get<Target>('fugato.selection');
    this.savedSelection = JSON.stringify(selectionIdentity(this.selected));
    this.status.command = 'fugato.selectWorktree'; this.status.text = '$(server) Fugato'; this.status.show();
    context.subscriptions.push(this.tree, this.sidebar, this.details, this.status, this.output, this.reports,
      vscode.window.registerWebviewViewProvider('fugato.sidebar', this.sidebar, { webviewOptions: { retainContextWhenHidden: true } }),
      vscode.workspace.registerTextDocumentContentProvider('fugato-report', this.reports),
      vscode.workspace.onDidChangeWorkspaceFolders(() => this.reset()),
      vscode.workspace.onDidChangeConfiguration(event => { if (event.affectsConfiguration('fugato')) { this.reset(); } }),
      vscode.window.onDidChangeWindowState(() => this.schedule()), this.sidebar.onDidChangeVisibility(() => { this.schedule(); if (this.sidebar.visible) { void this.refresh(); } }));
    const commands: Record<string, (row?: Row) => Promise<unknown>> = {
      refresh: async () => { this.paused = false; await this.refresh(); await this.loadDetails(); },
      selectWorktree: row => this.select(row), startRuntime: row => this.lifecycle('start', row), stopRuntime: row => this.lifecycle('stop', row),
      destroyRuntime: row => this.destroy(row),
      restartRuntime: row => this.restart(row), createEnvironment: row => this.createEnvironment(row),
      chooseDirectory: row => this.chooseDirectory(row), createRuntime: row => this.createRuntime(row),
      createWorktree: row => this.createWorktree(row), removeWorktree: row => this.removeWorktree(row), openWorktree: row => this.openWorktree(row),
      openPreview: row => this.preview(false, row), copyPreviewLink: row => this.preview(true, row), showLogs: row => this.logs(row),
      verifyHTTP: row => this.verify('http', row), verifyBrowser: row => this.verify('browser', row),
      showLatestVerification: row => this.showReports(row), openScreenshot: row => this.openScreenshot(row),
      selectProject: row => this.selectProject(row), checkSetup: row => this.checkSetup(row), editConfiguration: row => this.editConfiguration(row),
      showPorts: row => this.ports(row), runCommand: row => this.named('run', row), runCheck: row => this.named('check', row),
      openService: row => this.serviceAction(false, row), copyServiceAddress: row => this.serviceAction(true, row),
      startDaemon: () => this.startDaemon(), setup: () => this.setup()
    };
    for (const [name, action] of Object.entries(commands)) {
      context.subscriptions.push(vscode.commands.registerCommand(`fugato.${name}`, async (row?: Row) => {
        const generation = this.generation;
        try { this.assertHost(); await action(row); }
        catch (error) { if (generation === this.generation) { this.error(error, name); } }
      }));
    }
    void this.refresh().then(() => this.loadDetails());
  }
  private assertHost(): void {
    if (!vscode.workspace.isTrusted) { throw new Error('Trust this workspace to use Fugato: project workloads execute on your computer'); }
    if (vscode.env.remoteName || (process.platform !== 'darwin' && process.platform !== 'linux') || vscode.workspace.workspaceFolders?.some(folder => folder.uri.scheme !== 'file')) { throw new Error('Fugato supports local macOS and Linux file workspaces only'); }
    if (!vscode.workspace.workspaceFolders?.length) { throw new Error('Open a local Git repository folder to use Fugato'); }
  }
  private connection(): Client {
    this.assertHost();
    if (!this.client) {
      const config = vscode.workspace.getConfiguration('fugato');
      // Only globalValue is accepted. Workspace settings cannot select an executable or token.
      this.config = settings({ executablePath: config.inspect<string>('executablePath')?.globalValue, stateDirectory: config.inspect<string>('stateDirectory')?.globalValue, apiPort: config.inspect<number>('apiPort')?.globalValue });
      this.client = new Client(this.config);
    }
    return this.client;
  }
  private reset(): void {
    this.generation++; this.selectionGeneration++; this.discovery = undefined; this.serviceState = undefined; this.pendingCreation = undefined; this.daemonInfo = undefined; this.client?.dispose(); this.client = undefined; this.config = undefined;
    for (const controller of this.controllers) { controller.abort(); }
    this.reports.clear('Connection settings or workspace changed; refresh reports');
    this.paused = false; this.failures = 0;
    void (this.refreshInFlight || Promise.resolve()).then(async () => { await this.refresh(); await this.loadDetails(); });
  }
  private async refresh(): Promise<void> {
    if (this.disposed) { return; }
    if (this.refreshInFlight) { return this.refreshInFlight; }
    this.refreshInFlight = this.performRefresh().finally(() => { this.refreshInFlight = undefined; this.schedule(); });
    return this.refreshInFlight;
  }
  private async performRefresh(): Promise<void> {
    const generation = this.generation;
    try {
      const client = this.connection();
      if (!this.instance && !this.view.message) { this.view.message = 'Connecting to local daemon…'; }
      const info = await client.info();
      this.daemonInfo = info;
      this.tree.supportsWorktreeLifecycle = info.features.worktree_lifecycle === true;
      this.tree.supportsEnvironmentDestruction = info.features.destroy_environment === true;
      this.supportsServices = info.features?.service_inventory === true;
      this.supportsServiceLogs = info.features?.service_logs === true;
      const runtimes = await client.list();
      const repositories = await Promise.all((vscode.workspace.workspaceFolders || []).map(async folder => {
        try { const canonicalFolder = await fs.realpath(folder.uri.fsPath); return { folder, canonicalFolder, repository: await client.worktrees(canonicalFolder) }; }
        catch (error) { if (error instanceof APIError && error.status !== 401) { return { folder, error: String(error) }; } throw error; }
      }));
      if (generation !== this.generation || this.disposed) { return; }
      if (this.instance && this.instance !== info.instance_id) { this.reports.clear('Daemon restarted; run verification to create a new report'); }
      this.instance = info.instance_id;
      const entries = repositories.flatMap(r => r.repository ? [r.repository] : []);
      const rows = groups(entries, runtimes);
      for (const row of rows) {
        const entry = repositories.find(r => r.repository?.repository === row.repository.repository);
        if (entry) {
          const worktree = row.repository.worktrees.filter(w => within(w.path, entry.canonicalFolder || entry.folder.uri.fsPath)).sort((a,b) => b.path.length - a.path.length)[0];
          row.candidateRelative = worktree ? path.relative(worktree.path, entry.canonicalFolder || entry.folder.uri.fsPath) : '';
        }
      }
      this.tree.rows = rows; this.tree.stale = false; this.failures = 0; this.paused = false;
      this.tree.workspacePaths = repositories.flatMap(entry => entry.canonicalFolder ? [entry.canonicalFolder] : []);
      this.refreshSelection(rows); await this.saveSelection();
      await this.loadServices();
      if (generation !== this.generation || this.disposed) { return; }
      const message = repositories.some(r => r.error) ? 'Some workspace folders are not Git repositories' : undefined;
      if (this.view.message !== message) { this.view.message = message; }
      this.tree.refresh(); this.updateStatus(); this.renderDetails();
    } catch (error) {
      if (generation !== this.generation || this.disposed) { return; }
      this.tree.stale = true; this.failures++;
      // The CLI's read-only discovery works even when no daemon has been started.
      if (vscode.workspace.isTrusted) {
        try {
          const repositories = await Promise.all((vscode.workspace.workspaceFolders || []).map(async folder => repository(await this.cli(['worktrees', '--worktree', folder.uri.fsPath, '--json']))));
          if (generation !== this.generation || this.disposed) { return; }
          this.tree.rows = groups(repositories, this.tree.rows.flatMap(group => group.runtimes));
          this.refreshSelection(this.tree.rows); await this.saveSelection();
        } catch { /* Preserve the original daemon/setup diagnostic. */ }
      }
      this.serviceState = undefined; this.tree.refresh(); this.renderDetails();
      const message = plain(error instanceof Error ? error.message : String(error));
      this.paused = (error instanceof APIError && [401,403,404].includes(error.status)) || /token|must be|supports local|Trust this|Open a local|update required|Malformed/.test(message);
      const state = error instanceof APIError && error.status === 401 ? 'Unauthorized' : /update required/.test(message) ? 'Incompatible daemon' : 'Unavailable';
      const diagnostic = `${state}: ${message}. Use Refresh, Start Daemon, or Show Setup Instructions.`;
      if (this.view.message !== diagnostic) { this.view.message = diagnostic; }
      if (this.failures === 1) { this.reports.invalidate('Daemon unavailable; cached evidence is stale'); }
      if (this.status.text !== '$(warning) Fugato: unavailable') { this.status.text = '$(warning) Fugato: unavailable'; }
      if (this.status.tooltip !== diagnostic) { this.status.tooltip = diagnostic; }
    }
  }
  private schedule(): void {
    if (this.timer) { clearTimeout(this.timer); }
    if (this.disposed || this.paused || !vscode.window.state.focused || !this.sidebar.visible) { return; }
    const delay = this.failures ? Math.min(30000, 5000 * 2 ** Math.min(this.failures, 3)) + Math.floor(Math.random() * 500) : 5000;
    this.timer = setTimeout(() => { void this.refresh(); }, delay);
  }
  private updateStatus(): void {
    const runtime = this.selected?.runtime;
    const text = this.selected ? `$(server) ${path.basename(this.selected.worktree)}: ${runtime ? this.tree.busy.get(runtime.id) || runtime.observed : this.selected.projectDir ? 'not started' : 'select project'}` : '$(server) Fugato: select worktree';
    const tooltip = runtime ? `${runtime.project_dir}\n${runtime.preview_url || ''}\nDaemon-reported state` : this.selected?.worktree || 'Select a worktree';
    if (this.status.text !== text) { this.status.text = text; }
    if (this.status.tooltip !== tooltip) { this.status.tooltip = tooltip; }
  }
  private async saveSelection(): Promise<void> {
    if (this.selected?.runtime && this.remembered[this.selected.worktree] !== this.selected.runtime.id) {
      this.remembered[this.selected.worktree] = this.selected.runtime.id;
      await this.context.workspaceState.update('fugato.environments', this.remembered);
    }
    const next = JSON.stringify(selectionIdentity(this.selected));
    if (next !== this.savedSelection) {
      await this.context.workspaceState.update('fugato.selection', this.selected); this.savedSelection = next;
    }
    this.updateStatus();
    if (this.watchedWorktree === this.selected?.worktree) { return; }
    for (const watcher of this.selectedWatchers) { watcher.dispose(); }
    this.selectedWatchers = []; this.watchedWorktree = this.selected?.worktree;
    if (!this.watchedWorktree) { return; }
    const watcher = vscode.workspace.createFileSystemWatcher(new vscode.RelativePattern(this.watchedWorktree, '**/{.fugato.yaml,compose.yaml,compose.yml,docker-compose.yaml,docker-compose.yml,package.json}'));
    this.selectedWatchers.push(watcher, watcher.onDidChange(() => this.configurationChanged()), watcher.onDidCreate(() => this.configurationChanged()), watcher.onDidDelete(() => this.configurationChanged()));
  }
  private async select(row?: Row): Promise<Target | undefined> {
    const target = row?.target ? revalidate(row.target, this.tree.rows) : undefined;
    if (row?.target && !target) { throw new Error('Worktree selection changed; refresh and select it again'); }
    const items = this.tree.rows.flatMap(group => group.repository.worktrees.map(w => ({ label: w.branch || path.basename(w.path), description: w.path, target: { repository: group.repository.repository, worktree: w.path } as Target })));
    this.selected = target || (await vscode.window.showQuickPick(items, { title: 'Select a Fugato worktree', matchOnDescription: true }))?.target || this.selected;
    this.creating = false; this.pendingCreation = undefined;
    this.selected = this.environmentSelection(this.selected);
    this.sidebar.showLogs(undefined);
    this.selectionGeneration++; this.discovery = undefined;
    this.sidebar.openManagement();
    await this.saveSelection(); await this.loadDetails();
    return this.selected;
  }
  private environmentSelection(target: Target | undefined): Target | undefined {
    if (!target || target.runtime || this.creating) { return target; }
    const runtimes = this.tree.rows.find(group => group.repository.repository === target.repository)?.runtimes.filter(runtime => runtime.worktree === target.worktree) || [];
    const runtime = runtimes.find(runtime => runtime.id === this.remembered[target.worktree]) || runtimes[0];
    return { ...target, runtime, projectDir: runtime?.project_dir || target.projectDir };
  }
  private refreshSelection(rows: Group[]): void {
    const previous = this.selected;
    let target = revalidate(previous, rows);
    if (!target && previous && rows.some(group => group.repository.repository === previous.repository && group.repository.worktrees.some(worktree => worktree.path === previous.worktree))) {
      target = { repository: previous.repository, worktree: previous.worktree };
    }
    this.selected = this.environmentSelection(target);
    const pending = this.pendingCreation;
    const runtime = this.selected?.runtime;
    if (this.creating && pending && runtime?.id === pending.id && runtime.repository === pending.repository && runtime.worktree === pending.worktree && runtime.project_dir === pending.project_dir) {
      // A create response can be lost or malformed after registration commits.
      // Only refreshed evidence for this exact attempt may leave setup automatically.
      this.creating = false; this.pendingCreation = undefined;
    }
  }
  private async target(row?: Row): Promise<Target | undefined> {
    if (this.tree.stale) { await this.refresh(); }
    if (this.tree.stale) { throw new Error('Daemon unavailable; reconnect before acting on a worktree'); }
    let target = row?.target || this.selected;
    if (target) { target = revalidate(target, this.tree.rows); if (!target) { throw new Error('Worktree selection no longer exists; select it again'); } }
    if (!target) { target = await this.select(); }
    return target;
  }
  private async project(target: Target, allowResolve: boolean): Promise<Project | undefined> {
    const generation = this.generation; const client = this.connection();
    const group = this.tree.rows.find(g => g.repository.repository === target.repository);
    if (!group) { throw new Error('Open or add this repository to the workspace first'); }
    const registered = target.runtime || group.runtimes.find(runtime => runtime.worktree === target.worktree && runtime.project_dir === target.projectDir);
    if (registered) { return { ...registered, registered: true }; }
    const selection = this.selectionGeneration;
    const discovered = await this.candidate(target);
    this.current(client, generation);
    if (!discovered) { return undefined; }
    if (!allowResolve) {
      const existing = group.runtimes.find(runtime => runtime.worktree === target.worktree && runtime.project_dir === discovered.project_dir);
      if (!existing) { throw new Error('Create this environment before starting workloads'); }
      return { ...existing, registered: true };
    }
    if (discovered.error || !discovered.configured && !discovered.compose_files.length) { throw new Error(discovered.error || 'Configure this project before starting it'); }
    const resolved = await client.resolve(discovered.project_dir);
    this.current(client, generation);
    if (selection !== this.selectionGeneration) { throw new Error('Selection changed; choose the project again'); }
    if (resolved.repository !== target.repository || resolved.worktree !== target.worktree || resolved.project_dir !== discovered.project_dir) { throw new Error('Project identity changed'); }
    return resolved;
  }
  private current(client: Client, generation: number): void {
    if (this.disposed || this.client !== client || this.generation !== generation) { throw new Error('Fugato connection changed; select the project again'); }
  }
  private async requireCapability(feature: LifecycleFeature): Promise<void> {
    const client = this.connection(); const generation = this.generation; const selection = this.selectionGeneration;
    const info = await client.info(); this.current(client, generation);
    if (selection !== this.selectionGeneration) { throw new Error('Selection changed; choose the action again'); }
    requireFeature(info, feature);
  }
  private async operation<T>(project: Project, label: string, action: (signal: AbortSignal) => Promise<T>): Promise<T | undefined> {
    if (this.tree.busy.has(project.id)) { throw new Error('An action is already running for this project'); }
    this.tree.busy.set(project.id, label); this.tree.refresh(); this.updateStatus(); this.renderDetails();
    const controller = new AbortController(); this.controllers.add(controller);
    try {
      return await vscode.window.withProgress({ location: vscode.ProgressLocation.Notification, title: `${label} ${project.project_dir}`, cancellable: true }, async (_, token) => {
        const subscription = token.onCancellationRequested(() => controller.abort());
        try { return await action(controller.signal); } finally { subscription.dispose(); }
      });
    } catch (error) {
      const rejected = error instanceof APIError && error.status >= 400 && error.status < 500;
      if (rejected) { throw error; }
      throw new Error(`${error instanceof Error ? error.message : String(error)}. Outcome may be uncertain; state will refresh. The action has not been retried.`);
    }
    finally { this.controllers.delete(controller); this.tree.busy.delete(project.id); this.tree.refresh(); await this.refresh(); }
  }
  private async lifecycle(action: 'start' | 'stop', row?: Row): Promise<void> {
    const target = await this.target(row); if (!target) { return; }
    const project = await this.project(target, false); if (!project) { return; }
    const client = this.connection(); const generation = this.generation;
    const previousSelection = this.selected; const selectionGeneration = this.selectionGeneration;
    await this.operation(project, action === 'start' ? 'Starting…' : 'Stopping (keep data)…', signal => action === 'start' ? client.start(project, signal) : client.stop(project.id, signal));
    this.current(client, generation);
    const runtime = await client.get(project.id);
    this.current(client, generation);
    if (selectionGeneration !== this.selectionGeneration) { return; }
    if (this.selected && previousSelection && (this.selected.repository !== previousSelection.repository || this.selected.worktree !== previousSelection.worktree)) { return; }
    this.selected = revalidate({ repository: project.repository, worktree: project.worktree, runtime, projectDir: project.project_dir }, this.tree.rows); await this.saveSelection(); this.renderDetails();
    await this.loadServices(); this.renderDetails();
  }
  private async chosen(row?: Row): Promise<Runtime | undefined> {
    const target = await this.target(row); if (!target) { return undefined; }
    const client = this.connection(); const generation = this.generation;
    const project = await this.project(target, false); if (!project) { return undefined; }
    this.current(client, generation);
    const runtime = await client.get(project.id);
    this.current(client, generation);
    if (runtime.repository !== target.repository || runtime.worktree !== target.worktree) { throw new Error('Runtime moved outside the selected worktree'); }
    return runtime;
  }
  private async restart(row?: Row): Promise<void> {
    const runtime = await this.chosen(row); if (!runtime) { return; }
    const client = this.connection(); const generation = this.generation;
    const project = { ...runtime, registered: true };
    await this.operation(project, 'Restarting…', async signal => { await client.stop(runtime.id, signal); if (!signal.aborted) { await client.start(project, signal); } });
    this.current(client, generation); await this.loadDetails();
  }
  private async createEnvironment(row?: Row): Promise<void> {
    const target = await this.target(row); if (!target) { return; }
    const existing = this.tree.rows.find(group => group.repository.repository === target.repository)?.runtimes.filter(runtime => runtime.worktree === target.worktree) || [];
    if (existing.length) { await this.select({ target } as Row); return; }
    await this.requireCapability('create_environment');
    this.selected = { repository: target.repository, worktree: target.worktree };
    this.creating = true; this.pendingCreation = undefined; this.selectionGeneration++; this.discovery = undefined; this.sidebar.showLogs(undefined);
    this.sidebar.openManagement(); await this.saveSelection(); await this.loadDetails();
  }
  private async chooseDirectory(row?: Row): Promise<void> {
    const target = await this.detailTarget(row); if (!target || !this.creating) { return; }
    const selection = this.selectionGeneration;
    const files = await vscode.window.showOpenDialog({ title: 'Choose a project inside this worktree', defaultUri: vscode.Uri.file(target.worktree), canSelectFolders: true, canSelectFiles: false, canSelectMany: false });
    if (!files?.length || selection !== this.selectionGeneration) { return; }
    const directory = await fs.realpath(files[0].fsPath);
    if (!within(await fs.realpath(target.worktree), directory)) { throw new Error('Choose a project inside the selected worktree'); }
    if (selection !== this.selectionGeneration) { return; }
    const discovered = await this.discover(target);
    if (selection !== this.selectionGeneration) { return; }
    if (!discovered.projects.some(project => project.project_dir === directory)) {
      discovered.projects.push({ project_dir: directory, configured: false, kind: 'directory', compose_files: [], commands: [], checks: [], endpoints: [] });
    }
    this.discovery = discovered;
    await this.selectProject({ target: { ...target, runtime: undefined, projectDir: directory } } as Row);
  }
  private async createRuntime(row?: Row): Promise<void> {
    const target = await this.target(row); if (!target || !this.creating || !target.projectDir) { return; }
    if (this.tree.rows.find(group => group.repository.repository === target.repository)?.runtimes.some(runtime => runtime.worktree === target.worktree)) {
      await this.select({ target: { repository: target.repository, worktree: target.worktree } } as Row); return;
    }
    await this.requireCapability('create_environment');
    const client = this.connection(); const generation = this.generation; const selection = this.selectionGeneration;
    const resolved = await client.resolve(target.projectDir); this.current(client, generation);
    if (selection !== this.selectionGeneration) { return; }
    if (resolved.repository !== target.repository || resolved.worktree !== target.worktree || resolved.project_dir !== target.projectDir) { throw new Error('Project identity changed'); }
    this.pendingCreation = resolved;
    const runtime = await this.operation(resolved, 'Creating environment…', signal => client.create(resolved, signal));
    this.current(client, generation);
    if (!runtime || selection !== this.selectionGeneration) { return; }
    this.creating = false; this.pendingCreation = undefined;
    this.selected = { repository: target.repository, worktree: target.worktree, projectDir: runtime.project_dir, runtime };
    await this.saveSelection(); await this.loadDetails();
  }
  private async openWorktree(row?: Row): Promise<void> {
    const target = await this.detailTarget(row); if (!target) { return; }
    await vscode.commands.executeCommand('vscode.openFolder', vscode.Uri.file(target.worktree), { forceNewWindow: true });
  }
  private async createWorktree(row?: Row): Promise<void> {
    const target = await this.target(row); if (!target) { return; }
    await this.requireCapability('worktree_lifecycle');
    const client = this.connection(); const generation = this.generation; const selection = this.selectionGeneration;
    const mode = await vscode.window.showQuickPick([{ label: 'Existing branch', newBranch: false }, { label: 'New branch from HEAD', newBranch: true }], { title: 'Create worktree' });
    if (!mode) { return; }
    const branch = await vscode.window.showInputBox({ title: mode.newBranch ? 'New branch name' : 'Existing branch name', prompt: 'Local Git branch' });
    if (!branch) { return; }
    const parent = await vscode.window.showOpenDialog({ title: 'Choose the parent directory for the new worktree', defaultUri: vscode.Uri.file(path.dirname(target.worktree)), canSelectFolders: true, canSelectFiles: false, canSelectMany: false });
    if (!parent?.length) { return; }
    const folder = await vscode.window.showInputBox({ title: 'New worktree folder name', value: branch.replaceAll('/', '-'), validateInput: value => !value || value === '.' || value === '..' || /[/\\\x00]/.test(value) ? 'Enter one folder name' : undefined });
    if (!folder || folder === '.' || folder === '..' || /[/\\\x00]/.test(folder)) { return; }
    this.current(client, generation);
    if (selection !== this.selectionGeneration) { return; }
    const targetPath = path.join(parent[0].fsPath, folder);
    await client.createWorktree(target.worktree, branch, targetPath, mode.newBranch);
    this.current(client, generation); await this.refresh();
    await this.select({ target: { repository: target.repository, worktree: await fs.realpath(targetPath) } } as Row);
  }
  private async removeWorktree(row?: Row): Promise<void> {
    const target = await this.target(row); if (!target) { return; }
    await this.requireCapability('worktree_lifecycle');
    const group = this.tree.rows.find(group => group.repository.repository === target.repository);
    const environments = group?.runtimes.filter(runtime => runtime.worktree === target.worktree) || [];
    if (environments.length) { throw new Error(`Destroy the ${environments.length === 1 ? 'environment' : 'environments'} first.`); }
    const client = this.connection(); const generation = this.generation; const selection = this.selectionGeneration;
    const confirmed = await vscode.window.showWarningMessage(`Remove worktree “${plain(path.basename(target.worktree))}”?`, { modal: true, detail: `Worktree: ${plain(target.worktree)}\n\nDeletes this clean worktree folder and removes its Git registration. The branch is retained. Primary and modified worktrees cannot be removed.` }, 'Remove worktree');
    this.current(client, generation);
    if (confirmed !== 'Remove worktree' || selection !== this.selectionGeneration) { return; }
    const source = group?.repository.worktrees.find(worktree => !worktree.missing && worktree.path !== target.worktree)?.path;
    if (!source) { throw new Error('A primary worktree is required to remove this worktree'); }
    try { await client.removeWorktree(source, target.worktree); }
    catch (error) {
      this.current(client, generation);
      if (!(error instanceof APIError) || !error.message.startsWith('cannot remove a worktree with uncommitted, untracked, or ignored files')) { throw error; }
      const guidance = error.message.includes('\n\nLocal files:') ? error.message.slice(error.message.indexOf('\n\n') + 2) : 'Commit or stash tracked changes. Move untracked or ignored files you want to keep outside this worktree, then try removal again. Ignored files may contain local configuration or secrets.';
      const action = await vscode.window.showWarningMessage('Worktree was not removed', { modal: true, detail: `Worktree: ${plain(target.worktree)}\n\n${plain(guidance)}` }, 'Open folder');
      this.current(client, generation);
      if (action === 'Open folder') { await vscode.commands.executeCommand('vscode.openFolder', vscode.Uri.file(target.worktree), { forceNewWindow: true }); }
      return;
    }
    this.current(client, generation);
    await this.refresh(); await this.loadDetails();
  }
  private async destroy(row?: Row): Promise<void> {
    const target = await this.target(row); if (!target) { return; }
    await this.requireCapability('destroy_environment');
    const client = this.connection(); const generation = this.generation; const selection = this.selectionGeneration;
    const candidates = this.tree.rows.find(group => group.repository.repository === target.repository)?.runtimes.filter(runtime =>
      runtime.worktree === target.worktree && (!target.runtime || runtime.id === target.runtime.id) && (!target.projectDir || runtime.project_dir === target.projectDir)) || [];
    if (!candidates.length) { throw new Error('No registered environment to destroy; refresh and choose an environment'); }
    const runtime = candidates.length === 1 ? candidates[0] : (await vscode.window.showQuickPick(candidates.map(runtime => ({
      label: path.basename(runtime.project_dir), description: runtime.id, detail: runtime.project_dir, runtime
    })), { title: 'Choose an environment to destroy', matchOnDescription: true }))?.runtime;
    this.current(client, generation);
    if (!runtime || selection !== this.selectionGeneration) { return; }
    if (this.tree.busy.has(runtime.id)) { throw new Error('An action is already running for this project'); }
    const confirmed = await vscode.window.showWarningMessage(`Destroy environment “${plain(path.basename(runtime.project_dir))}”?`, {
      modal: true,
      detail: `Environment: ${runtime.id}\nWorktree: ${plain(runtime.worktree)}\nProject: ${plain(runtime.project_dir)}\n\nStops the environment, removes its owned containers and networks, and forgets its registration. Volumes and native data are retained. The worktree folder is not deleted.`
    }, 'Destroy environment');
    this.current(client, generation);
    if (confirmed !== 'Destroy environment' || selection !== this.selectionGeneration) { return; }
    const current = await client.get(runtime.id);
    this.current(client, generation);
    if (selection !== this.selectionGeneration) { return; }
    if (current.id !== runtime.id || current.repository !== runtime.repository || current.worktree !== runtime.worktree || current.project_dir !== runtime.project_dir) {
      throw new Error('Environment identity changed; refresh and choose it again');
    }
    await this.operation({ ...current, registered: true }, 'Destroying (keep data)…', async signal => {
      await client.destroy(current.id, signal);
      this.current(client, generation); this.reports.destroyed(current.id);
    });
  }
  private async preview(copy: boolean, row?: Row): Promise<void> {
    const runtime = await this.chosen(row); if (!runtime) { return; }
    const client = this.connection(); const generation = this.generation; const selection = this.selectionGeneration;
    const ports = (await client.ports(runtime.id)).filter(port => port.browser);
    this.current(client, generation);
    if (!ports.length) { throw new Error('This project has no available browser endpoints'); }
    const choice = ports.length === 1 ? ports[0] : (await vscode.window.showQuickPick(ports.map(port => ({ label: port.name || 'Preview', description: port.url, port })), { title: 'Select browser endpoint' }))?.port;
    this.current(client, generation);
    if (!choice || selection !== this.selectionGeneration) { return; }
    const url = endpointURL(choice, runtime.id);
    if (copy) { await vscode.env.clipboard.writeText(url); void vscode.window.showInformationMessage('Preview link copied'); }
    else if (!(await vscode.env.openExternal(vscode.Uri.parse(url)))) { throw new Error('The browser could not open the preview; use Copy Preview Link'); }
  }
  private async logs(row?: Row): Promise<void> {
    const runtime = await this.chosen(row); if (!runtime) { return; }
    const client = this.connection(); const generation = this.generation;
    const selection = this.selectionGeneration;
    if (row?.service && (!this.supportsServiceLogs || !this.serviceState?.services.some(service => service.name === row.service && service.kind === row.serviceKind))) { throw new Error('Service selection changed; refresh logs'); }
    const logs = await client.logs(runtime.id, row?.service, row?.serviceKind);
    this.current(client, generation);
    this.output.clear(); this.output.appendLine(`${runtime.project_dir} · ${runtime.id}\nRecent logs at ${new Date().toISOString()}${logs.truncated ? ' (truncated)' : ''}\n`);
    this.output.append(plain(logs.text));
    if (this.selected?.runtime?.id === runtime.id && selection === this.selectionGeneration) { this.sidebar.showLogs(plain(logs.text) + (logs.truncated ? '\n[Output truncated to recent logs]' : '')); this.sidebar.openManagement(row ? undefined : 'logs'); }
  }
  private async verify(kind: 'http' | 'browser', row?: Row): Promise<void> {
    const runtime = await this.chosen(row); if (!runtime) { return; }
    const client = this.connection(); const generation = this.generation; const selection = this.selectionGeneration;
    const result = await client.projects(runtime.worktree);
    this.current(client, generation); if (selection !== this.selectionGeneration) { return; }
    const project = result.projects.find(project => project.project_dir === runtime.project_dir);
    if (!project) { throw new Error('Project no longer exists; refresh discovery'); }
    const endpoints = project.endpoints.filter(endpoint => ['http', 'https'].includes(endpoint.protocol) && (kind === 'http' || endpoint.browser && endpoint.service));
    if (!endpoints.length) { throw new Error(kind === 'http' ? 'This project has no HTTP(S) endpoint to verify' : 'Browser verification requires a Compose browser endpoint and runner; native endpoints support HTTP verification and named checks'); }
    const choice = endpoints.length === 1 ? endpoints[0] : (await vscode.window.showQuickPick(endpoints.map(endpoint => ({ label: endpoint.name, description: `${endpoint.protocol} · ${endpoint.service || endpoint.command}`, endpoint })), { title: `Select endpoint for ${kind} verification` }))?.endpoint;
    this.current(client, generation); if (!choice || selection !== this.selectionGeneration) { return; }
    const endpoint = choice.name;
    const value = await vscode.window.showInputBox({ title: `Verify ${kind}`, prompt: 'Relative URL path to verify', value: '/', validateInput: input => { try { verifyPath(input); return undefined; } catch (error) { return (error as Error).message; } } });
    this.current(client, generation);
    if (value === undefined || selection !== this.selectionGeneration) { return; }
    const verification = await this.operation({ ...runtime, registered: true }, 'Verifying…', signal => client.verify(runtime.id, kind, value, signal, endpoint));
    this.current(client, generation);
    if (verification) { void vscode.window.showInformationMessage(`Fugato ${kind} verification ${verification.ok ? 'passed' : 'failed'}`); await this.displayReports(await client.get(runtime.id)); }
  }
  private async displayReports(runtime: Runtime): Promise<void> {
    const client = this.connection(); const generation = this.generation;
    const reports = await client.latest(runtime.id);
    this.current(client, generation);
    await this.reports.show(runtime.id, {
      runtime_id: runtime.id, project: runtime.project_dir, worktree: runtime.worktree,
      provenance: 'Commit is HEAD at verification start; local edits may be included. Matching HEAD does not prove unchanged content.',
      current_head: runtime.commit,
      ...(!reports.http && !reports.browser ? { message: 'No retained report; run verification.' } : {}),
      http: this.reportDetail(reports.http, runtime), browser: this.reportDetail(reports.browser, runtime)
    });
  }
  private reportDetail(report: Latest['http'], runtime: Runtime): unknown { return report ? { ...report, different_head: report.result.runtime.commit !== runtime.commit } : null; }
  private async showReports(row?: Row): Promise<void> { const runtime = await this.chosen(row); if (runtime) { await this.displayReports(runtime); } }
  private async openScreenshot(row?: Row): Promise<void> {
    const runtime = await this.chosen(row); if (!runtime) { return; }
    const client = this.connection(); const generation = this.generation;
    const latest = await client.latest(runtime.id);
    this.current(client, generation);
    const filename = latest.browser?.result.screenshot_path;
    if (!filename) { throw new Error('No screenshot retained; run browser verification'); }
    const canonical = await screenshot(client.settings.stateDirectory, runtime.id, filename);
    this.current(client, generation);
    await vscode.commands.executeCommand('vscode.open', vscode.Uri.file(canonical));
  }
  private async binary(): Promise<string> {
    this.connection();
    const roots = [...await Promise.all((vscode.workspace.workspaceFolders || []).map(folder => fs.realpath(folder.uri.fsPath))), ...this.tree.rows.flatMap(group => group.repository.worktrees.map(worktree => worktree.path))];
    return executable(this.config!.executablePath, roots);
  }
  private async cli(args: string[]): Promise<unknown> {
    const client = this.connection(); const generation = this.generation;
    const binary = await this.binary(); this.current(client, generation);
    const controller = new AbortController(); this.controllers.add(controller);
    try { const value = await localJSON(binary, args, client.settings, controller.signal); this.current(client, generation); return value; }
    finally { this.controllers.delete(controller); }
  }
  private configurationChanged(): void {
    this.selectionGeneration++;
    if (this.discoveryTimer) { clearTimeout(this.discoveryTimer); }
    this.discoveryTimer = setTimeout(() => { void this.loadDetails().catch(error => this.error(error)); }, 200);
  }
  private async discover(target: Target): Promise<Discovery> {
    const value = this.tree.stale
      ? discovery(await this.cli(['projects', '--worktree', target.worktree, '--json']), target.worktree)
      : await this.connection().projects(target.worktree);
    return value;
  }
  private async loadDetails(): Promise<void> {
    const target = this.selected; const generation = this.generation; const selection = this.selectionGeneration;
    this.renderDetails();
    if (!target) { return; }
    try {
      const result = await this.discover(target);
      if (this.disposed || generation !== this.generation || selection !== this.selectionGeneration) { return; }
      this.discovery = result;
      const explicit = target.projectDir || target.runtime?.project_dir;
      if (this.creating && explicit && !result.projects.some(project => project.project_dir === explicit)) {
        result.projects.push({ project_dir: explicit, configured: false, kind: 'directory', compose_files: [], commands: [], checks: [], endpoints: [] });
      }
      const configured = result.projects.filter(project => project.configured && !project.error);
      const chosen = explicit ? result.projects.find(project => project.project_dir === explicit) : this.creating && configured.length === 1 ? configured[0] : undefined;
      const projectDir = chosen?.project_dir || explicit;
      this.selected = { ...target, projectDir, runtime: this.tree.rows.find(group => group.repository.repository === target.repository)?.runtimes.find(runtime => runtime.project_dir === projectDir && runtime.worktree === target.worktree) };
      await this.saveSelection(); await this.loadServices(); this.renderDetails();
    } catch (error) {
      if (generation !== this.generation || selection !== this.selectionGeneration || this.disposed) { return; }
      this.renderDetails(); this.detailView.message = plain(`Discovery unavailable: ${error instanceof Error ? error.message : String(error)}`);
    }
  }
  private serviceKey(): string { return `${this.generation}:${this.instance}:${this.selected?.runtime?.id}:${this.selectionGeneration}`; }
  private async loadServices(): Promise<void> {
    const runtime = this.selected?.runtime;
    if (!runtime || this.tree.stale) { this.serviceState = undefined; return; }
    // Retain service names for stopped-environment logs; running inventory is rendered only while running.
    if (runtime.observed !== 'running') { if (this.serviceState?.key !== this.serviceKey()) { this.serviceState = undefined; } return; }
    if (this.tree.busy.has(runtime.id)) { return; }
    const key = this.serviceKey();
    if (this.serviceLoad?.key === key) { return this.serviceLoad.promise; }
    const client = this.connection(); const generation = this.generation;
    const load = async () => {
      try {
        const services = this.supportsServices ? await client.services(runtime.id) : servicesFromBindings(await client.ports(runtime.id));
        if (this.disposed || key !== this.serviceKey() || generation !== this.generation || this.selected?.runtime?.observed !== 'running' || this.tree.stale) { return; }
        this.serviceState = { key, services, publishedOnly: !this.supportsServices };
      } catch (error) {
        if (!this.disposed && key === this.serviceKey() && generation === this.generation) {
          this.serviceState = { key, services: [], error: plain(error instanceof Error ? error.message : String(error)) };
        }
      }
    };
    const pending = { key, promise: load() }; this.serviceLoad = pending;
    try { await pending.promise; } finally { if (this.serviceLoad === pending) { this.serviceLoad = undefined; } }
  }
  private async serviceAction(copy: boolean, row?: Row): Promise<void> {
    const runtime = await this.chosen(row); if (!runtime || !row?.port) { return; }
    const client = this.connection(); const generation = this.generation; const selection = this.selectionGeneration;
    const ports = this.supportsServices ? (await client.services(runtime.id)).flatMap(service => service.ports) : await client.ports(runtime.id);
    this.current(client, generation);
    if (selection !== this.selectionGeneration) { return; }
    const previous = row.port;
    const matches = ports.filter(port => port.name === previous.name && port.service === previous.service && port.command === previous.command && port.container_port === previous.container_port && port.protocol === previous.protocol && port.host_port > 0);
    const binding = matches.find(port => port.host_ip === previous.host_ip && port.host_port === previous.host_port) || (matches.length === 1 ? matches[0] : undefined);
    if (!binding) { throw new Error('Service port changed; refresh and select it again'); }
    const address = serviceAddress(binding, runtime.id);
    if (copy) { await vscode.env.clipboard.writeText(address); }
    else {
      if (!/^https?:\/\//.test(address)) { throw new Error('This service has no browser URL; use Copy address'); }
      if (!(await vscode.env.openExternal(vscode.Uri.parse(address)))) { throw new Error('The browser could not open the service'); }
    }
  }
  private renderDetails(): void {
    const rows: Row[] = []; const target = this.selected;
    const item = (label: string, description?: string, command?: string, commandTarget = target, children?: Row[], collapsed = false): Row => {
      const row = new Row(label, commandTarget, undefined, children, collapsed);
      row.id = `details:${target?.worktree}:${commandTarget?.projectDir || ''}:${command || label}`;
      row.description = description && plain(description); row.tooltip = description && plain(description);
      if (command) { row.command = { command: `fugato.${command}`, title: label, arguments: [row] }; row.iconPath = new vscode.ThemeIcon(command === 'selectProject' ? 'folder' : 'arrow-right'); }
      return row;
    };
    const message = target ? undefined : 'Select an environment or worktree above.';
    if (this.detailView.message !== message) { this.detailView.message = message; }
    if (target) {
      const group = this.tree.rows.find(group => group.repository.repository === target.repository);
      const worktree = group?.repository.worktrees.find(worktree => worktree.path === target.worktree);
      const project = this.discovery?.projects.find(project => project.project_dir === target.projectDir);
      const runtime = target.runtime; const running = runtime?.observed === 'running'; const busy = this.tree.busy.get(runtime?.id || project?.id || '');
      const actions: Row[] = [];
      if (runtime) {
        const heading = item(path.basename(project?.project_dir || runtime!.project_dir), runtime?.id || project?.id);
        heading.id = `selected-environment:${target.worktree}:${project?.project_dir || runtime!.project_dir}`;
        heading.iconPath = new vscode.ThemeIcon(runtime?.last_error ? 'warning' : running ? 'pass' : 'circle-outline'); rows.push(heading);
      }
      const branch = item('Branch', worktree?.branch || '(detached HEAD)');
      branch.contextValue = group?.repository.worktrees[0]?.path === target.worktree ? 'primaryWorktree' : 'worktree';
      rows.push(branch);
      rows.push(item('Worktree path', target.worktree));
      const canCreate = !this.tree.stale && this.daemonInfo?.features.create_environment === true && !worktree?.missing;
      const worktreeActions = worktree?.missing ? [] : [item('Open folder', undefined, 'openWorktree')];
      const environments = group?.runtimes.filter(runtime => runtime.worktree === target.worktree) || [];
      if (!this.tree.stale && this.tree.supportsWorktreeLifecycle && group?.repository.worktrees[0]?.path !== target.worktree) {
        const blocked = environments.length ? `Destroy the ${environments.length === 1 ? 'environment' : 'environments'} first.` : undefined;
        const remove = item('Remove worktree', blocked, blocked ? undefined : 'removeWorktree'); remove.contextValue = 'destructive'; worktreeActions.push(remove);
      }
      rows.push(item('Worktree actions', undefined, undefined, target, worktreeActions, true));
      if (!this.creating && environments.length > 1) {
        rows.push(item('Choose environment', undefined, undefined, target, environments.map(runtime => item(path.relative(target.worktree, runtime.project_dir) || '(root project)', runtime.observed, 'selectWorktree', { ...target, runtime, projectDir: runtime.project_dir }))));
      }
      rows.push(item('Runtime', this.tree.stale ? 'Daemon unavailable · cached state' : busy || runtime?.observed || 'Not started'));
      if (runtime?.last_error) { rows.push(item('Runtime error', runtime.last_error)); }
      if (project?.error) { rows.push(item('Configuration error', project.error)); }
      if (!this.tree.stale && runtime && !this.creating) {
        if (!busy && !project?.error && !running && !worktree?.missing) { rows.push(item('Start', undefined, 'startRuntime')); }
        if (runtime && !busy && runtime.observed !== 'stopped') { rows.push(item('Stop', undefined, 'stopRuntime')); }
        if (!busy && running && !worktree?.missing) { rows.push(item('Restart', undefined, 'restartRuntime')); }
      }
      if (!this.tree.stale && this.daemonInfo && ['create_environment', 'destroy_environment', 'worktree_lifecycle'].some(feature => this.daemonInfo?.features[feature] !== true)) {
        rows.push(item('Daemon update required', 'Update the installed Fugato CLI and restart its daemon for environment creation/destruction and worktree controls. Existing supported controls remain available.'));
      }
      if (!this.creating && !runtime && canCreate) { rows.push(item('Create environment', undefined, 'createEnvironment')); }
      if (runtime && !this.tree.stale) {
        const logs = [item('All services', undefined, 'showLogs')];
        logs[0].id += ':all';
        if (this.supportsServiceLogs && this.serviceState?.key === this.serviceKey()) {
          for (const service of this.serviceState.services) { const row = item(service.name, service.kind, 'showLogs'); row.id += `:${service.kind}:${service.name}`; row.service = service.name; row.serviceKind = service.kind; logs.push(row); }
        }
        rows.push(item('Log services', this.supportsServiceLogs ? undefined : 'Update daemon for individual service logs', undefined, target, logs));
      }
      if (running && !this.tree.stale) {
        const state = this.serviceState?.key === this.serviceKey() ? this.serviceState : undefined;
        const services: Row[] = [];
        if (!state) { services.push(item('Loading services…')); }
        else if (state.error) { services.push(item('Services unavailable', state.error)); }
        else {
          for (const service of state.services) {
            const ports = service.ports.map((port, index) => {
              const internal = !port.host_port;
              let address: string | undefined;
              try { address = internal ? undefined : serviceAddress(port, runtime!.id); } catch { /* Display the observed port without an unsafe action. */ }
              const row = item(internal ? 'Internal' : address || `${port.host_ip}:${port.host_port}`, internal ? `${port.container_port}/${port.protocol}` : `${port.container_port ? port.container_port + '/' : ''}${port.protocol}${port.name ? ' · ' + port.name : ''}`);
              row.id = `service-port:${runtime!.id}:${service.kind}:${service.name}:${port.name || ''}:${port.container_port}:${port.protocol}:${port.host_ip}:${port.host_port}:${index}`;
              row.tooltip = internal ? `Internal only: ${service.name}:${port.container_port}/${port.protocol}. Not published to the host.` : `${service.name}\n${port.host_ip}:${port.host_port}${port.container_port ? ' → ' + port.container_port : ''}/${port.protocol}`;
              if (address) {
                row.port = port; row.contextValue = 'servicePort'; row.iconPath = new vscode.ThemeIcon('plug');
                if (/^https?:\/\//.test(address)) { row.command = { command: 'fugato.openService', title: 'Open service', arguments: [row] }; }
              } else { row.iconPath = new vscode.ThemeIcon('circle-small-filled'); }
              return row;
            });
            if (!ports.length) {
              const empty = item('Ports', 'None declared'); empty.id = `service-ports-empty:${runtime!.id}:${service.kind}:${service.name}`; ports.push(empty);
            }
            const serviceRow = item(service.name, `${service.state}${service.health ? ' · ' + service.health : ''}`, undefined, target, ports);
            serviceRow.id = `service:${runtime!.id}:${service.kind}:${service.name}`;
            serviceRow.iconPath = new vscode.ThemeIcon(service.state === 'running' ? 'server' : 'circle-outline'); services.push(serviceRow);
          }
          if (!services.length) { services.push(item('No services reported')); }
          if (state.publishedOnly) { services.push(item('Published ports only', 'Update the Fugato daemon to include internal ports.')); }
        }
        rows.push(item('Services & ports', undefined, undefined, target, services));
      }
      if (!this.discovery) { if (this.creating) { rows.push(item('Projects', 'Discovering…')); } }
      else {
        if (this.creating) {
          rows.push(item('Create environment setup', 'Choose a project, review its configuration, then create. Workloads remain stopped.'));
          rows.push(item('Cancel', undefined, 'selectWorktree', { repository: target.repository, worktree: target.worktree }));
          rows.push(item('Choose directory…', undefined, 'chooseDirectory'));
          const candidates = this.discovery.projects.filter(candidate => !environments.some(runtime => runtime.project_dir === candidate.project_dir));
          const candidateRows = (configured: boolean) => candidates.filter(candidate => candidate.configured === configured).map(candidate => item(path.relative(target.worktree, candidate.project_dir) || '(root project)', candidate.error || candidate.kind, 'selectProject', { ...target, runtime: undefined, projectDir: candidate.project_dir }));
          if (candidateRows(true).length > 1) { rows.push(item('Configured projects', undefined, undefined, target, candidateRows(true))); }
          if (candidateRows(false).length) { rows.push(item('Advanced: manifest candidates', undefined, undefined, target, candidateRows(false), true)); }
          if (this.discovery.truncated) { rows.push(item('Discovery incomplete', 'Search limit reached; choose a directory explicitly.')); }
          if (project && !runtime && !project.error && !busy && this.daemonInfo?.features.create_environment === true) { rows.push(item('Create', 'Prepare and register; keep workloads stopped', 'createRuntime')); }
        }
        if (project) {
          const config = [item('Project path', project.project_dir), item('Configuration', project.config_file ? path.relative(project.project_dir, project.config_file) : 'Not configured'), item('Workload type', project.kind),
            item('Validate configuration', undefined, 'checkSetup'), item('Edit configuration', undefined, 'editConfiguration')];
          if (project.compose_files.length) { config.push(item('Compose files', project.compose_files.map(filename => path.relative(project.project_dir, filename)).join(', '))); }
          rows.push(item('Configuration & setup', undefined, undefined, target, config, true));
          if (!this.creating && project.checks.length) {
            const checkRows = project.checks.map(name => {
              const unavailable = this.checkUnavailable(project, name, runtime);
              const result = this.checkResults?.key === this.checkKey(target, project) ? this.checkResults.results.get(name) : undefined;
              const description = this.tree.stale ? 'Reconnect to run this check' : busy ? 'An environment action is running' : project.error || unavailable || result || 'Run check';
              const row = item(name, description, !this.tree.stale && !busy && !project.error && !unavailable ? 'runCheck' : undefined);
              row.id = `project-check:${target.repository}:${target.worktree}:${project.project_dir}:${name}`; row.checkName = name;
              return row;
            });
            rows.push(item('Project checks', undefined, undefined, target, checkRows));
          }
          if (!this.tree.stale) {
            if (!busy && !project.error && project.commands.some(command => command.mode !== 'service')) { actions.push(item('Run command', undefined, 'runCommand')); }
            if (runtime) {
              if (running) {
                if (project.endpoints.some(endpoint => ['http', 'https'].includes(endpoint.protocol))) { actions.push(item('Verify HTTP', undefined, 'verifyHTTP')); }
                if (project.endpoints.some(endpoint => endpoint.browser && endpoint.service)) { actions.push(item('Verify browser', undefined, 'verifyBrowser')); }
              }
              actions.push(item('Latest verification', undefined, 'showLatestVerification'), item('Open screenshot', undefined, 'openScreenshot'));
            }
          }
        }
      }
      if (!this.creating && !this.tree.stale && this.tree.supportsEnvironmentDestruction && !busy && (runtime || !target.projectDir && group?.runtimes.some(runtime => runtime.worktree === target.worktree))) {
        const destroy = item('Destroy environment', 'Keep data', 'destroyRuntime'); destroy.contextValue = 'destructive'; destroy.iconPath = new vscode.ThemeIcon('trash'); actions.push(destroy);
      }
      if (actions.length) { rows.push(item('More actions', undefined, undefined, target, actions, true)); }
    }
    this.details.rows = rows; this.details.refresh();
    this.sidebar.update(this.selected);
  }
  private async selectProject(row?: Row): Promise<void> {
    const target = row?.target;
    if (!target?.projectDir || !this.discovery?.projects.some(project => project.project_dir === target.projectDir) || (target.worktree !== this.selected?.worktree || target.repository !== this.selected?.repository)) { throw new Error('Project selection changed; select the worktree again'); }
    this.selectionGeneration++; this.pendingCreation = undefined;
    this.selected = { ...target, runtime: this.tree.rows.find(group => group.repository.repository === target.repository)?.runtimes.find(runtime => runtime.worktree === target.worktree && runtime.project_dir === target.projectDir) };
    this.sidebar.showLogs(undefined);
    await this.saveSelection(); this.renderDetails(); await this.loadServices(); this.renderDetails();
  }
  private async candidate(target: Target): Promise<Candidate | undefined> {
    const generation = this.generation; const selection = this.selectionGeneration;
    const result = await this.discover(target);
    if (generation !== this.generation || selection !== this.selectionGeneration) { throw new Error('Selection changed; choose the project again'); }
    const explicit = target.projectDir || target.runtime?.project_dir;
    if (explicit) {
      const project = result.projects.find(project => project.project_dir === explicit);
      if (!project) {
        if (this.creating) { return { project_dir: explicit, configured: false, kind: 'directory', compose_files: [], commands: [], checks: [], endpoints: [] }; }
        throw new Error('Project no longer exists; refresh discovery');
      }
      return project;
    }
    if (result.projects.length === 1) { return result.projects[0]; }
    if (!result.projects.length) { return undefined; }
    const choice = await vscode.window.showQuickPick(result.projects.map(project => ({ label: path.relative(target.worktree, project.project_dir) || '(root project)', description: project.error || project.kind, project })), { title: 'Select the worktree project' });
    if (generation !== this.generation || selection !== this.selectionGeneration) { throw new Error('Selection changed; choose the project again'); }
    return choice?.project;
  }
  private async detailTarget(row?: Row): Promise<Target | undefined> {
    const target = row?.target || this.selected || await this.select();
    const valid = revalidate(target, this.tree.rows);
    if (target && !valid) { throw new Error('Worktree selection changed'); }
    return valid;
  }
  private async checkSetup(row?: Row): Promise<void> {
    const target = await this.detailTarget(row); if (!target) { return; }
    const project = await this.candidate(target); if (!project) { return; }
    const result = await this.cli(['doctor', '--worktree', project.project_dir, '--json']);
    await this.reports.show(`setup-${project.id || 'candidate'}`, result);
    await this.loadDetails();
  }
  private async editConfiguration(row?: Row): Promise<void> {
    const target = await this.detailTarget(row); if (!target) { return; }
    const project = await this.candidate(target);
    const directory = project?.project_dir || target.projectDir || target.worktree;
    const canonical = await fs.realpath(directory);
    if (!within(await fs.realpath(target.worktree), canonical)) { throw new Error('Configuration directory is outside the worktree'); }
    const filename = path.join(canonical, '.fugato.yaml');
    try {
      const canonicalFile = await fs.realpath(filename);
      if (canonicalFile !== filename) { throw new Error('Configuration must not be a symbolic link'); }
      await vscode.window.showTextDocument(await vscode.workspace.openTextDocument(vscode.Uri.file(filename)));
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code !== 'ENOENT') { throw error; }
      const document = await vscode.workspace.openTextDocument(vscode.Uri.file(filename).with({ scheme: 'untitled' }));
      await vscode.window.showTextDocument(document);
      const edit = new vscode.WorkspaceEdit(); edit.insert(document.uri, new vscode.Position(0, 0), 'version: 1\nstart: {}\nendpoints: {}\ncommands: {}\nchecks: {}\n');
      await vscode.workspace.applyEdit(edit);
    }
  }
  private async ports(row?: Row): Promise<void> {
    const runtime = await this.chosen(row); if (!runtime) { return; }
    const client = this.connection(); const generation = this.generation;
    const ports = await client.ports(runtime.id); this.current(client, generation);
    await this.reports.show(`ports-${runtime.id}`, { runtime_id: runtime.id, project: runtime.project_dir, ports });
  }
  private checkKey(target: Target, project: Candidate): string {
    return JSON.stringify([this.generation, this.selectionGeneration, target.repository, target.worktree, project.project_dir, target.runtime?.id || project.id, project]);
  }
  private checkUnavailable(project: Candidate, name: string, runtime?: Runtime): string | undefined {
    const check = project.check_details?.find(check => check.name === name);
    if (!check && runtime?.observed !== 'running') { return 'Update the daemon to determine whether this check can run while stopped'; }
    return check?.requires_running && runtime?.observed !== 'running' ? 'Requires a running environment.' : undefined;
  }
  private recordCheck(key: string, name: string, result: string): void {
    const target = this.selected;
    const project = this.discovery?.projects.find(project => project.project_dir === target?.projectDir);
    if (!target || !project || this.checkKey(target, project) !== key) { return; }
    if (this.checkResults?.key !== key) { this.checkResults = { key, results: new Map() }; }
    this.checkResults.results.set(name, plain(result)); this.renderDetails();
  }
  private async showCheckPrerequisites(): Promise<void> {
    this.sidebar.openManagement(); await this.refresh(); await this.loadDetails();
  }
  private async named(kind: 'run' | 'check', row?: Row): Promise<void> {
    const target = await this.target(row); if (!target) { return; }
    const project = await this.candidate(target); if (!project) { return; }
    const selection = this.selectionGeneration; const generation = this.generation; const client = this.connection();
    const key = this.checkKey(target, project);
    if (kind === 'check' && row?.checkName) {
      if (!project.checks.includes(row.checkName)) { throw new Error('This check is no longer configured. Refresh the environment.'); }
      const unavailable = this.checkUnavailable(project, row.checkName, target.runtime);
      if (unavailable) { await this.showCheckPrerequisites(); return; }
    }
    const choices = kind === 'run' ? project.commands.filter(command => command.mode !== 'service').map(command => ({ label: command.name, description: command.mode })) : project.checks.filter(name => !this.checkUnavailable(project, name, target.runtime)).map(name => ({ label: name, description: 'check' }));
    if (!choices.length) {
      if (kind === 'check' && project.checks.length) { await this.showCheckPrerequisites(); return; }
      throw new Error(`No ${kind === 'run' ? 'runnable commands' : 'checks'} configured`);
    }
    const choice = kind === 'check' && row?.checkName ? choices.find(choice => choice.label === row.checkName) : await vscode.window.showQuickPick(choices, { title: kind === 'run' ? 'Run command' : 'Run check' });
    this.current(client, generation); if (!choice || selection !== this.selectionGeneration) { return; }
    if (choice.description === 'interactive') {
      const binary = await this.binary(); this.current(client, generation);
      if (selection !== this.selectionGeneration) { return; }
      const terminal = vscode.window.createTerminal({ name: `Fugato: ${choice.label}`, shellPath: binary, shellArgs: ['run', choice.label, '--worktree', project.project_dir], cwd: project.project_dir, env: { FUGATO_STATE_DIR: client.settings.stateDirectory, FUGATO_API_PORT: String(client.settings.apiPort) } });
      terminal.show(); return;
    }
    const resolved = await client.resolve(project.project_dir); this.current(client, generation);
    if (selection !== this.selectionGeneration) { return; }
    if (resolved.project_dir !== project.project_dir || resolved.repository !== target.repository || resolved.worktree !== target.worktree || project.id && resolved.id !== project.id) { throw new Error('Project identity changed'); }
    if (kind === 'check' && project.check_details?.find(check => check.name === choice.label)?.requires_running) {
      const current = resolved.registered ? await client.get(resolved.id) : undefined; this.current(client, generation);
      if (selection !== this.selectionGeneration) { return; }
      if (current && (current.repository !== target.repository || current.worktree !== target.worktree || current.project_dir !== project.project_dir)) { throw new Error('Environment identity changed. Refresh the environment.'); }
      const unavailable = this.checkUnavailable(project, choice.label, current);
      if (unavailable) { await this.showCheckPrerequisites(); return; }
    }
    try {
      const result = await this.operation(resolved, kind === 'run' ? 'Running command…' : 'Checking…', signal => client.named(project, kind, choice.label, signal));
      this.current(client, generation);
      if (kind === 'run') { await this.reports.show(`${kind}-${resolved.id}`, result); return; }
      const check = object(result);
      if (check.name !== choice.label || typeof check.ok !== 'boolean') { throw new Error('Invalid check result. Run the check again to verify its outcome.'); }
      this.recordCheck(key, choice.label, check.ok ? 'Passed' : `Failed${typeof check.status === 'number' && check.status ? `: HTTP ${check.status}` : typeof check.exit_code === 'number' && check.exit_code ? `: exit ${check.exit_code}` : ''}`);
    } catch (error) {
      if (kind === 'check') {
        const message = error instanceof Error ? error.message : String(error);
        this.recordCheck(key, choice.label, `Failed: ${message}`);
        throw new Error(`${choice.label}: ${message}`);
      }
      throw error;
    }
  }
  private async startDaemon(): Promise<void> {
    const client = this.connection(); const generation = this.generation;
    let running: Info | undefined;
    try { running = await client.info(); }
    catch (error) {
      if (error instanceof APIError || /update required|Malformed/.test(error instanceof Error ? error.message : String(error))) { throw error; }
    }
    this.current(client, generation);
    if (running) {
      for (const feature of ['create_environment', 'destroy_environment', 'worktree_lifecycle'] as const) { requireFeature(running, feature); }
      this.paused = false; await this.refresh(); await this.loadDetails();
      void vscode.window.showInformationMessage('Fugato daemon is already running. Start Daemon does not restart it.');
      return;
    }
    const roots = [...await Promise.all((vscode.workspace.workspaceFolders || []).map(folder => fs.realpath(folder.uri.fsPath))), ...this.tree.rows.flatMap(group => group.repository.worktrees.map(worktree => worktree.path))];
    const binary = await executable(this.config!.executablePath, roots);
    this.current(client, generation);
    await vscode.window.withProgress({ location: vscode.ProgressLocation.Notification, title: 'Starting Fugato daemon', cancellable: true }, async (_, token) => {
      this.current(client, generation);
      await new Promise<void>((resolve, reject) => {
        const child = spawn(binary, ['ls'], { cwd: os.tmpdir(), shell: false, env: { ...process.env, FUGATO_STATE_DIR: client.settings.stateDirectory, FUGATO_API_PORT: String(client.settings.apiPort) }, stdio: ['ignore','pipe','pipe'] });
        this.children.add(child); let size = 0;
        const consume = (chunk: Buffer) => { size += chunk.length; if (size > 64 * 1024) { child.kill(); } };
        child.stdout?.on('data', consume); child.stderr?.on('data', consume);
        const cancel = token.onCancellationRequested(() => child.kill());
        const timer = setTimeout(() => child.kill('SIGKILL'), 30000);
        child.once('error', reject);
        child.once('close', code => { clearTimeout(timer); cancel.dispose(); this.children.delete(child); code === 0 ? resolve() : reject(new Error(`Daemon startup failed (${code ?? 'cancelled'}). Run fugato doctor in your terminal.`)); });
      });
    });
    this.current(client, generation);
    const started = await client.info();
    this.current(client, generation);
    for (const feature of ['create_environment', 'destroy_environment', 'worktree_lifecycle'] as const) {
      if (started.features[feature] !== true) { throw new Error('The installed Fugato CLI started an older daemon. Update the CLI selected by fugato.executablePath (or your host PATH), restart its daemon, then Refresh. The VSIX does not include the CLI'); }
    }
    this.paused = false; await this.refresh(); await this.loadDetails();
  }
  private async setup(): Promise<void> {
    const text = [
      'Fugato setup', '',
      '1. Install the current Fugato CLI outside the workspace and set the user setting fugato.executablePath if needed. The VSIX does not include the CLI. Restart an existing daemon after upgrading the CLI, then Refresh. Start Daemon starts one only when absent.',
      '2. Configure .fugato.yaml from existing Compose files, package scripts, and development documentation. Version 1 supports Compose, native commands, and mixed projects.',
      '3. Select a worktree to open its management page. Selection never starts anything. An empty worktree offers Create environment; choose a project when several configurations exist, review Configuration, then Create. Workloads remain stopped until Start.',
      '4. Use Validate configuration or Edit configuration. YAML completion requires an editor YAML extension.',
      '5. Start the environment to see service addresses. Click a published HTTP(S) address to open it. Browser endpoints are optional.',
      '6. Run named tasks or individual Project checks and inspect Logs. Checks explain their prerequisites and show results inline. Stop retains application data.', '',
      'Host commands run with your permissions and rely on configured port/data overrides for isolation. Browser verification requires a configured browser service.',
      'GUI editors may not inherit terminal FUGATO_STATE_DIR or FUGATO_API_PORT; set user-level settings to match the CLI. Only local trusted macOS and Linux workspaces are supported.', '',
      'See the packaged README and docs/guides/editor-extension.md for installation and troubleshooting.'
    ].join('\n');
    await vscode.window.showTextDocument(await vscode.workspace.openTextDocument({ content: text, language: 'plaintext' }));
  }
  private error(error: unknown, command?: string): void {
    if (this.disposed) { return; }
    const message = plain(error instanceof Error ? error.message : String(error));
    // Notifications remain open until dismissed; their lifetime must not own
    // command completion or keep the management page's action pending.
    const setup = ['checkSetup', 'editConfiguration', 'startDaemon'].includes(command || '') || /Daemon update required|Fugato CLI|executablePath|Trust this workspace|Open a local Git repository/.test(message);
    void Promise.resolve(vscode.window.showErrorMessage(`Fugato: ${message}`, ...(setup ? ['Setup Instructions'] : [])))
      .then(action => action === 'Setup Instructions' && !this.disposed ? this.setup() : undefined)
      .catch(() => { /* Reporting must not create an unhandled rejection. */ });
  }
  dispose(): void { for (const watcher of this.selectedWatchers) { watcher.dispose(); } this.disposed = true; this.generation++; this.selectionGeneration++; if (this.discoveryTimer) { clearTimeout(this.discoveryTimer); } if (this.timer) { clearTimeout(this.timer); } this.client?.dispose(); for (const c of this.controllers) { c.abort(); } for (const c of this.children) { c.kill('SIGKILL'); } }
}
export function activate(context: vscode.ExtensionContext): void { context.subscriptions.push(new Extension(context)); }
export function deactivate(): void { /* All owned resources are disposed through ExtensionContext. */ }
