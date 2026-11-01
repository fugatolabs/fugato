import { Repository, Runtime, Worktree } from './contracts';
export interface Group { repository: Repository; runtimes: Runtime[]; candidateRelative?: string }
export interface Target { repository: string; worktree: string; runtime?: Runtime; projectDir?: string }
export function groups(repositories: Repository[], runtimes: Runtime[]): Group[] {
  return [...new Map(repositories.map(repository => [repository.repository, repository])).values()].map(repository => {
    const matching = runtimes.filter(runtime => runtime.repository === repository.repository);
    const worktrees: Worktree[] = [...repository.worktrees];
    for (const runtime of matching) {
      if (!worktrees.some(w => w.path === runtime.worktree)) { worktrees.push({ path: runtime.worktree, missing: true }); }
    }
    return { repository: { ...repository, worktrees }, runtimes: matching };
  });
}
export function revalidate(target: Target | undefined, rows: Group[]): Target | undefined {
  if (!target) { return undefined; }
  const group = rows.find(g => g.repository.repository === target.repository);
  if (!group?.repository.worktrees.some(w => w.path === target.worktree)) { return undefined; }
  if (!target.runtime) {
    const runtime = target.projectDir ? group.runtimes.find(runtime => runtime.worktree === target.worktree && runtime.project_dir === target.projectDir) : undefined;
    return { repository: target.repository, worktree: target.worktree, projectDir: target.projectDir, runtime };
  }
  const runtime = group.runtimes.find(r => r.id === target.runtime?.id && r.worktree === target.worktree);
  return runtime ? { repository: target.repository, worktree: target.worktree, projectDir: runtime.project_dir, runtime } : undefined;
}
