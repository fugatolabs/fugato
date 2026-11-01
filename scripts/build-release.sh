#!/bin/sh
# Assemble release assets locally; this never uploads or publishes them.
set -eu
usage() { echo "usage: $0 OUTPUT_DIR [--extension]" >&2; exit 2; }
[ "$#" -ge 1 ] && [ "$#" -le 2 ] || usage
output_dir=$1
with_extension=false
if [ "$#" -eq 2 ]; then [ "$2" = --extension ] || usage; with_extension=true; fi
if "$with_extension"; then
  node -e 'if (Number(process.versions.node.split(".")[0]) < 22) { console.error("Node.js 22 or newer is required to build the Cursor VSIX"); process.exit(1); }'
fi
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_dir=$(CDPATH= cd -- "$script_dir/.." && pwd)
version=$(sed -n 's/^const Version = "\([^"]*\)"/\1/p' "$repo_dir/packages/fugato/internal/version/version.go")
[ -n "$version" ] || { echo 'missing release version' >&2; exit 1; }
python3 - "$repo_dir" "$version" <<'PY'
import json, pathlib, sys
root, version = pathlib.Path(sys.argv[1]), sys.argv[2]
package = json.loads((root / 'packages/cursor-extension/package.json').read_text())
lock = json.loads((root / 'packages/cursor-extension/package-lock.json').read_text())
assert package['version'] == lock['version'] == lock['packages']['']['version'] == version, 'release versions differ'
PY
python3 "$script_dir/generate-notices.py" --check
mkdir -p "$output_dir"
output_dir=$(CDPATH= cd -- "$output_dir" && pwd)
[ -z "$(ls -A "$output_dir")" ] || { echo 'output directory must be empty' >&2; exit 1; }
for target_os in darwin linux; do
  for target_arch in amd64 arm64; do
    "$script_dir/package-local.sh" "$target_os" "$target_arch" "$output_dir"
  done
done
sed "s/^version=.*/version=$version/" "$script_dir/install-release.sh" > "$output_dir/install.sh"
chmod +x "$output_dir/install.sh"
python3 "$script_dir/export-website-docs.py" --output "$output_dir/docs.json"
if "$with_extension"; then
  (cd "$repo_dir/packages/cursor-extension" && npm ci && npm audit --audit-level=high && npm run package)
  cp "$repo_dir/packages/cursor-extension/fugato-$version.vsix" "$output_dir/"
fi
python3 - "$output_dir" <<'PY'
import hashlib, pathlib, sys
root = pathlib.Path(sys.argv[1])
assets = sorted(p for p in root.iterdir() if p.suffix != '.sha256')
(root / 'checksums.txt').write_text(''.join(f'{hashlib.sha256(p.read_bytes()).hexdigest()}  {p.name}\n' for p in assets))
PY
python3 "$script_dir/generate-homebrew-formula.py" "$output_dir" --version "$version" > "$output_dir/fugato.rb"
python3 - "$output_dir" <<'PY'
import hashlib, pathlib, sys
root = pathlib.Path(sys.argv[1])
formula = root / 'fugato.rb'
with (root / 'checksums.txt').open('a') as manifest:
    manifest.write(f'{hashlib.sha256(formula.read_bytes()).hexdigest()}  {formula.name}\n')
PY
printf 'Release %s assembled in %s\n' "$version" "$output_dir"
