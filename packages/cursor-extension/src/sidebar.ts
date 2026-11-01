import * as vscode from 'vscode';
import { createHash, randomBytes } from 'node:crypto';
import { Row, Tree, Details, selectionIdentity } from './views';
import { Target } from './model';

export interface SidebarRow {
  id: string; label: string; description: string; tooltip: string; icon?: string;
  context?: string; action?: string; copyAction?: string; destroyAction?: string; collapsed: boolean; selected: boolean; children?: SidebarRow[];
}
interface SidebarModel { repositories: SidebarRow[]; details: SidebarRow[]; connection?: string; diagnostic?: string; logs?: string; stale: boolean; refresh: string; setup: string; startDaemon: string; createWorktree: string }
const commands = new Set(['refresh', 'selectWorktree', 'selectProject', 'createEnvironment', 'chooseDirectory', 'createRuntime', 'createWorktree', 'removeWorktree', 'openWorktree', 'restartRuntime', 'startRuntime', 'stopRuntime', 'destroyRuntime', 'openPreview', 'copyPreviewLink', 'showLogs', 'checkSetup', 'editConfiguration', 'showPorts', 'runCommand', 'runCheck', 'verifyHTTP', 'verifyBrowser', 'showLatestVerification', 'openScreenshot', 'openService', 'copyServiceAddress', 'startDaemon', 'setup'].map(name => `fugato.${name}`));

export class SidebarState {
  private value?: string;
  constructor(private change: () => void) {}
  get message(): string | undefined { return this.value; }
  set message(value: string | undefined) { if (this.value !== value) { this.value = value; this.change(); } }
}

// The page receives opaque action keys, never command arguments or credentials.
export class Sidebar implements vscode.WebviewViewProvider, vscode.Disposable {
  readonly connection = new SidebarState(() => this.update());
  readonly selection = new SidebarState(() => this.update());
  private visibility = new vscode.EventEmitter<void>();
  readonly onDidChangeVisibility = this.visibility.event;
  private view?: vscode.WebviewView;
  private panel?: vscode.WebviewPanel;
  private panelReady = false;
  private pendingTab?: 'logs';
  private logText?: string;
  private subscriptions: vscode.Disposable[] = [];
  private actions = new Map<string, { command: string; row?: Row }>();
  private snapshot = ''; private model?: SidebarModel; private selected?: Target; private ready = false;
  get visible(): boolean { return !!(this.view?.visible || this.panel?.visible); }
  constructor(private extensionUri: vscode.Uri, readonly tree: Tree, readonly details: Details) {}
  openManagement(tab?: 'logs'): void {
    if (tab) { this.pendingTab = tab; }
    if (this.panel) { this.panel.reveal(undefined, true); this.sendTab(); return; }
    const panel = vscode.window.createWebviewPanel('fugato.environment', 'Fugato', vscode.ViewColumn.Active, {
      enableScripts: true, retainContextWhenHidden: true, localResourceRoots: [vscode.Uri.joinPath(this.extensionUri, 'resources')]
    });
    this.panel = panel;
    panel.webview.onDidReceiveMessage(message => { void this.receive(message, true); }, undefined, this.subscriptions);
    panel.onDidDispose(() => { if (this.panel === panel) { this.panel = undefined; this.panelReady = false; this.pendingTab = undefined; this.visibility.fire(); } }, undefined, this.subscriptions);
    panel.onDidChangeViewState(() => this.visibility.fire(), undefined, this.subscriptions);
    panel.webview.html = sidebarHTML(panel.webview, this.extensionUri, true);
    this.visibility.fire();
  }
  private sendTab(): void {
    if (this.panelReady && this.pendingTab) {
      void this.panel?.webview.postMessage({ type: 'selectTab', tab: this.pendingTab });
      this.pendingTab = undefined;
    }
  }
  showLogs(text: string | undefined): void { this.logText = text; this.update(); }
  resolveWebviewView(view: vscode.WebviewView): void {
    this.view = view; this.ready = false;
    view.description = 'Worktrees';
    view.webview.options = { enableScripts: true, localResourceRoots: [vscode.Uri.joinPath(this.extensionUri, 'resources')] };
    this.subscriptions.push(view.webview.onDidReceiveMessage(message => { void this.receive(message); }),
      view.onDidChangeVisibility(() => { this.visibility.fire(); }),
      view.onDidDispose(() => { if (this.view === view) { this.view = undefined; this.ready = false; this.visibility.fire(); } }));
    view.webview.html = sidebarHTML(view.webview, this.extensionUri);
    this.update(); this.visibility.fire();
  }
  update(...selection: [] | [Target | undefined]): void {
    if (selection.length) { this.selected = selection[0]; }
    const selected = this.selected;
    const actions = new Map<string, { command: string; row?: Row }>();
    const action = (command: string, row?: Row): string => {
      if (!commands.has(command)) { return ''; }
      const key = createHash('sha256').update(`${command}:${row?.id || 'global'}`).digest('hex');
      actions.set(key, { command, row }); return key;
    };
    const serialize = (row: Row): SidebarRow => ({
      id: row.id || String(row.label), label: typeof row.label === 'string' ? row.label : row.label?.label || '',
      description: typeof row.description === 'string' ? row.description : '', tooltip: typeof row.tooltip === 'string' ? row.tooltip : '',
      icon: row.iconPath instanceof vscode.ThemeIcon ? row.iconPath.id : undefined, context: row.contextValue,
      action: row.command ? action(row.command.command, row.command.arguments?.[0]) : undefined,
      copyAction: row.contextValue === 'servicePort' ? action('fugato.copyServiceAddress', row) : undefined,
      destroyAction: row.destroyCommand ? action(row.destroyCommand.command, row) : undefined,
      collapsed: row.collapsibleState === vscode.TreeItemCollapsibleState.Collapsed,
      selected: !!row.target && (row.contextValue === 'worktree' ? row.target.repository === selected?.repository && row.target.worktree === selected?.worktree : JSON.stringify(selectionIdentity(row.target)) === JSON.stringify(selectionIdentity(selected))),
      children: row.children?.map(serialize)
    });
    const model: SidebarModel = { repositories: this.tree.getChildren().map(serialize), details: this.details.rows.map(serialize),
      connection: this.connection.message, diagnostic: this.selection.message, logs: this.logText, stale: this.tree.stale,
      refresh: action('fugato.refresh'), setup: action('fugato.setup'), startDaemon: action('fugato.startDaemon'), createWorktree: this.tree.supportsWorktreeLifecycle && !this.tree.stale ? action('fugato.createWorktree') : '' };
    // Refresh retained targets even when the visible model has not changed.
    this.actions = actions;
    const snapshot = createHash('sha256').update(JSON.stringify(model)).digest('hex');
    this.model = model;
    if (snapshot === this.snapshot) { return; }
    this.snapshot = snapshot;
    this.send();
  }
  private send(): void {
    const state = { type: 'state', revision: this.snapshot, model: this.model };
    if (this.ready && this.model) { void this.view?.webview.postMessage(state); }
    if (this.panelReady && this.model) { void this.panel?.webview.postMessage(state); }
  }
  private async receive(message: unknown, management = false): Promise<void> {
    const webview = management ? this.panel?.webview : this.view?.webview;
    if (!message || typeof message !== 'object') { return; }
    const input = message as Record<string, unknown>;
    if (input.type === 'ready') { if (management) { this.panelReady = true; } else { this.ready = true; } this.send(); if (management) { this.sendTab(); } return; }
    if (input.type !== 'action' || typeof input.action !== 'string' || input.action.length !== 64) { return; }
    if (input.revision !== this.snapshot) { this.send(); void webview?.postMessage({ type: 'actionFinished', action: input.action }); return; }
    const action = this.actions.get(input.action);
    if (!action || !vscode.workspace.isTrusted) { void webview?.postMessage({ type: 'actionFinished', action: input.action }); return; }
    try { await vscode.commands.executeCommand(action.command, action.row); }
    catch { void webview?.postMessage({ type: 'actionError', message: 'The action could not complete. Refresh and try again.' }); }
    finally { void webview?.postMessage({ type: 'actionFinished', action: input.action }); }
  }
  focusSelection(): void { this.view?.show(true); void this.view?.webview.postMessage({ type: 'focusSelection' }); }
  dispose(): void { this.panel?.dispose(); for (const subscription of this.subscriptions) { subscription.dispose(); } this.visibility.dispose(); this.actions.clear(); this.view = undefined; }
}

export function sidebarHTML(webview: vscode.Webview, extensionUri: vscode.Uri, management = false): string {
  const nonce = randomBytes(24).toString('hex');
  const style = webview.asWebviewUri(vscode.Uri.joinPath(extensionUri, 'resources', 'sidebar.css'));
  const script = webview.asWebviewUri(vscode.Uri.joinPath(extensionUri, 'resources', 'sidebar.js'));
  return `<!DOCTYPE html><html lang="en"><head><meta charset="UTF-8"><meta name="viewport" content="width=device-width,initial-scale=1.0">
<meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src ${webview.cspSource}; script-src 'nonce-${nonce}'; img-src ${webview.cspSource}; font-src ${webview.cspSource};">
<link rel="stylesheet" href="${style}"><title>Fugato</title></head><body data-surface="${management ? 'management' : 'navigation'}">
<main id="fugato"><section class="environments" aria-labelledby="environment-heading"><div class="section-heading"><h2 id="environment-heading">Worktrees</h2><button id="create-worktree" type="button" class="icon-button" aria-label="Create worktree" title="Create worktree" disabled>＋</button><button id="refresh" type="button" class="icon-button" aria-label="Refresh worktrees" title="Refresh worktrees" disabled>↻</button></div><div id="connection" role="status"></div><div id="repositories"></div></section>
<section id="selection" class="selection" aria-label="Manage worktree" tabindex="-1"><div id="details"><p class="muted">Select a worktree in the Fugato sidebar.</p></div></section>
<footer><span id="daemon-state" role="status">Connecting to local daemon…</span><span class="muted">Automatic updates</span></footer></main>
<script nonce="${nonce}" src="${script}"></script></body></html>`;
}
