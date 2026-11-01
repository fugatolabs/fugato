#!/usr/bin/env python3
"""Disposable acceptance checks for native environments; no Docker required."""
import argparse
import http.client
import json
import os
from pathlib import Path
import shutil
import signal
import socket
import subprocess
import tempfile
import time
from urllib.parse import urlsplit

SOURCE = Path(__file__).resolve().parents[2]


def require(value, message):
    if not value:
        raise AssertionError(message)


def free_port():
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        return sock.getsockname()[1]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', type=Path)
    args = parser.parse_args()
    root = Path(tempfile.mkdtemp(prefix='fugato-native-acceptance-')).resolve()
    binary = args.binary.resolve() if args.binary else root / 'fugato'
    env = os.environ | {'FUGATO_STATE_DIR': str(root / 'state'),
                        'FUGATO_API_PORT': str(free_port()), 'FUGATO_PROXY_PORT': str(free_port())}
    while env['FUGATO_API_PORT'] == env['FUGATO_PROXY_PORT']:
        env['FUGATO_PROXY_PORT'] = str(free_port())
    evidence = []
    daemon = None
    runtime_ids = []
    assigned_ports = []
    preview_urls = {}
    log = (root / 'commands.log').open('w')

    def run(argv, cwd=None, expected=0, stdin=None):
        result = subprocess.run([str(v) for v in argv], cwd=cwd, env=env, text=True,
                                input=stdin, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=90)
        log.write(json.dumps([str(v) for v in argv]) + '\n' + result.stdout + result.stderr + '\n')
        log.flush()
        require(result.returncode == expected,
                f'{argv[:3]}: expected {expected}, got {result.returncode}: {result.stdout[-1000:]} {result.stderr[-1000:]}')
        return result.stdout

    def api(route, body=None, expected_status=200):
        connection = http.client.HTTPConnection('127.0.0.1', int(env['FUGATO_API_PORT']), timeout=75)
        token = (root / 'state/token').read_text().strip()
        connection.request('POST' if body is not None else 'GET', route,
                           json.dumps(body) if body is not None else None,
                           {'Authorization': 'Bearer ' + token, 'Content-Type': 'application/json'})
        response = connection.getresponse()
        payload = response.read()
        connection.close()
        require(response.status == expected_status, f'{route}: HTTP {response.status}: {payload[:1000]!r}')
        return json.loads(payload)

    def mark(name):
        evidence.append(name)
        (root / 'results.json').write_text(json.dumps(evidence, indent=2) + '\n')
        print('PASS ' + name, flush=True)

    def reachable(port):
        with socket.socket() as sock:
            sock.settimeout(.2)
            return sock.connect_ex(('127.0.0.1', port)) == 0

    def health(endpoint):
        url = urlsplit(endpoint['url'])
        connection = http.client.HTTPConnection('127.0.0.1', url.port, timeout=5)
        connection.request('GET', '/health', headers={'Host': url.netloc})
        response = connection.getresponse()
        body = response.read()
        connection.close()
        require(response.status == 200, f'preview {endpoint}: {response.status} {body!r}')
        return json.loads(body)

    try:
        if not args.binary:
            run(['go', 'build', '-o', binary, './cmd/fugato'], SOURCE / 'packages/fugato')
        repo = root / 'repo'
        shutil.copytree(SOURCE / 'examples/native-app', repo)
        run(['git', 'init', '-q', repo])
        run(['git', 'add', '.'], repo)
        run(['git', '-c', 'user.name=Fugato Acceptance', '-c', 'user.email=acceptance@localhost',
             'commit', '-qm', 'native acceptance fixture'], repo)
        worktree = root / 'second'
        run(['git', 'worktree', 'add', '--detach', worktree, 'HEAD'], repo)
        for directory in (repo, worktree):
            report = json.loads(run([binary, 'doctor', '--json', '--worktree', directory]))
            require(report['ok'], f'doctor: {report}')
            runtime_ids.append(report['project']['runtime_id'])
        discovery = json.loads(run([binary, 'projects', '--json', '--worktree', repo]))
        require(len(discovery['projects']) == 1, f'unexpected projects: {discovery}')
        require(not (root / 'state/token').exists(), 'discovery or doctor started daemon')
        mark('doctor and project discovery do not start daemon or workloads')

        with (root / 'daemon.log').open('w') as daemon_log:
            daemon = subprocess.Popen([str(binary), 'daemon'], env=env, stdout=daemon_log, stderr=daemon_log)
        deadline = time.monotonic() + 15
        while time.monotonic() < deadline:
            require(daemon.poll() is None, 'daemon exited before readiness')
            try:
                api('/v1/health')
                break
            except (OSError, AssertionError):
                time.sleep(.1)
        else:
            raise AssertionError('daemon readiness timed out')

        for directory in (repo, worktree):
            run([binary, 'up', '--worktree', directory])
        for runtime_id in runtime_ids:
            endpoints = api(f'/v1/runtimes/{runtime_id}/ports')
            require({e.get('name') for e in endpoints} == {'website', 'admin'}, f'endpoints: {endpoints}')
            for endpoint in endpoints:
                require(urlsplit(endpoint['url']).hostname == f"{endpoint['name']}.{runtime_id}.localhost",
                        f'preview is not scoped to the endpoint and worktree: {endpoint}')
                preview_urls[(runtime_id, endpoint['name'])] = endpoint['url']
            assigned_ports.extend(e['host_port'] for e in endpoints)
            deadline = time.monotonic() + 10
            while True:
                try:
                    require(all(health(e)['runtime'] == runtime_id for e in endpoints), 'preview crossed worktrees')
                    break
                except (OSError, AssertionError):
                    if time.monotonic() >= deadline:
                        raise
                    time.sleep(.1)
        require(len(set(assigned_ports)) == 4, f'host ports collided: {assigned_ports}')
        mark('two worktrees with four distinct native ports and correctly routed named previews')
        run([binary, 'check', 'ready', '--worktree', repo])
        run([binary, 'run', 'test', '--worktree', repo])
        run([binary, 'check', 'tests', '--worktree', repo])
        require('Worker started' in run([binary, 'logs', '--worktree', repo]), 'worker logs missing')
        mark('named HTTP/task checks and worker logs')
        run([binary, 'run', 'console', '--worktree', repo], stdin='console.log("interactive-ok");\n.exit\n')
        mark('interactive command consumes caller stdin')
        error = subprocess.run([str(binary), 'open', '--worktree', str(repo)], env=env,
                               text=True, capture_output=True, timeout=10)
        require(error.returncode != 0 and 'endpoint' in error.stderr.lower(), 'multiple previews silently chose a browser')
        mark('multiple browser endpoints require selection')

        for runtime_id in runtime_ids:
            data_file = root / 'state/data' / runtime_id / 'runtime.txt'
            require(data_file.read_text() == runtime_id, 'native data crossed worktrees')
        retained = root / 'state/data' / runtime_ids[0] / 'retained.txt'
        retained.write_text('keep across stop and restart')
        run([binary, 'down', '--worktree', repo])
        stopped = api(f'/v1/runtimes/{runtime_ids[0]}/ports', expected_status=400)
        require('not running' in stopped.get('error', ''), 'stopped runtime still advertises ports')
        require(not any(reachable(port) for port in assigned_ports[:2]), 'stop left a native listener running')
        require(all(reachable(port) for port in assigned_ports[2:]), 'stop affected another worktree')
        require(retained.read_text() == 'keep across stop and restart', 'stop removed retained data')
        run([binary, 'up', '--worktree', repo])
        restarted = api(f'/v1/runtimes/{runtime_ids[0]}/ports')
        assigned_ports.extend(e['host_port'] for e in restarted)
        require(all(e['url'] == preview_urls[(runtime_ids[0], e['name'])] for e in restarted),
                'preview URLs changed after restart')
        require(retained.read_text() == 'keep across stop and restart', 'restart removed retained data')
        mark('separate retained data, scoped stop, restart, and no stopped port advertisement')

        # A task-only project is discoverable and runs without a preview or persistent service.
        task = repo / 'tasks'
        task.mkdir()
        (task / '.fugato.yaml').write_text('version: 1\ncommands:\n  fail:\n    mode: task\n    host:\n      argv: [node, -e, "process.exit(7)"]\n')
        run([binary, 'run', 'fail', '--worktree', task], expected=7)
        manifest = repo / 'candidate'
        manifest.mkdir()
        (manifest / 'package.json').write_text('{"scripts":{"dev":"node server.js"}}')
        discovery = json.loads(run([binary, 'projects', '--json', '--worktree', repo]))
        require(len(discovery['projects']) == 3, f'multi-project/candidate discovery: {discovery}')
        mark('finite task exit status and nested manifest/project discovery')

        # Graceful daemon exit must stop every native child, including npm descendants.
        daemon.send_signal(signal.SIGTERM)
        require(daemon.wait(timeout=15) == 0, 'daemon shutdown failed')
        require(not any(reachable(port) for port in assigned_ports), 'native child survived daemon shutdown')
        mark('daemon shutdown stops native process groups')
        print(f'Evidence: {root}', flush=True)
    finally:
        if daemon is not None and daemon.poll() is None:
            for runtime_id in runtime_ids:
                try:
                    api(f'/v1/runtimes/{runtime_id}/down', {})
                except Exception as error:
                    log.write('cleanup: ' + str(error) + '\n')
            daemon.send_signal(signal.SIGTERM)
            try:
                daemon.wait(timeout=15)
            except subprocess.TimeoutExpired:
                daemon.kill()
                daemon.wait(timeout=5)
        log.close()
        print(f'Artifacts retained in {root}', flush=True)


if __name__ == '__main__':
    main()
