#!/usr/bin/env python3
"""Exercise Compose and mixed environments using disposable owned resources."""
import argparse
import http.client
import json
import time
import traceback
from types import SimpleNamespace
from compose import Acceptance, SERVER, check

COMPOSE = '''services:
  api:
    image: node:20-alpine
    working_dir: /app
    command: [node, server.js]
    volumes: ['./server.js:/app/server.js:ro']
    ports: ['3000:3000']
  admin:
    image: node:20-alpine
    working_dir: /app
    command: [node, server.js]
    volumes: ['./server.js:/app/server.js:ro']
    expose: ['3000']
  worker:
    image: node:20-alpine
    command: [node, -e, 'setInterval(() => console.log("working"), 1000)']
  tools:
    image: node:20-alpine
    profiles: [tools]
'''


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary')
    args = parser.parse_args()
    harness = Acceptance(SimpleNamespace(artifacts=None, binary=args.binary, mcp_binary=None, only='web', skip_browser=True))
    failure = None
    try:
        harness.prepare()
        native = 'const http=require("node:http"); const s=http.createServer(async(q,r)=>{try{r.end(JSON.stringify({native:true,api:await(await fetch(process.env.API_URL)).json()}));}catch(e){r.writeHead(502);r.end(e.message);}}); s.listen(Number(process.env.PORT),"127.0.0.1"); process.on("SIGTERM",()=>s.close());\n'
        cases = {
            'no-browser': 'version: 1\nstart:\n  services: [api, worker]\nendpoints: {}\n',
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
'''
        }
        for name, config in cases.items():
            repo = harness.repository(name, {'compose.yaml': COMPOSE, 'server.js': SERVER, 'native.cjs': native, '.fugato.yaml': config})
            doctor = harness.register_project(repo)
            check(doctor.returncode == 0, f'{name} doctor failed: {doctor.stdout} {doctor.stderr}')
            runtime_id = next(key for key, value in harness.projects.items() if value == repo)
            start = harness.run([harness.binary, 'up', '--worktree', repo], check_exit=False)
            if name == 'rollback':
                check(start.returncode != 0 and '9' in start.stderr, f'failed task not reported: {start.stderr}')
                check(not harness.containers(runtime_id), 'failed mixed startup left containers')
                harness.mark('mixed startup task failure tears down owned containers')
                continue
            check(start.returncode == 0, f'{name} start failed: {start.stderr}')
            status, runtime = harness.api(f'/v1/runtimes/{runtime_id}')
            check(status == 200 and runtime['observed'] == 'running', f'runtime: {runtime}')
            status, endpoints = harness.api(f'/v1/runtimes/{runtime_id}/ports')
            check(status == 200, f'ports failed: {endpoints}')
            if name == 'no-browser':
                check(not runtime.get('preview_url') and not any(e.get('browser') for e in endpoints), 'inferred a browser from TCP port')
                check(len(endpoints) == 1 and endpoints[0]['host_port'] != 3000, f'Compose publication not remapped: {endpoints}')
                harness.mark('API and worker start without preview; Compose publications remain remapped')
            elif name == 'multi-preview':
                check({e.get('name') for e in endpoints} == {'website', 'admin'}, f'missing previews: {endpoints}')
                deadline = time.monotonic() + 15
                while True:
                    try:
                        harness.run([harness.binary, 'check', 'ready', '--worktree', repo])
                        break
                    except AssertionError:
                        if time.monotonic() > deadline: raise
                        time.sleep(.2)
                for command, expected in [('task', 'task-ok'), ('exec', 'exec-ok')]:
                    result = harness.run([harness.binary, 'run', command, '--worktree', repo])
                    check(expected in result.stdout, f'{command} lost output: {result.stdout}')
                harness.mark('multiple Compose previews, HTTP check, run and exec tasks')
            elif name == 'mixed':
                check({e.get('name') for e in endpoints} == {'api', 'native'}, f'mixed endpoints: {endpoints}')
                check(len({e['host_port'] for e in endpoints}) == 2, 'mixed ports collided')
                native_endpoint = next(e for e in endpoints if e.get('name') == 'native')
                deadline = time.monotonic() + 15
                while True:
                    try:
                        connection = http.client.HTTPConnection('127.0.0.1', native_endpoint['host_port'], timeout=5)
                        connection.request('GET', '/')
                        response = connection.getresponse()
                        payload = response.read()
                        connection.close()
                        check(response.status == 200 and json.loads(payload)['api']['ok'], f'native-to-Compose request failed: {payload!r}')
                        break
                    except (OSError, AssertionError):
                        if time.monotonic() > deadline: raise
                        time.sleep(.2)
                harness.mark('native frontend connects to Compose API through its declared dynamic endpoint')
            harness.down(repo)
            check(not harness.containers(runtime_id), f'{name} stop left containers')
    except BaseException as error:
        failure = repr(error)
        traceback.print_exc()
    cleanup_errors = harness.cleanup()
    summary = {'passed': failure is None and not cleanup_errors, 'failure': failure, 'cleanup_errors': cleanup_errors, 'checks': harness.checks}
    (harness.root / 'summary.json').write_text(json.dumps(summary, indent=2) + '\n')
    print(f"{'PASS' if summary['passed'] else 'FAIL'} environments: {harness.root}", flush=True)
    return 0 if summary['passed'] else 1


if __name__ == '__main__':
    raise SystemExit(main())
