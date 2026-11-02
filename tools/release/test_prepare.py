import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest import mock


spec = importlib.util.spec_from_file_location('release_prepare', Path(__file__).with_name('prepare.py'))
prepare = importlib.util.module_from_spec(spec)
spec.loader.exec_module(prepare)


class ReleasePreparationTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        for name, text in {
            'packages/fugato/internal/version/version.go': 'package version\nconst Version = "0.0.1"\n',
            'packages/cursor-extension/package.json': json.dumps({'version': '0.0.1'}),
            'packages/cursor-extension/package-lock.json': json.dumps({'version': '0.0.1', 'packages': {'': {'version': '0.0.1'}}}),
            'install/install.sh': '#!/bin/sh\nversion=stale\nprintf "%s" "$version"\n',
            'docs/guides/install.md': 'Installation instructions\n',
        }.items():
            file = self.root / name
            file.parent.mkdir(parents=True, exist_ok=True)
            file.write_text(text)
        self.environment = mock.patch.dict(os.environ, {'FUGATO_VERSION': '0.0.1'})
        self.environment.start()
        self.addCleanup(self.environment.stop)

    def test_pins_installer_and_removes_stale_extension_without_building_one(self):
        output = self.root / '.release'
        output.mkdir()
        (output / 'old.vsix').write_bytes(b'old extension')
        with mock.patch.object(prepare.subprocess, 'run') as run:
            prepare.prepare(self.root)
        self.assertEqual(run.call_count, 1)
        self.assertIn('tools/docs/export.py', run.call_args.args[0][1])
        self.assertIn('version=0.0.1\n', (output / 'install.sh').read_text())
        self.assertEqual((output / 'INSTALL.md').read_text(), 'Installation instructions\n')
        self.assertFalse((output / 'old.vsix').exists())
        self.assertEqual((output / 'install.sh').stat().st_mode & 0o777, 0o755)

    def test_refuses_mismatched_extension_version_before_writing(self):
        (self.root / 'packages/cursor-extension/package.json').write_text('{"version":"9.0.0"}')
        with self.assertRaisesRegex(ValueError, 'versions differ'):
            prepare.prepare(self.root)
        self.assertFalse((self.root / '.release').exists())

    def test_refuses_mismatched_environment_version(self):
        with mock.patch.dict(os.environ, {'FUGATO_VERSION': '9.0.0'}):
            with self.assertRaisesRegex(ValueError, 'FUGATO_VERSION'):
                prepare.prepare(self.root)
        self.assertFalse((self.root / '.release').exists())

    def test_refuses_symlink_output_without_deleting_target(self):
        target = self.root / 'keep'
        target.mkdir()
        (target / 'file').write_text('keep')
        (self.root / '.release').symlink_to(target, target_is_directory=True)
        with self.assertRaisesRegex(ValueError, 'symlink'):
            prepare.prepare(self.root)
        self.assertEqual((target / 'file').read_text(), 'keep')


if __name__ == '__main__':
    unittest.main()
