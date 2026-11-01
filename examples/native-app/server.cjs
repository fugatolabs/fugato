const http = require('node:http');
const fs = require('node:fs');
const path = require('node:path');

function page(name, runtime) {
  return `<!doctype html><title>Fugato ${name}</title><h1>${name}</h1><p>Runtime: ${runtime}</p>`;
}

function start() {
  const runtime = process.env.FUGATO_RUNTIME_ID || 'manual';
  const data = process.env.FUGATO_DATA_DIR;
  if (data) {
    fs.mkdirSync(data, { recursive: true });
    fs.writeFileSync(path.join(data, 'runtime.txt'), runtime);
  }
  const servers = [
    ['Website', process.env.PORT || '3000'],
    ['Admin', process.env.ADMIN_PORT || '3001'],
  ].map(([name, port]) => {
    const server = http.createServer((request, response) => {
      if (request.url === '/health') {
        response.writeHead(200, { 'Content-Type': 'application/json' });
        response.end(JSON.stringify({ ok: true, runtime }));
      } else {
        response.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });
        response.end(page(name, runtime));
      }
    });
    server.on('error', error => {
      console.error(error.message);
      process.exitCode = 1;
      for (const server of servers) server.close();
    });
    server.listen(Number(port), '127.0.0.1', () => console.log(`${name}: ${server.address().port}`));
    return server;
  });
  const stop = () => { for (const server of servers) server.close(); };
  process.once('SIGTERM', stop);
  process.once('SIGINT', stop);
}

if (require.main === module) start();
module.exports = { page };
