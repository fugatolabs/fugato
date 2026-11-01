#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
usage: scripts/install-local.sh [--from-binaries DIR] [--bin-dir DIR] [--project DIR] [--skip-extension] [--no-start]

Build and install the Fugato CLI/MCP server, register the MCP server with
Cursor, and install the Cursor extension. The daemon is part of the CLI and
starts on demand; no system service or Homebrew formula is required.
Building the Cursor extension requires Node.js 22 or newer.

  --from-binaries DIR  Install prebuilt fugato and fugato-mcp from DIR.
  --bin-dir DIR        Install executables in DIR (default: ~/.local/bin).
  --project DIR        Include this project path in the printed agent prompt.
  --skip-extension    Do not build or install the Cursor VSIX.
  --no-start          Do not start the daemon at the end of installation.
EOF
}

die() { printf 'error: %s\n' "$*" >&2; exit 1; }
require() { command -v "$1" >/dev/null 2>&1 || die "$1 is required"; }

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_dir=$(CDPATH= cd -- "$script_dir/.." && pwd)
bin_dir="$HOME/.local/bin"
source_bin_dir=""
project_dir=""
skip_extension=0
no_start=0

while (($#)); do
  case "$1" in
    --from-binaries|--bin-dir|--project)
      (($# >= 2)) || die "$1 requires a path"
      case "$1" in
        --from-binaries) source_bin_dir=$2 ;;
        --bin-dir) bin_dir=$2 ;;
        --project) project_dir=$2 ;;
      esac
      shift 2 ;;
    --skip-extension) skip_extension=1; shift ;;
    --no-start) no_start=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) usage >&2; die "unknown option: $1" ;;
  esac
done

case $(uname -s) in
  Darwin)
    state_dir="$HOME/Library/Application Support/fugato"
    cursor_settings="$HOME/Library/Application Support/Cursor/User/settings.json"
    ;;
  Linux)
    config_home=${XDG_CONFIG_HOME:-$HOME/.config}
    state_dir="$config_home/fugato"
    cursor_settings="$config_home/Cursor/User/settings.json"
    ;;
  *) die "only macOS and Linux are supported" ;;
esac

# Persist the effective daemon location for GUI launches, which do not
# necessarily inherit the terminal's environment.
state_dir=${FUGATO_STATE_DIR:-$state_dir}
api_port=${FUGATO_API_PORT:-46655}
proxy_port=${FUGATO_PROXY_PORT:-46656}
[[ $state_dir = /* ]] || die "FUGATO_STATE_DIR must be absolute"
[[ $api_port =~ ^[1-9][0-9]*$ ]] && ((api_port <= 65535)) || die "FUGATO_API_PORT must be 1-65535"
[[ $proxy_port =~ ^[1-9][0-9]*$ ]] && ((proxy_port <= 65535 && proxy_port != api_port)) || die "FUGATO_PROXY_PORT must be 1-65535 and differ from the API port"
[[ $bin_dir = /* ]] || die "--bin-dir must be absolute"
if [[ -n $project_dir ]]; then
  [[ $project_dir = /* && -d $project_dir ]] || die "--project must be an existing absolute directory"
fi

require node
if ((skip_extension == 0)); then
  node -e 'if (Number(process.versions.node.split(".")[0]) < 22) { console.error("Node.js 22 or newer is required to build the Cursor VSIX"); process.exit(1); }'
fi
if [[ -z $source_bin_dir ]]; then
  require go
  stage_dir=$(mktemp -d)
  trap 'rm -rf "$stage_dir"' EXIT
  (
    cd "$repo_dir/packages/fugato"
    go build -o "$stage_dir/fugato" ./cmd/fugato
    go build -o "$stage_dir/fugato-mcp" ./cmd/fugato-mcp
  )
  source_bin_dir=$stage_dir
fi
[[ -f $source_bin_dir/fugato && -f $source_bin_dir/fugato-mcp ]] || die "both fugato and fugato-mcp are required in $source_bin_dir"

mkdir -p "$bin_dir"
bin_dir=$(CDPATH= cd -- "$bin_dir" && pwd)
install -m 755 "$source_bin_dir/fugato" "$bin_dir/fugato"
install -m 755 "$source_bin_dir/fugato-mcp" "$bin_dir/fugato-mcp"

# The MCP protocol owns stdout. Start the daemon before execing the stdio
# server, and keep all CLI output away from the JSON-RPC stream.
printf -v quoted_cli '%q' "$bin_dir/fugato"
printf -v quoted_mcp '%q' "$bin_dir/fugato-mcp"
printf -v quoted_state '%q' "$state_dir"
launcher="$bin_dir/fugato-mcp-launch"
cat > "$launcher" <<EOF
#!/usr/bin/env bash
set -euo pipefail
export FUGATO_STATE_DIR=$quoted_state
export FUGATO_API_PORT=$api_port
export FUGATO_PROXY_PORT=$proxy_port
export FUGATO_API_URL=http://127.0.0.1:$api_port
export FUGATO_API_TOKEN_FILE=$quoted_state/token
$quoted_cli ls >/dev/null
exec $quoted_mcp "\$@"
EOF
chmod 755 "$launcher"

# Preserve unrelated Cursor MCP servers and reject malformed configuration.
mcp_config="$HOME/.cursor/mcp.json"
FUGATO_MCP_CONFIG=$mcp_config FUGATO_MCP_LAUNCHER=$launcher node <<'NODE'
const fs = require('node:fs');
const path = require('node:path');
const file = process.env.FUGATO_MCP_CONFIG;
let config = {};
if (fs.existsSync(file)) {
  config = JSON.parse(fs.readFileSync(file, 'utf8'));
}
if (!config || typeof config !== 'object' || Array.isArray(config)) {
  throw new Error('Cursor MCP configuration must be a JSON object');
}
if (config.mcpServers !== undefined &&
    (!config.mcpServers || typeof config.mcpServers !== 'object' || Array.isArray(config.mcpServers))) {
  throw new Error('mcpServers must be a JSON object');
}
config.mcpServers ??= {};
config.mcpServers.fugato = {type: 'stdio', command: process.env.FUGATO_MCP_LAUNCHER};
fs.mkdirSync(path.dirname(file), {recursive: true, mode: 0o700});
const tmp = `${file}.tmp-${process.pid}`;
fs.writeFileSync(tmp, `${JSON.stringify(config, null, 2)}\n`, {mode: 0o600, flag: 'wx'});
try {
  fs.renameSync(tmp, file);
} catch (error) {
  fs.unlinkSync(tmp);
  throw error;
}
NODE

if ((skip_extension == 0)); then
  require npm
  require cursor
  extension_dir="$repo_dir/packages/cursor-extension"
  (cd "$extension_dir" && npm ci && npm audit --audit-level=high && npm run package)
  version=$(node -p "require(process.argv[1]).version" "$extension_dir/package.json")
  vsix="$extension_dir/fugato-$version.vsix"
  [[ -f $vsix ]] || die "Cursor VSIX was not produced: $vsix"
  cursor --install-extension "$vsix" --force

  # Cursor settings are JSONC. Use the parser already locked by the extension
  # build so comments and unrelated settings survive the edit.
  FUGATO_SETTINGS_FILE=$cursor_settings FUGATO_CLI_PATH="$bin_dir/fugato" \
    FUGATO_STATE_PATH=$state_dir FUGATO_API_PORT_VALUE=$api_port \
    FUGATO_JSONC_PATH="$extension_dir/node_modules/jsonc-parser" node <<'NODE'
const fs = require('node:fs');
const path = require('node:path');
const jsonc = require(process.env.FUGATO_JSONC_PATH);
const file = process.env.FUGATO_SETTINGS_FILE;
let source = fs.existsSync(file) ? fs.readFileSync(file, 'utf8') : '{}\n';
const errors = [];
const value = jsonc.parse(source, errors, {allowTrailingComma: true});
if (errors.length || !value || typeof value !== 'object' || Array.isArray(value)) {
  throw new Error('Cursor settings must be a valid JSONC object');
}
for (const [key, setting] of Object.entries({
  'fugato.executablePath': process.env.FUGATO_CLI_PATH,
  'fugato.stateDirectory': process.env.FUGATO_STATE_PATH,
  'fugato.apiPort': Number(process.env.FUGATO_API_PORT_VALUE),
})) {
  const edits = jsonc.modify(source, [key], setting, {
    formattingOptions: {insertSpaces: true, tabSize: 2, eol: '\n'},
  });
  source = jsonc.applyEdits(source, edits);
}
fs.mkdirSync(path.dirname(file), {recursive: true, mode: 0o700});
const tmp = `${file}.tmp-${process.pid}`;
fs.writeFileSync(tmp, source, {mode: 0o600, flag: 'wx'});
try {
  fs.renameSync(tmp, file);
} catch (error) {
  fs.unlinkSync(tmp);
  throw error;
}
NODE
fi

if ((no_start == 0)); then
  FUGATO_STATE_DIR=$state_dir FUGATO_API_PORT=$api_port FUGATO_PROXY_PORT=$proxy_port \
    "$bin_dir/fugato" ls >/dev/null
  "$launcher" --check
else
  printf 'Daemon connection was not checked. Run %s --check when ready.\n' "$launcher"
fi

printf 'Installed CLI: %s\nInstalled MCP server: %s\nCursor MCP configuration: %s\n' \
  "$bin_dir/fugato" "$bin_dir/fugato-mcp" "$mcp_config"
case ":$PATH:" in
  *":$bin_dir:"*) ;;
  *) printf 'Add %s to your shell PATH to use the short fugato command.\n' "$bin_dir" ;;
esac
if ((skip_extension == 0)); then
  printf 'Installed Cursor extension and configured its user settings.\n'
fi
if ((no_start == 0)); then
  printf 'Fugato daemon is available on 127.0.0.1:%s.\n' "$api_port"
fi

if [[ -z $project_dir ]]; then
  project_dir='<ABSOLUTE_PROJECT_PATH>'
fi
printf '\nPaste this into your local AI agent for project setup:\n\n'
cat <<EOF
Configure $project_dir for Fugato's version: 1 environment schema. Inspect existing
.fugato.yaml files, manifests/scripts, Compose definitions, environment templates,
and local development documentation. Identify independent projects, persistent
services, tasks, interactive commands, prerequisites, and endpoints. Reuse existing
launch commands and infrastructure definitions. Write the smallest supported
configuration and preserve existing choices. Infer browser intent from evidence
of a UI, never an open port alone. Ask only about unresolved project/startup/browser
choices. Use native port/data variables where supported; report isolation limits
and unsupported capabilities. Inventory required ignored inputs and use the
installed schema for prepare.files, exclusions, and deliberate default-filter
exceptions. For an existing target worktree and an explicitly selected source
project, preview preparation with fugato prepare --worktree TARGET --from SOURCE
--dry-run --json. Apply only the authorized declared preparation, preserve existing
files, and report missing inputs without their contents. Do not embed secrets,
install dependencies, or start workloads during discovery. Run $bin_dir/fugato doctor --json, fix configuration
errors, and summarize startup, access, checks, prerequisites, and changed files.
Static validation does not establish runtime success.
EOF
