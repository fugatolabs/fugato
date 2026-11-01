import TodoList from './todo-list';

export const dynamic = 'force-dynamic';

export default async function Home() {
  const response = await fetch(`${process.env.API_URL}/todos`, { cache: 'no-store' });
  if (!response.ok) throw new Error(`Nest API returned ${response.status}`);
  const todos = await response.json();
  const helloResponse = await fetch(`${process.env.API_URL}/hello`, { cache: 'no-store' });
  if (!helloResponse.ok) throw new Error(`Nest greeting returned ${helloResponse.status}`);
  const hello = await helloResponse.json();

  return <main>
    <p className="eyebrow">Fugato · Example 1</p>
    <h1>A little less to do</h1>
    <p className="intro">Add a task, check it off, and pick up where you left off.</p>
    <p><strong>Backend says:</strong> {hello.message}</p>
    <TodoList initialTodos={todos} />
    <footer>Next.js → NestJS → PostgreSQL · Saved in this worktree’s database.</footer>
  </main>;
}
