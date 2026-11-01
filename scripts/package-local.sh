#!/bin/sh
set -eu

if [ "$#" -ne 3 ]; then
  echo "usage: $0 {darwin|linux} {amd64|arm64} OUTPUT_DIR" >&2
  exit 2
fi

target_os=$1
target_arch=$2
output_dir=$3
case "$target_os/$target_arch" in
  darwin/amd64|darwin/arm64|linux/amd64|linux/arm64) ;;
  *) echo "unsupported target: $target_os/$target_arch" >&2; exit 2 ;;
esac

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_dir=$(CDPATH= cd -- "$script_dir/.." && pwd)
mkdir -p "$output_dir"
output_dir=$(CDPATH= cd -- "$output_dir" && pwd)
stage_root=$(mktemp -d)
trap 'rm -rf "$stage_root"' EXIT HUP INT TERM
version=$(sed -n 's/^const Version = "\([^"]*\)"/\1/p' "$repo_dir/packages/fugato/internal/version/version.go")
[ -n "$version" ] || { echo "cannot determine release version" >&2; exit 1; }
commit=$(git -C "$repo_dir" rev-parse HEAD)
if [ -n "$(git -C "$repo_dir" status --porcelain --untracked-files=normal)" ]; then commit="$commit-dirty"; fi
module=$(cd "$repo_dir/packages/fugato" && go list -m)
ldflags="-X $module/internal/version.Commit=$commit"
package_name="fugato_${version}_${target_os}_${target_arch}"
stage_dir="$stage_root/$package_name"
mkdir "$stage_dir"

(
  cd "$repo_dir/packages/fugato"
  GOOS="$target_os" GOARCH="$target_arch" CGO_ENABLED=0 go build -trimpath -ldflags "$ldflags" -o "$stage_dir/fugato" ./cmd/fugato
  GOOS="$target_os" GOARCH="$target_arch" CGO_ENABLED=0 go build -trimpath -ldflags "$ldflags" -o "$stage_dir/fugato-mcp" ./cmd/fugato-mcp
)
cp "$repo_dir/docs/guides/install.md" "$stage_dir/INSTALL.md"
cp "$script_dir/setup-agent.mjs" "$stage_dir/setup-agent.mjs"
mkdir "$stage_dir/skills"
cp -R "$repo_dir/skills/fugato-workflow" "$stage_dir/skills/"
cp "$repo_dir/LICENSE" "$stage_dir/LICENSE"
cp "$repo_dir/NOTICE" "$stage_dir/NOTICE"
cp "$repo_dir/THIRD_PARTY_NOTICES.md" "$stage_dir/THIRD_PARTY_NOTICES.md"
cp -R "$repo_dir/third_party" "$stage_dir/third_party"
COPYFILE_DISABLE=1 tar --no-xattrs -C "$stage_root" -czf "$output_dir/$package_name.tar.gz" "$package_name"
(
  cd "$output_dir"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$package_name.tar.gz" > "$package_name.tar.gz.sha256"
  else
    shasum -a 256 "$package_name.tar.gz" > "$package_name.tar.gz.sha256"
  fi
)
printf '%s\n' "$output_dir/$package_name.tar.gz"
