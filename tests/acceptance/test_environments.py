#!/usr/bin/env python3
"""Exercise Compose and mixed environments using disposable owned resources."""
import http.client
import json
import time
import pytest

from .fixtures import ENVIRONMENT_CASES, ENVIRONMENT_COMPOSE, NATIVE_SERVER, SERVER
from .harness import check

pytestmark = [pytest.mark.environments, pytest.mark.docker]

def test_environments(acceptance):
    harness = acceptance
    for name, config in ENVIRONMENT_CASES.items():
        repo = harness.repository(name, {'compose.yaml': ENVIRONMENT_COMPOSE, 'server.js': SERVER, 'native.cjs': NATIVE_SERVER, '.fugato.yaml': config})
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
