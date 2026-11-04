import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest import mock

spec = importlib.util.spec_from_file_location('exporter', Path(__file__).resolve().parents[2] / 'tools/docs/export.py')
exporter = importlib.util.module_from_spec(spec)
spec.loader.exec_module(exporter)


class DocumentationExport(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.bundle = exporter.export()

    def test_all_help_commands_exported(self):
        names = {command['name'] for command in self.bundle['commands']}
        self.assertTrue({'doctor', 'prepare', 'destroy', 'example', 'schema', 'daemon'} <= names)
        for command in self.bundle['commands']:
            self.assertTrue(command['help'].startswith('Usage: fugato ' + command['name']))
            self.assertTrue(command['summary'])

    def test_complete_sources_and_unique_routes(self):
        paths = {guide['path'] for guide in self.bundle['guides']}
        self.assertTrue({'docs/reference/environment.md', 'docs/reference/mcp.md', 'docs/guides/agent-setup.md'} <= paths)
        self.assertEqual(len(paths), len(self.bundle['guides']))
        self.assertTrue(paths <= self.bundle['sources'].keys())
        self.assertEqual(set(self.bundle['examples']), {'native', 'compose', 'mixed'})
        self.assertEqual(self.bundle['schema']['properties']['version']['const'], 1)


class DocumentationPrivacy(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        inputs = exporter.PublicSources(exporter.ROOT, revision='HEAD')
        cls.public_files = {path: inputs.read(path) for path in exporter.export(revision='HEAD')['sources']}

    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.git('init', '--quiet')
        for path, content in self.public_files.items():
            file = self.root / path
            file.parent.mkdir(parents=True, exist_ok=True)
            file.write_bytes(content)
        self.git('add', '.')
        self.commit()

    def git(self, *arguments):
        return subprocess.check_output(['git', *arguments], cwd=self.root)

    def commit(self):
        self.git('-c', 'user.name=Documentation Test', '-c', 'user.email=docs@example.invalid',
                 '-c', 'commit.gpgsign=false', 'commit', '--quiet', '-m', 'Synthetic public documentation')

    def write(self, path, text):
        file = self.root / path
        file.parent.mkdir(parents=True, exist_ok=True)
        file.write_text(text)
        return file

    def test_ignored_and_untracked_notes_never_enter_previews_or_releases(self):
        self.write('docs/.gitignore', '*.private.md\n!public.private.md\n')
        self.git('add', 'docs/.gitignore')
        self.commit()
        ignored = 'docs/guides/local.private.md'
        untracked = 'docs/guides/public.private.md'
        for path in (ignored, untracked):
            self.write(path, '# Private note\nSYNTHETIC_PRIVATE_SENTINEL\n')
        self.assertEqual(self.git('check-ignore', ignored).decode().strip(), ignored)
        self.assertEqual(subprocess.run(['git', 'check-ignore', untracked], cwd=self.root).returncode, 1)
        for revision in (None, 'HEAD'):
            with self.subTest(revision=revision):
                bundle = exporter.export(self.root, revision=revision)
                self.assertFalse(bundle['dirty'])
                self.assertNotIn('SYNTHETIC_PRIVATE_SENTINEL', str(bundle))
                self.assertNotIn(ignored, bundle['sources'])
                self.assertNotIn(untracked, bundle['sources'])
        self.assertFalse(exporter.export(self.root, revision='HEAD', require_clean=True)['dirty'])

    def test_tracked_edits_are_previewed_but_release_reads_committed_bytes(self):
        path = 'docs/guides/agent-setup.md'
        original = (self.root / path).read_text()
        self.write(path, original + '\nSYNTHETIC_LOCAL_EDIT\n')
        preview = exporter.export(self.root)
        self.assertTrue(preview['dirty'])
        self.assertIn('SYNTHETIC_LOCAL_EDIT', str(preview))
        release = exporter.export(self.root, revision='HEAD')
        self.assertFalse(release['dirty'])
        self.assertNotIn('SYNTHETIC_LOCAL_EDIT', str(release))
        with self.assertRaisesRegex(ValueError, 'clean tracked checkout'):
            exporter.export(self.root, revision='HEAD', require_clean=True)

    def test_staged_public_document_is_previewed_only_until_committed(self):
        path = 'docs/guides/new.md'
        self.write(path, '# New public guide\nReviewed for staging.\n')
        self.git('add', path)
        preview = exporter.export(self.root)
        self.assertTrue(preview['dirty'])
        self.assertIn(path, preview['sources'])
        self.assertNotIn(path, exporter.export(self.root, revision='HEAD')['sources'])
        with self.assertRaisesRegex(ValueError, 'clean tracked checkout'):
            exporter.export(self.root, revision='HEAD', require_clean=True)

    def test_release_is_pinned_to_requested_commit(self):
        revision = self.git('rev-parse', 'HEAD').decode().strip()
        path = 'docs/guides/later.md'
        self.write(path, '# Later public guide\n')
        self.git('add', path)
        self.commit()
        bundle = exporter.export(self.root, revision=revision)
        self.assertEqual(bundle['commit'], revision)
        self.assertNotIn(path, bundle['sources'])
        with self.assertRaisesRegex(ValueError, 'intended revision'):
            exporter.export(self.root, revision=revision, require_clean=True)

    def test_tracked_files_stay_public_even_when_ignore_rule_matches(self):
        path = 'docs/guides/agent-setup.md'
        self.write('.gitignore', path + '\n')
        self.git('add', '.gitignore')
        self.commit()
        self.assertIn(path, exporter.export(self.root)['sources'])

    def test_tracked_file_and_directory_symlinks_are_rejected(self):
        outside = tempfile.TemporaryDirectory()
        self.addCleanup(outside.cleanup)
        private = Path(outside.name) / 'private.md'
        private.write_text('# Private external note\nSYNTHETIC_EXTERNAL_SENTINEL\n')
        for path in ('docs/leak.md', 'docs/linked-directory'):
            with self.subTest(path=path):
                file = self.root / path
                file.symlink_to(private if path.endswith('.md') else private.parent)
                self.git('add', path)
                for revision in (None, 'HEAD'):
                    if revision:
                        self.commit()
                    with self.assertRaisesRegex(ValueError, 'symlink'):
                        exporter.export(self.root, revision=revision)
                self.git('rm', '--quiet', path)
                self.commit()

    def test_release_never_reads_a_worktree_link_target(self):
        outside = tempfile.TemporaryDirectory()
        self.addCleanup(outside.cleanup)
        private = Path(outside.name) / 'private.md'
        private.write_text('# Private external note\nSYNTHETIC_EXTERNAL_SENTINEL\n')
        file = self.root / 'docs/guides/agent-setup.md'
        file.unlink()
        file.symlink_to(private)
        with self.assertRaisesRegex(ValueError, 'symlink'):
            exporter.export(self.root)
        self.assertNotIn('SYNTHETIC_EXTERNAL_SENTINEL', str(exporter.export(self.root, revision='HEAD')))

    def test_link_replacement_after_validation_cannot_redirect_a_read(self):
        path = 'docs/guides/agent-setup.md'
        private = self.write('private-local.md', '# Private local note\nSYNTHETIC_PRIVATE_SENTINEL\n')
        inputs = exporter.PublicSources(self.root)
        validate = inputs.validate

        def replace_after_validation(name):
            validate(name)
            (self.root / name).unlink()
            (self.root / name).symlink_to(private)

        with mock.patch.object(inputs, 'validate', side_effect=replace_after_validation):
            with self.assertRaisesRegex(ValueError, 'without following symlinks'):
                inputs.read(path)

    def test_worktree_symlink_replacing_tracked_parent_is_rejected(self):
        directory = self.root / 'docs/guides'
        target = self.root / 'local-guides'
        directory.rename(target)
        directory.symlink_to(target, target_is_directory=True)
        with self.assertRaisesRegex(ValueError, 'symlink'):
            exporter.export(self.root)

    def test_untracked_required_input_and_outside_paths_are_rejected(self):
        path = 'docs/reference/fugato.schema.json'
        self.git('rm', '--cached', '--quiet', path)
        with self.assertRaisesRegex(ValueError, 'must be Git-tracked'):
            exporter.export(self.root)
        inputs = exporter.PublicSources(self.root)
        for path in ('../private.md', '/private.md'):
            with self.subTest(path=path), self.assertRaisesRegex(ValueError, 'must be Git-tracked'):
                inputs.read(path)

    def test_clean_release_requires_explicit_revision(self):
        with self.assertRaisesRegex(ValueError, 'explicit revision'):
            exporter.export(self.root, require_clean=True)

    def test_release_preparation_exports_clean_committed_inputs_end_to_end(self):
        spec = importlib.util.spec_from_file_location('prepare', exporter.ROOT / 'tools/release/prepare.py')
        prepare = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(prepare)
        version = prepare.release_version(exporter.ROOT)
        self.write('packages/cursor-extension/package.json', json.dumps({'version': version}))
        self.write('packages/cursor-extension/package-lock.json',
                   json.dumps({'version': version, 'packages': {'': {'version': version}}}))
        self.write('install/install.sh', '#!/bin/sh\nversion=stale\n')
        self.write('tools/docs/export.py', Path(exporter.__file__).read_text())
        self.git('add', '.')
        self.commit()
        self.write('.git/info/exclude', 'docs/local-private.md\n')
        self.write('docs/local-private.md', '# Private note\nSYNTHETIC_PRIVATE_SENTINEL\n')
        prepare.prepare(self.root)
        bundle = json.loads((self.root / '.release/docs.json').read_text())
        self.assertEqual(bundle, exporter.export(self.root, revision='HEAD', require_clean=True))
        self.assertNotIn('SYNTHETIC_PRIVATE_SENTINEL', str(bundle))


if __name__ == '__main__':
    unittest.main()
