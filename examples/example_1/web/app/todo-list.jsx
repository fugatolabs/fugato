'use client';

import { useState } from 'react';

export default function TodoList({ initialTodos }) {
  const [todos, setTodos] = useState(initialTodos);
  const [title, setTitle] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const remaining = todos.filter((todo) => !todo.completed).length;

  async function mutate(path, method, body, apply) {
    setBusy(true);
    setError('');
    try {
      const response = await fetch(path, {
        method,
        ...(body === undefined ? {} : {
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(body),
        }),
      });
      const data = response.status === 204 ? null : await response.json();
      if (!response.ok) throw new Error(data?.message || `Request failed (${response.status})`);
      apply(data);
    } catch (cause) {
      setError(cause.message);
    } finally {
      setBusy(false);
    }
  }

  function add(event) {
    event.preventDefault();
    if (!title.trim() || busy) return;
    mutate('/api/todos', 'POST', { title }, (todo) => {
      setTodos((items) => [...items, todo]);
      setTitle('');
    });
  }

  return <section className="card" aria-label="Todo list" aria-busy={busy}>
    <form onSubmit={add}>
      <label htmlFor="title">What needs doing?</label>
      <div className="add-row">
        <input id="title" value={title} onChange={(event) => setTitle(event.target.value)}
          placeholder="e.g. Try Fugato in a second worktree" maxLength={200} required disabled={busy} />
        <button type="submit" disabled={busy || !title.trim()}>Add task</button>
      </div>
    </form>
    {error && <p className="error" role="alert">{error}</p>}
    <p className="count" aria-live="polite">{remaining} remaining · {todos.length - remaining} completed</p>
    {todos.length === 0 ? <p className="empty">Your list is clear. Add your first task above.</p> :
      <ul>{todos.map((todo) => <li key={todo.id}>
        <label className={todo.completed ? 'completed' : ''}>
          <input type="checkbox" checked={todo.completed} disabled={busy}
            onChange={() => mutate(`/api/todos/${todo.id}`, 'PATCH', { completed: !todo.completed },
              (updated) => setTodos((items) => items.map((item) => item.id === updated.id ? updated : item)))} />
          <span>{todo.title}</span>
        </label>
        <button className="delete" type="button" disabled={busy} aria-label={`Delete ${todo.title}`}
          onClick={() => mutate(`/api/todos/${todo.id}`, 'DELETE', undefined,
            () => setTodos((items) => items.filter((item) => item.id !== todo.id)))}>Delete</button>
      </li>)}</ul>}
  </section>;
}
