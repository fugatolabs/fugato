const form = document.querySelector('#todo-form');
const input = document.querySelector('#title');
const list = document.querySelector('#todos');
const empty = document.querySelector('#empty');
const count = document.querySelector('#count');
const errorBox = document.querySelector('#error');

async function api(url, options = {}) {
  const response = await fetch(url, {
    ...options,
    headers: { 'content-type': 'application/json', ...options.headers },
  });
  const data = await response.json();
  if (!response.ok) throw new Error(data.error || `Request failed (${response.status})`);
  return data;
}

function showError(error) {
  errorBox.textContent = error.message;
  errorBox.hidden = false;
}

async function refresh() {
  const todos = await api('/api/todos');
  list.replaceChildren();
  empty.hidden = todos.length > 0;
  count.textContent = `${todos.filter(todo => !todo.completed).length} open`;
  for (const todo of todos) {
    const item = document.createElement('li');
    const checkbox = document.createElement('input');
    checkbox.type = 'checkbox';
    checkbox.checked = todo.completed;
    checkbox.setAttribute('aria-label', `Complete ${todo.title}`);
    checkbox.addEventListener('change', async () => {
      try {
        await api(`/api/todos/${todo.id}`, { method: 'PATCH', body: JSON.stringify({ completed: checkbox.checked }) });
        await refresh();
      } catch (error) { showError(error); }
    });
    const title = document.createElement('span');
    title.textContent = todo.title;
    if (todo.completed) title.className = 'completed';
    const remove = document.createElement('button');
    remove.type = 'button';
    remove.className = 'remove';
    remove.textContent = 'Delete';
    remove.setAttribute('aria-label', `Delete ${todo.title}`);
    remove.addEventListener('click', async () => {
      try {
        await api(`/api/todos/${todo.id}`, { method: 'DELETE' });
        await refresh();
      } catch (error) { showError(error); }
    });
    item.append(checkbox, title, remove);
    list.append(item);
  }
}

form.addEventListener('submit', async event => {
  event.preventDefault();
  errorBox.hidden = true;
  try {
    await api('/api/todos', { method: 'POST', body: JSON.stringify({ title: input.value }) });
    input.value = '';
    await refresh();
    input.focus();
  } catch (error) { showError(error); }
});

refresh().catch(showError);

// The test stack deliberately uses a tiny polling reload loop so mounted source
// changes are visible in both the shared Engine and Lima guest without tooling.
let version;
setInterval(async () => {
  try {
    const current = (await api('/dev/version')).version;
    if (version && version !== current) location.reload();
    version = current;
  } catch { /* A container restart briefly interrupts the poll. */ }
}, 1000);
