"""Synthetic projects and example locations used by acceptance scenarios."""
from .harness import SOURCE

EXAMPLE = SOURCE / "examples/example_2"

WEB_ENVIRONMENT = """version: 1
start:
  services: [web]
endpoints:
  website:
    service: web
    port: 3000
    protocol: http
    browser: true
"""


SERVER = """const http=require('node:http');
http.createServer((req,res)=>{res.writeHead(200,{'content-type':'application/json'});
res.end(JSON.stringify({ok:true,path:req.url}));}).listen(3000,'0.0.0.0');
"""
WEB_ONLY = """services:
  web:
    image: node:24-alpine
    working_dir: /app
    command: ["node", "server.js"]
    expose: ["3000"]
    volumes: ["./server.js:/app/server.js:ro"]
"""

ENVIRONMENT_COMPOSE = '''services:
  api:
    image: node:24-alpine
    working_dir: /app
    command: [node, server.js]
    volumes: ['./server.js:/app/server.js:ro']
    ports: ['3000:3000']
  admin:
    image: node:24-alpine
    working_dir: /app
    command: [node, server.js]
    volumes: ['./server.js:/app/server.js:ro']
    expose: ['3000']
  worker:
    image: node:24-alpine
    command: [node, -e, 'setInterval(() => console.log("working"), 1000)']
  tools:
    image: node:24-alpine
    profiles: [tools]
'''

NATIVE_SERVER = 'const http=require("node:http"); const s=http.createServer(async(q,r)=>{try{r.end(JSON.stringify({native:true,api:await(await fetch(process.env.API_URL)).json()}));}catch(e){r.writeHead(502);r.end(e.message);}}); s.listen(Number(process.env.PORT),"127.0.0.1"); process.on("SIGTERM",()=>s.close());\n'

ENVIRONMENT_CASES = {
    'no-browser': '''version: 1
start:
  services: [api, worker]
endpoints: {}
''',
    'multi-preview': '''version: 1
start:
  services: [api, admin]
endpoints:
  website: {service: api, port: 3000, protocol: http, browser: true}
  admin: {service: admin, port: 3000, protocol: http, browser: true}
checks:
  ready: {http: {endpoint: website, path: /health}}
commands:
  task: {mode: task, run: {service: tools, argv: [node, -e, 'console.log("task-ok")']}}
  exec: {mode: task, exec: {service: api, argv: [node, -e, 'console.log("exec-ok")']}}
''',
    'mixed': '''version: 1
start:
  services: [api]
  commands: [native]
commands:
  native:
    mode: service
    host:
      argv: [node, native.cjs]
      env: {PORT: '${FUGATO_PORT_native}', API_URL: 'http://127.0.0.1:${FUGATO_PORT_api}/health'}
endpoints:
  api: {service: api, port: 3000, protocol: http, browser: false}
  native: {command: native, port: 0, protocol: http, browser: true}
''',
    'rollback': '''version: 1
start:
  services: [api]
  commands: [fail]
commands:
  fail: {mode: task, host: {argv: [node, -e, 'process.exit(9)']}}
''',
}
