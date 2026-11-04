#!/usr/bin/env python3
"""Export the release's CLI help, schema, examples, and public documentation."""
import argparse
from contextlib import ExitStack
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import stat
import subprocess

ROOT = Path(__file__).resolve().parents[2]


class PublicSources:
    """Read tracked preview inputs or immutable blobs from a release commit."""

    def __init__(self, root, revision=None, require_clean=False):
        self.root = Path(root).resolve()
        if Path(self.git('rev-parse', '--show-toplevel').decode().strip()).resolve() != self.root:
            raise ValueError('Documentation root must be the Git repository root')
        self.commit = self.git('rev-parse', '--verify', '--end-of-options',
                               f'{revision or "HEAD"}^{{commit}}').decode().strip()
        self.committed = revision is not None
        if require_clean:
            if not self.committed:
                raise ValueError('A clean release export requires an explicit revision')
            head = self.git('rev-parse', 'HEAD').decode().strip()
            if self.commit != head or self.git('status', '--porcelain', '--untracked-files=no').strip():
                raise ValueError('Release export requires the intended revision at a clean tracked checkout')
        self.entries = {}
        if self.committed:
            records = self.git('ls-tree', '-r', '-z', '--full-tree', self.commit)
        else:
            records = self.git('ls-files', '--stage', '-z')
        for record in records.split(b'\0'):
            if not record:
                continue
            metadata, path = record.split(b'\t', 1)
            mode, second, third = metadata.decode().split()
            if not self.committed and third != '0':
                raise ValueError('Resolve Git index conflicts before exporting documentation')
            self.entries[path.decode('utf-8')] = (mode, third if self.committed else second)

    def git(self, *arguments):
        return subprocess.check_output(['git', *arguments], cwd=self.root)

    def validate(self, path):
        relative = PurePosixPath(path)
        if relative.is_absolute() or '..' in relative.parts or path not in self.entries:
            raise ValueError(f'Public input must be Git-tracked: {path}')
        if self.entries[path][0] not in ('100644', '100755'):
            raise ValueError(f'Public input must be a regular file, not a symlink or submodule: {path}')
        if not self.committed:
            current = self.root
            for part in relative.parts:
                current /= part
                if current.is_symlink():
                    raise ValueError(f'Refusing symlink in public input: {path}')
            if not current.resolve().is_relative_to(self.root) or not current.is_file():
                raise ValueError(f'Public input must be a regular file inside the repository: {path}')

    def read(self, path):
        self.validate(path)
        if self.committed:
            return self.git('cat-file', 'blob', self.entries[path][1])
        # Hold each directory open and refuse links at open time as well. A file
        # replaced after validation must not redirect a preview to private data.
        try:
            with ExitStack() as stack:
                flags = os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW
                directory = os.open(self.root, flags)
                stack.callback(os.close, directory)
                parts = PurePosixPath(path).parts
                for part in parts[:-1]:
                    directory = os.open(part, flags, dir_fd=directory)
                    stack.callback(os.close, directory)
                descriptor = os.open(parts[-1], os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=directory)
                with os.fdopen(descriptor, 'rb') as file:
                    if not stat.S_ISREG(os.fstat(file.fileno()).st_mode):
                        raise ValueError(f'Public input must be a regular file: {path}')
                    return file.read()
        except OSError as error:
            raise ValueError(f'Cannot open public input without following symlinks: {path}') from error

    def guides(self):
        for path in sorted(self.entries):
            if path == 'docs' or path.startswith('docs/'):
                # Reject directory links too; never traverse them looking for Markdown.
                if self.entries[path][0] not in ('100644', '100755'):
                    self.validate(path)
                if path.endswith('.md') and PurePosixPath(path).name != 'README.md':
                    yield path


def export(root=ROOT, revision=None, require_clean=False):
    inputs = PublicSources(root, revision, require_clean)
    sources = {}

    def read(path):
        content = inputs.read(path)
        sources[path] = hashlib.sha256(content).hexdigest()
        return content.decode('utf-8')

    version = re.search(r'const Version = "([^"]+)"', read('packages/fugato/internal/version/version.go')).group(1)
    help_text = read('packages/fugato/cmd/fugato/help.go')
    root_help = re.search(r'const rootHelp = `([^`]+)`', help_text).group(1)
    summaries = dict(re.findall(r'^  ([a-z]+) +(.+)$', root_help.split('Commands:\n')[1].split('\nRun ')[0], re.M))
    entries = re.findall(r'\t"([a-z]+)": `([^`]+)`', help_text)
    if set(summaries) != {name for name, _ in entries} or len(entries) != len(summaries):
        raise ValueError('CLI help format or command coverage changed; update the exporter')
    commands = []
    for name, text in entries:
        usage = text.split('\n\n', 1)[0]
        flags = sorted(set(re.findall(r'--[a-z][a-z-]*(?: [A-Z][A-Z_]*)?', usage)))
        commands.append(dict(name=name, summary=summaries[name], help=text.strip(), usage=usage, flags=flags))
    commands.sort(key=lambda command: command['name'])
    schema_text = read('docs/reference/fugato.schema.json')
    for path in ['packages/fugato/cmd/fugato/assets/fugato.schema.json', 'packages/cursor-extension/resources/fugato.schema.json']:
        if json.loads(read(path)) != json.loads(schema_text):
            raise ValueError(f'Schema drift: {path}')
    examples = {name: read(f'packages/fugato/cmd/fugato/assets/{name}.yaml') for name in ['native', 'compose', 'mixed']}
    guides = []
    for relative in inputs.guides():
        markdown = read(relative)
        title = re.search(r'^# (.+)$', markdown, re.M)
        if not title:
            raise ValueError(f'Missing title: {relative}')
        guides.append(dict(slug=relative[5:-3], path=relative, title=title.group(1), markdown=markdown))
    dirty = not inputs.committed and bool(inputs.git('status', '--porcelain', '--',
                                                    *(':(literal)' + path for path in sources)).strip())
    return dict(format=1, version=version, repository='fugatolabs/fugato', commit=inputs.commit, dirty=dirty,
                commands=commands, schema=json.loads(schema_text), examples=examples, guides=guides, sources=sources)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', required=True, type=Path)
    parser.add_argument('--cli', type=Path, help='Compare exported content with an already-built CLI')
    parser.add_argument('--revision', help='Read immutable inputs from this Git commit instead of the tracked working tree')
    parser.add_argument('--require-clean', action='store_true', help='Require --revision to match HEAD and a clean tracked checkout')
    args = parser.parse_args()
    bundle = export(revision=args.revision, require_clean=args.require_clean)
    if args.cli:
        def run(*arguments):
            return subprocess.check_output([str(args.cli.resolve()), *arguments], text=True).strip()
        for command in bundle['commands']:
            if run('help', command['name']) != command['help']:
                raise ValueError('Export differs from CLI help: ' + command['name'])
        if json.loads(run('schema')) != bundle['schema']:
            raise ValueError('Export differs from CLI schema')
        for name, text in bundle['examples'].items():
            if run('example', name) != text.strip():
                raise ValueError('Export differs from CLI example: ' + name)
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(bundle, indent=2, ensure_ascii=False) + '\n')
    print(f"Exported {len(bundle['commands'])} commands, {len(bundle['guides'])} articles, schema and examples for {bundle['version']}")
