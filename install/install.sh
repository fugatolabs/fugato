#!/bin/sh
# Download a pinned release, verify its checksum, and install into a user prefix.
set -eu
version=0.0.1
bin_dir=${FUGATO_BIN_DIR:-"$HOME/.local/bin"}
share_dir=${FUGATO_SHARE_DIR:-"$HOME/.local/share/fugato"}
usage() {
  cat <<EOF
Usage: install.sh [--version VERSION] [--bin-dir DIR] [--share-dir DIR]
Install CLI and MCP binaries for macOS/Linux (AMD64/ARM64).
Default version: $version; binary directory: $bin_dir
Licenses and installation instructions: $share_dir/VERSION
EOF
}
while [ "$#" -gt 0 ]; do
  case "$1" in
    --help|-h) usage; exit 0 ;;
    --version|--bin-dir|--share-dir)
      [ "$#" -ge 2 ] || { usage >&2; exit 2; }
      case "$1" in --version) version=${2#v} ;; --bin-dir) bin_dir=$2 ;; --share-dir) share_dir=$2 ;; esac
      shift 2 ;;
    *) usage >&2; exit 2 ;;
  esac
done
case "$version" in ''|*[!0-9A-Za-z.+-]*) echo 'invalid version' >&2; exit 2 ;; esac
case "$(uname -s)" in Darwin) target_os=darwin ;; Linux) target_os=linux ;; *) echo 'supported systems: macOS and Linux' >&2; exit 1 ;; esac
case "$(uname -m)" in arm64|aarch64) target_arch=arm64 ;; x86_64|amd64) target_arch=amd64 ;; *) echo 'supported CPUs: AMD64 and ARM64' >&2; exit 1 ;; esac
for tool in curl tar install; do command -v "$tool" >/dev/null 2>&1 || { echo "required tool: $tool" >&2; exit 1; }; done
if command -v sha256sum >/dev/null 2>&1; then checksum_tool=sha256sum
elif command -v shasum >/dev/null 2>&1; then checksum_tool=shasum
else echo 'required tool: sha256sum or shasum' >&2; exit 1; fi
archive="fugato_${version}_${target_os}_${target_arch}.tar.gz"
base="https://github.com/fugatolabs/fugato/releases/download/v$version"
staging=$(mktemp -d)
trap 'rm -rf "$staging"' EXIT HUP INT TERM
curl --fail --location --silent --show-error --proto '=https' --tlsv1.2 "$base/$archive" -o "$staging/$archive"
curl --fail --location --silent --show-error --proto '=https' --tlsv1.2 "$base/checksums.txt" -o "$staging/checksums.txt"
# Match exactly one expected filename; never pass arbitrary manifest paths to a checksum tool.
awk -v file="$archive" '$2 == file && length($1) == 64 && $1 !~ /[^0-9a-f]/ { print $1 "  " file; count++ } END { if (count != 1) exit 1 }' "$staging/checksums.txt" > "$staging/selected.sha256" || { echo 'missing or ambiguous archive checksum' >&2; exit 1; }
(cd "$staging" && if [ "$checksum_tool" = sha256sum ]; then sha256sum -c selected.sha256; else shasum -a 256 -c selected.sha256; fi)
tar -xzf "$staging/$archive" -C "$staging"
source_dir="$staging/fugato_${version}_${target_os}_${target_arch}"
[ -f "$source_dir/fugato" ] && [ -f "$source_dir/fugato-mcp" ] && [ -d "$source_dir/third_party" ] || { echo 'invalid Fugato archive' >&2; exit 1; }
[ -f "$source_dir/skills/fugato-workflow/SKILL.md" ] || { echo 'missing workflow skill in archive' >&2; exit 1; }
mkdir -p "$bin_dir" "$share_dir/$version"
install -m 755 "$source_dir/fugato" "$source_dir/fugato-mcp" "$bin_dir/"
cp "$source_dir/LICENSE" "$source_dir/NOTICE" "$source_dir/THIRD_PARTY_NOTICES.md" "$source_dir/INSTALL.md" "$share_dir/$version/"
cp -R "$source_dir/third_party" "$share_dir/$version/"
cp -R "$source_dir/skills" "$share_dir/$version/"
printf 'Installed Fugato %s in %s\nNotices: %s/%s\n' "$version" "$bin_dir" "$share_dir" "$version"
case ":$PATH:" in *":$bin_dir:"*) ;; *) printf 'Add this directory to PATH: %s\n' "$bin_dir" ;; esac
printf 'Verify with: %s/fugato --version\n' "$bin_dir"
printf 'Agent setup: "%s/fugato" setup --agent NAME --project /path/to/project --bin-dir "%s"\n' "$bin_dir" "$bin_dir"
