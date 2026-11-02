"""Validate positive scenario configurations through Fugato before requiring Docker."""
import json
import os
import subprocess

import pytest

from .fixtures import ENVIRONMENT_CASES, ENVIRONMENT_COMPOSE, NATIVE_SERVER, SERVER, WEB_ENVIRONMENT, WEB_ONLY
from .harness import check

pytestmark = pytest.mark.native


def test_positive_fixture_discovery(binaries, tmp_path):
    cli, _ = binaries
    state = tmp_path / 'state'
    env = os.environ | {'FUGATO_STATE_DIR': str(state)}
    scenarios = {name: (config, ENVIRONMENT_COMPOSE) for name, config in ENVIRONMENT_CASES.items()}
    scenarios['web-only'] = (WEB_ENVIRONMENT, WEB_ONLY)
    for name, (config, compose) in scenarios.items():
        repo = tmp_path / name
        repo.mkdir()
        subprocess.run(['git', 'init', '-q', str(repo)], env=env, check=True, timeout=10)
        for filename, content in {
            '.fugato.yaml': config, 'compose.yaml': compose,
            'server.js': SERVER, 'native.cjs': NATIVE_SERVER,
        }.items():
            (repo / filename).write_text(content)
        result = subprocess.run([str(cli), 'projects', '--json', '--worktree', str(repo)],
                                env=env, text=True, capture_output=True, timeout=15)
        check(result.returncode == 0, f'{name} fixture discovery failed: {result.stderr}')
        report = json.loads(result.stdout)
        check(not report['truncated'] and len(report['projects']) == 1,
              f'{name} fixture discovery was incomplete: {report}')
        project = report['projects'][0]
        check(project['configured'] and project.get('id') and not project.get('error'),
              f'{name} fixture configuration is invalid: {project}')
        if name == 'mixed':
            check(project['kind'] == 'mixed' and {'name': 'native', 'mode': 'service'} in project['commands'],
                  f'mixed fixture lost its native service: {project}')
    check(not (state / 'token').exists(), 'fixture preflight started a daemon')
