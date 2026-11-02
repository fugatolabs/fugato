"""Exercise the public bootstrap offline against fixture release downloads."""
import hashlib
import io
import os
from pathlib import Path
import subprocess
import tarfile

import pytest

ROOT = Path(__file__).resolve().parents[2]


def fixture(tmp_path, manifest):
    downloads = tmp_path / 'downloads'
    downloads.mkdir()
    archive = downloads / 'fugato_0.0.1_linux_amd64.tar.gz'
    with tarfile.open(archive, 'w:gz') as tar:
        for name, content in {
            'fugato': b'#!/bin/sh\necho fugato\n',
            'fugato-mcp': b'#!/bin/sh\necho mcp\n',
            'INSTALL.md': b'install', 'LICENSE': b'license', 'NOTICE': b'notice',
            'THIRD_PARTY_NOTICES.md': b'third-party notices',
            'third_party/dependency/LICENSE': b'upstream license',
            'skills/fugato-workflow/SKILL.md': b'workflow',
        }.items():
            info = tarfile.TarInfo('fugato_0.0.1_linux_amd64/' + name)
            info.size = len(content)
            info.mode = 0o755 if name in ('fugato', 'fugato-mcp') else 0o644
            tar.addfile(info, io.BytesIO(content))
    digest = hashlib.sha256(archive.read_bytes()).hexdigest()
    row = f'{digest}  {archive.name}\n'
    (downloads / 'checksums.txt').write_text(manifest(row))
    fake = tmp_path / 'tools'
    fake.mkdir()
    (fake / 'uname').write_text('#!/bin/sh\ncase "$1" in -s) echo Linux;; -m) echo x86_64;; esac\n')
    (fake / 'curl').write_text('''#!/usr/bin/env python3
import os, pathlib, shutil, sys
args = sys.argv[1:]
url = next(a for a in args if a.startswith('https://'))
shutil.copyfile(pathlib.Path(os.environ['FIXTURE_DOWNLOADS']) / url.rsplit('/', 1)[1], args[args.index('-o') + 1])
''')
    for p in fake.iterdir():
        p.chmod(0o755)
    return {**os.environ, 'PATH': str(fake) + os.pathsep + os.environ['PATH'],
            'FIXTURE_DOWNLOADS': str(downloads)}


def install(tmp_path, manifest):
    env = fixture(tmp_path, manifest)
    return subprocess.run(['sh', str(ROOT / 'install/install.sh'), '--version', '0.0.1',
                           '--bin-dir', str(tmp_path / 'bin'), '--share-dir', str(tmp_path / 'share')],
                          env=env, capture_output=True, text=True)


def test_installs_binaries_skill_and_notices_without_node(tmp_path):
    result = install(tmp_path, lambda row: row)
    assert result.returncode == 0, result.stderr
    assert os.access(tmp_path / 'bin/fugato', os.X_OK)
    assert os.access(tmp_path / 'bin/fugato-mcp', os.X_OK)
    assert (tmp_path / 'share/0.0.1/third_party/dependency/LICENSE').read_text() == 'upstream license'
    assert (tmp_path / 'share/0.0.1/skills/fugato-workflow/SKILL.md').is_file()
    assert 'setup --agent NAME' in result.stdout
    assert 'setup-agent.mjs' not in result.stdout


@pytest.mark.parametrize('manifest', [lambda row: row.replace(row[:64], '0' * 64),
                                      lambda row: row + row, lambda row: ''])
def test_rejects_invalid_manifest_before_installation(tmp_path, manifest):
    result = install(tmp_path, manifest)
    assert result.returncode != 0
    assert not (tmp_path / 'bin').exists()
