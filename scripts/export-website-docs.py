#!/usr/bin/env python3
"""Export the release's CLI help, schema, examples, and public documentation."""
import argparse
import hashlib
import json
from pathlib import Path
import re
import subprocess

ROOT = Path(__file__).resolve().parents[1]


def export(root=ROOT):
    sources = {}

    def read(path):
        text = (root / path).read_text()
        sources[path] = hashlib.sha256(text.encode()).hexdigest()
        return text

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
    for path in sorted((root / 'docs').rglob('*.md')):
        if path.name == 'README.md':
            continue
        relative = path.relative_to(root).as_posix()
        markdown = read(relative)
        title = re.search(r'^# (.+)$', markdown, re.M)
        if not title:
            raise ValueError(f'Missing title: {relative}')
        guides.append(dict(slug=relative[5:-3], path=relative, title=title.group(1), markdown=markdown))
    commit = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=root, text=True).strip()
    dirty = bool(subprocess.check_output(['git', 'status', '--porcelain', '--', *sources], cwd=root, text=True).strip())
    return dict(format=1, version=version, repository='fugatolabs/fugato', commit=commit, dirty=dirty,
                commands=commands, schema=json.loads(schema_text), examples=examples, guides=guides, sources=sources)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', required=True, type=Path)
    parser.add_argument('--cli', type=Path, help='Compare exported content with an already-built CLI')
    args = parser.parse_args()
    bundle = export()
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
