import * as vscode from 'vscode';
import * as path from 'node:path';
import { Group, Target } from './model';
import { PortBinding, within } from './contracts';
import { plain } from './security';

export class Row extends vscode.TreeItem {
  checkName?: string;
  port?: PortBinding;
  service?: string;
  serviceKind?: 'compose' | 'native';
  destroyCommand?: vscode.Command;
  constructor(label: string, readonly target?: Target, readonly group?: Group, readonly children?: Row[], collapsed = false) {
    super(plain(label), children ? collapsed ? vscode.TreeItemCollapsibleState.Collapsed : vscode.TreeItemCollapsibleState.Expanded : vscode.TreeItemCollapsibleState.None);
  }
}
export function selectionIdentity(target: Target | undefined): unknown {
  return target && [target.repository, target.worktree, target.projectDir, target.runtime?.id];
}
function presentation(row: Row): unknown {
  const argument = row.command?.arguments?.[0] as Row | undefined;
  return [row.id, row.label, row.description, row.tooltip, row.iconPath, row.contextValue, row.collapsibleState,
    row.command?.command, row.destroyCommand?.command, selectionIdentity(argument?.target), argument?.port, row.children?.map(presentation)];
}
export class Tree implements vscode.TreeDataProvider<Row>, vscode.Disposable {
  private emitter = new vscode.EventEmitter<Row | undefined>();
  readonly onDidChangeTreeData = this.emitter.event;
  private snapshot = '';
  rows: Group[] = []; stale = false; busy = new Map<string, string>();
  supportsWorktreeLifecycle = true; supportsEnvironmentDestruction = true;
  workspacePaths: string[] = (vscode.workspace.workspaceFolders || []).map(folder => folder.uri.fsPath);
  getTreeItem(row: Row): vscode.TreeItem { return row; }
  isCurrent(worktree: string): boolean { return this.workspacePaths.some(folder => within(worktree, folder)); }
  getChildren(row?: Row): Row[] {
    if (row) { return row.children || []; }
    return this.rows.map(group => {
      const worktrees = [...group.repository.worktrees].sort((a, b) => Number(this.isCurrent(b.path)) - Number(this.isCurrent(a.path)));
      const available: Row[] = [], missing: Row[] = [];
      for (const worktree of worktrees) {
        const target = { repository: group.repository.repository, worktree: worktree.path };
        const runtimes = group.runtimes.filter(r => r.worktree === worktree.path);
        const projects = runtimes.map(runtime => {
          const project = new Row(path.basename(runtime.project_dir), { ...target, runtime, projectDir: runtime.project_dir });
          project.id = `runtime:${runtime.id}`; project.contextValue = runtime.observed === 'running' && !this.stale ? runtime.preview_url ? 'runtimeRunningPreview' : 'runtimeRunning' : 'runtime';
          project.description = `${runtime.id} · ${this.busy.get(runtime.id) || runtime.observed}${this.stale ? ' · cached' : ''}`;
          project.tooltip = plain(`${path.relative(worktree.path, runtime.project_dir) || '(root project)'}\nEnvironment ${runtime.id}\n${runtime.project_dir}\nDesired: ${runtime.desired}; observed: ${runtime.observed}\nHEAD: ${runtime.commit || 'unavailable'}\n${runtime.metadata_error || ''}\n${runtime.last_error || ''}`);
          project.iconPath = new vscode.ThemeIcon(runtime.last_error ? 'warning' : runtime.observed === 'running' ? 'pass' : 'circle-outline');
          project.command = { command: 'fugato.selectWorktree', title: 'Select environment', arguments: [project] };
          return project;
        });
        const item = new Row(worktree.branch || '(detached HEAD)', target, group, projects.length > 1 ? projects : undefined);
        item.id = `worktree:${group.repository.repository}:${worktree.path}`; item.contextValue = 'worktree';
        item.description = worktree.missing ? 'Git checkout unavailable' : `${this.isCurrent(worktree.path) ? 'Current folder · ' : ''}${runtimes.length === 1 ? this.busy.get(runtimes[0].id) || runtimes[0].observed : runtimes.length ? `${runtimes.length} environments` : 'No environment'} · ${worktree.path}`;
        if (this.stale) { item.description = `${item.description ? item.description + ' · ' : ''}cached`; }
        item.tooltip = worktree.path; item.iconPath = new vscode.ThemeIcon(worktree.missing || runtimes.some(runtime => runtime.last_error) ? 'warning' : runtimes.some(runtime => runtime.observed === 'running') ? 'pass' : 'git-branch');
        item.command = { command: 'fugato.selectWorktree', title: 'Select worktree', arguments: [item] };
        if (worktree.missing && runtimes.length && !this.stale && this.supportsEnvironmentDestruction && !runtimes.some(runtime => this.busy.has(runtime.id))) {
          item.destroyCommand = { command: 'fugato.destroyRuntime', title: 'Destroy registered environment…', arguments: [item] };
        }
        (worktree.missing ? missing : available).push(item);
      }
      if (missing.length) {
        const unavailable = new Row('Unavailable worktrees', undefined, undefined, missing, true);
        unavailable.id = `missing:${group.repository.repository}`; unavailable.description = String(missing.length);
        unavailable.iconPath = new vscode.ThemeIcon('warning'); available.push(unavailable);
      }
      const item = new Row(path.basename(path.dirname(group.repository.repository)), undefined, group, available);
      item.id = `repository:${group.repository.repository}`; item.contextValue = 'repository'; item.tooltip = group.repository.repository;
      item.description = group.repository.truncated ? 'Worktree list truncated' : 'All worktrees';
      item.iconPath = new vscode.ThemeIcon('repo'); return item;
    });
  }
  refresh(): void {
    const next = JSON.stringify(this.getChildren().map(presentation));
    if (next === this.snapshot) { return; }
    this.snapshot = next; this.emitter.fire(undefined);
  }
  dispose(): void { this.emitter.dispose(); }
}
export class Details implements vscode.TreeDataProvider<Row>, vscode.Disposable {
  private emitter = new vscode.EventEmitter<Row | undefined>();
  readonly onDidChangeTreeData = this.emitter.event;
  private snapshot = '';
  rows: Row[] = [];
  getTreeItem(row: Row): vscode.TreeItem { return row; }
  getChildren(row?: Row): Row[] { return row ? row.children || [] : this.rows; }
  refresh(): void {
    const next = JSON.stringify(this.rows.map(presentation));
    if (next === this.snapshot) { return; }
    this.snapshot = next; this.emitter.fire(undefined);
  }
  dispose(): void { this.emitter.dispose(); }
}
