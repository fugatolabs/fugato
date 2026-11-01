const http = require('node:http');
const fs = require('node:fs/promises');
const path = require('node:path');
const { Pool } = require('pg');

const pool = new Pool({ connectionString: process.env.DATABASE_URL });
const port = Number(process.env.PORT || 3000);
const publicDir = path.join(__dirname, 'public');
const startedAt = Date.now();

async function sendJson(response, status, value) {
  response.writeHead(status, { 'content-type': 'application/json; charset=utf-8' });
  response.end(JSON.stringify(value));
}

async function readJson(request) {
  let body = '';
  for await (const chunk of request) {
    body += chunk;
    if (body.length > 16_384) {
      const error = new Error('Request body is too large');
      error.status = 413;
      throw error;
    }
  }
  try {
    return JSON.parse(body || '{}');
  } catch {
    const error = new Error('Invalid JSON');
    error.status = 400;
    throw error;
  }
}

async function handle(request, response) {
  const url = new URL(request.url, `http://${request.headers.host || 'localhost'}`);
  try {
    if (url.pathname === '/health' && request.method === 'GET') {
      await pool.query('SELECT 1');
      return sendJson(response, 200, { ok: true });
    }
    if (url.pathname === '/dev/version' && request.method === 'GET') {
      const files = ['index.html', 'app.js', 'style.css'];
      const stats = await Promise.all(files.map(file => fs.stat(path.join(publicDir, file))));
      return sendJson(response, 200, { version: [startedAt, ...stats.map(stat => stat.mtimeMs)].join(':') });
    }
    if (url.pathname === '/api/todos' && request.method === 'GET') {
      const result = await pool.query('SELECT id, title, completed, created_at FROM todos ORDER BY id DESC');
      return sendJson(response, 200, result.rows);
    }
    if (url.pathname === '/api/todos' && request.method === 'POST') {
      const title = String((await readJson(request)).title || '').trim();
      if (!title || title.length > 200) return sendJson(response, 400, { error: 'Enter a title (up to 200 characters).' });
      const result = await pool.query('INSERT INTO todos (title) VALUES ($1) RETURNING id, title, completed, created_at', [title]);
      return sendJson(response, 201, result.rows[0]);
    }
    const match = /^\/api\/todos\/(\d+)$/.exec(url.pathname);
    if (match && request.method === 'PATCH') {
      const { completed } = await readJson(request);
      if (typeof completed !== 'boolean') return sendJson(response, 400, { error: 'completed must be a boolean.' });
      const result = await pool.query('UPDATE todos SET completed = $1 WHERE id = $2 RETURNING id, title, completed, created_at', [completed, match[1]]);
      return result.rowCount ? sendJson(response, 200, result.rows[0]) : sendJson(response, 404, { error: 'To-do not found.' });
    }
    if (match && request.method === 'DELETE') {
      const result = await pool.query('DELETE FROM todos WHERE id = $1', [match[1]]);
      return result.rowCount ? sendJson(response, 200, { ok: true }) : sendJson(response, 404, { error: 'To-do not found.' });
    }
    if (request.method === 'GET' && (url.pathname === '/' || url.pathname === '/app.js' || url.pathname === '/style.css')) {
      const filename = url.pathname === '/' ? 'index.html' : url.pathname.slice(1);
      const type = filename.endsWith('.html') ? 'text/html' : filename.endsWith('.js') ? 'text/javascript' : 'text/css';
      const content = await fs.readFile(path.join(publicDir, filename));
      response.writeHead(200, { 'content-type': `${type}; charset=utf-8` });
      return response.end(content);
    }
    return sendJson(response, 404, { error: 'Not found.' });
  } catch (error) {
    console.error(error);
    return sendJson(response, error.status || 500, { error: error.status ? error.message : 'Server error.' });
  }
}

async function start() {
  await pool.query(`CREATE TABLE IF NOT EXISTS todos (
    id BIGSERIAL PRIMARY KEY,
    title TEXT NOT NULL,
    completed BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
  )`);
  const server = http.createServer(handle);
  server.listen(port, '0.0.0.0', () => console.log(`To-do app listening on ${port}`));
  for (const signal of ['SIGINT', 'SIGTERM']) {
    process.on(signal, () => server.close(() => pool.end().then(() => process.exit(0))));
  }
}

start().catch(error => {
  console.error(error);
  process.exit(1);
});
