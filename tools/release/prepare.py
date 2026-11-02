#!/usr/bin/env python3
"""Prepare version-matched supplementary assets for GoReleaser; never publish."""

import json
import os
from pathlib import Path
import re
import shutil
import subprocess

ROOT = Path(__file__).resolve().parents[2]


def release_version(root=ROOT):
    text = (root / 'packages/fugato/internal/version/version.go').read_text()
    match = re.search(r'^const Version = "([^"]+)"$', text, re.M)
    if not match or not re.fullmatch(r'[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?', match[1]):
        raise ValueError('Cannot determine release version')
    version = match[1]
    package = json.loads((root / 'packages/cursor-extension/package.json').read_text())
    lock = json.loads((root / 'packages/cursor-extension/package-lock.json').read_text())
    if not (package['version'] == lock['version'] == lock['packages']['']['version'] == version):
        raise ValueError('Runtime and extension release versions differ')
    return version


def prepare(root=ROOT, with_extension=False):
    version = release_version(root)
    if os.environ.get('FUGATO_VERSION', version) != version:
        raise ValueError('FUGATO_VERSION differs from the source version')
    output = root / '.release'
    if output.is_symlink():
        raise ValueError('Refusing a symlink at .release')
    if output.exists():
        shutil.rmtree(output)
    output.mkdir()
    installer = (root / 'install/install.sh').read_text()
    installer, count = re.subn(r'^version=.*$', f'version={version}', installer, flags=re.M)
    if count != 1:
        raise ValueError('Installer must contain exactly one default version')
    (output / 'install.sh').write_text(installer)
    (output / 'install.sh').chmod(0o755)
    shutil.copyfile(root / 'docs/guides/install.md', output / 'INSTALL.md')
    subprocess.run(['python3', str(root / 'tools/docs/export.py'), '--output', str(output / 'docs.json')], check=True)
    if with_extension:
        extension = root / 'packages/cursor-extension'
        for command in (['npm', 'ci'], ['npm', 'audit', '--audit-level=high'], ['npm', 'run', 'package']):
            subprocess.run(command, cwd=extension, check=True)
        shutil.copyfile(extension / f'fugato-{version}.vsix', output / f'fugato-{version}.vsix')
    print(f'Prepared supplementary release assets for {version}')


if __name__ == '__main__':
    prepare(with_extension=os.environ.get('WITH_EXTENSION') == '1')
