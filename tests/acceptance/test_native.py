"""Native process isolation, discovery, commands, previews and lifecycle."""
import json
import shutil
import socket
import time
from urllib.parse import urlsplit

import pytest

from .harness import SOURCE, check as require

pytestmark = pytest.mark.native


def test_native_environments(acceptance):
    harness = acceptance
    root, binary, env = harness.root, harness.binary, harness.env
    runtime_ids, assigned_ports, preview_urls = [], [], {}
    mark = harness.mark

    def run(argv, cwd=None, expected=0, stdin=None):
        result = harness.run(argv, cwd=cwd, timeout=90, check_exit=False, stdin=stdin)
        require(result.returncode == expected,
                f'{argv[:3]}: expected {expected}, got {result.returncode}: {result.stdout[-1000:]} {result.stderr[-1000:]}')
        return result.stdout

    def api(route, body=None, expected_status=200):
        status, payload = harness.api(route, body)
        require(status == expected_status, f'{route}: HTTP {status}: {payload}')
        return payload

    def reachable(port):
        with socket.socket() as sock:
            sock.settimeout(.2)
            return sock.connect_ex(('127.0.0.1', port)) == 0

    def health(endpoint):
        return harness.preview(endpoint['url'], 'health')

    repo = root / 'repo'
    shutil.copytree(SOURCE / 'examples/native-app', repo)
    run(['git', 'init', '-q', repo])
    run(['git', 'add', '.'], repo)
    run(['git', '-c', 'user.name=Fugato Acceptance', '-c', 'user.email=acceptance@localhost',
         'commit', '-qm', 'native acceptance fixture'], repo)
    worktree = root / 'second'
    run(['git', 'worktree', 'add', '--detach', worktree, 'HEAD'], repo)
    harness.worktrees.append((repo, worktree))
    for directory in (repo, worktree):
        report = json.loads(run([binary, 'doctor', '--json', '--worktree', directory]))
        require(report['ok'], f'doctor: {report}')
        runtime_ids.append(report['project']['runtime_id'])
        harness.projects[runtime_ids[-1]] = directory
    discovery = json.loads(run([binary, 'projects', '--json', '--worktree', repo]))
    require(len(discovery['projects']) == 1, f'unexpected projects: {discovery}')
    require(not (root / 'state/token').exists(), 'discovery or doctor started daemon')
    mark('doctor and project discovery do not start daemon or workloads')

    harness.start_daemon()

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
    error = harness.run([binary, 'open', '--worktree', repo], check_exit=False, timeout=10)
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
    harness.stop_daemon()
    require(not any(reachable(port) for port in assigned_ports), 'native child survived daemon shutdown')
    mark('daemon shutdown stops native process groups')
    print(f'Evidence: {root}', flush=True)
