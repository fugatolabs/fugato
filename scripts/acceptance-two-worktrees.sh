#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 || ! -x $1 ]]; then
  echo "usage: $0 /absolute/path/to/fugato" >&2
  exit 2
fi
binary=$1
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_dir=$(CDPATH= cd -- "$script_dir/.." && pwd)
temporary=$(mktemp -d)
source_repo="$temporary/source"
second_worktree="$temporary/second"
first_project="$source_repo/examples/example_2"
second_project="$second_worktree/examples/example_2"
export FUGATO_STATE_DIR="$temporary/state"
export FUGATO_API_PORT=${FUGATO_API_PORT:-47655}
export FUGATO_PROXY_PORT=${FUGATO_PROXY_PORT:-47656}
daemon_pid=
first_id=
second_id=

cleanup() {
  for project in "$first_project" "$second_project"; do
    if [[ -d "$project" ]]; then
      "$binary" down --worktree "$project" >/dev/null 2>&1 || true
    fi
  done
  for id in "$first_id" "$second_id"; do
    if [[ -n "$id" ]]; then
      docker compose -p "fugato-$id" -f "$first_project/compose.yaml" down -v --remove-orphans >/dev/null 2>&1 || true
    fi
  done
  if [[ -n "$daemon_pid" ]]; then
    kill "$daemon_pid" >/dev/null 2>&1 || true
    wait "$daemon_pid" >/dev/null 2>&1 || true
  fi
  if [[ -d "$second_worktree" ]]; then
    git -C "$source_repo" worktree remove --force "$second_worktree" >/dev/null 2>&1 || true
  fi
  rm -rf "$temporary"
}
trap cleanup EXIT

wait_ready() {
  local url=$1
  for ((attempt=0; attempt<120; attempt++)); do
    if curl --fail --silent --max-time 2 "$url" >/dev/null; then
      return 0
    fi
    sleep 0.25
  done
  echo "endpoint did not become ready: $url" >&2
  return 1
}

mkdir -p "$source_repo/examples/example_2"
rsync -a --exclude node_modules "$repo_dir/examples/example_2/" "$first_project/"
cat >"$first_project/.fugato.yaml" <<'YAML'
version: 1
start:
  services: [web]
endpoints:
  website:
    service: web
    port: 3000
    protocol: http
    browser: true
YAML
git -C "$source_repo" init -q
git -C "$source_repo" add -A
git -C "$source_repo" -c user.name='Fugato Acceptance' -c user.email='fugato-acceptance@example.invalid' commit -qm fixture
git -C "$source_repo" worktree add --detach "$second_worktree" HEAD >/dev/null

python3 - "$first_project/compose.yaml" "$second_project/compose.yaml" <<'PY'
from pathlib import Path
import sys

for filename in sys.argv[1:]:
    path = Path(filename)
    original = path.read_text()
    assert '"127.0.0.1::3000"' in original
    assert 'image: postgres:17-alpine\n' in original
    updated = original.replace('"127.0.0.1::3000"', '"3000:3000"')
    updated = updated.replace('image: postgres:17-alpine\n', 'image: postgres:17-alpine\n    ports:\n      - "5432:5432"\n')
    path.write_text(updated)
PY

"$binary" daemon >"$temporary/daemon.log" 2>&1 &
daemon_pid=$!
sleep 1
first_doctor=$("$binary" doctor --worktree "$first_project")
second_doctor=$("$binary" doctor --worktree "$second_project")
first_id=$(awk '/^Runtime ID:/ {print $3}' <<<"$first_doctor")
second_id=$(awk '/^Runtime ID:/ {print $3}' <<<"$second_doctor")
[[ -n "$first_id" && -n "$second_id" && "$first_id" != "$second_id" ]]
for report in "$first_doctor" "$second_doctor"; do
  grep -q 'PORT_LAN_BINDING_CHANGED.*web:3000' <<<"$report"
  grep -q 'PORT_LAN_BINDING_CHANGED.*db:5432' <<<"$report"
done

"$binary" up --worktree "$first_project" >/dev/null
"$binary" up --worktree "$second_project" >/dev/null
first_ports=$("$binary" ports --worktree "$first_project")
second_ports=$("$binary" ports --worktree "$second_project")
first_web=$(awk '$1=="web" && $2==3000 {print $4}' <<<"$first_ports")
first_db=$(awk '$1=="db" && $2==5432 {print $4}' <<<"$first_ports")
second_web=$(awk '$1=="web" && $2==3000 {print $4}' <<<"$second_ports")
second_db=$(awk '$1=="db" && $2==5432 {print $4}' <<<"$second_ports")
[[ -n "$first_web" && -n "$first_db" && -n "$second_web" && -n "$second_db" ]]
[[ "$first_web" != "$second_web" && "$first_db" != "$second_db" ]]
for endpoint in "$first_web" "$first_db" "$second_web" "$second_db"; do
  [[ "$endpoint" == 127.0.0.1:* ]]
done
wait_ready "http://$first_web/health"
wait_ready "http://$second_web/health"
curl --fail --silent -H "Host: website.$first_id.localhost:$FUGATO_PROXY_PORT" "http://127.0.0.1:$FUGATO_PROXY_PORT/health" >/dev/null
curl --fail --silent -H "Host: website.$second_id.localhost:$FUGATO_PROXY_PORT" "http://127.0.0.1:$FUGATO_PROXY_PORT/health" >/dev/null

curl --fail --silent -H 'Content-Type: application/json' -d '{"title":"acceptance-first"}' "http://$first_web/api/todos" >/dev/null
curl --fail --silent "http://$second_web/api/todos" | python3 -c 'import json,sys; assert json.load(sys.stdin) == []'
"$binary" down --worktree "$first_project" >/dev/null
if "$binary" ports --worktree "$first_project" >/dev/null 2>&1; then
  echo 'stopped runtime reported active ports' >&2
  exit 1
fi
"$binary" up --worktree "$first_project" >/dev/null
first_web=$("$binary" ports --worktree "$first_project" | awk '$1=="web" && $2==3000 {print $4}')
wait_ready "http://$first_web/health"
curl --fail --silent "http://$first_web/api/todos" | python3 -c 'import json,sys; assert any(item["title"] == "acceptance-first" for item in json.load(sys.stdin))'
curl --fail --silent -H "Host: website.$second_id.localhost:$FUGATO_PROXY_PORT" "http://127.0.0.1:$FUGATO_PROXY_PORT/health" >/dev/null

printf 'two worktrees passed: IDs %s and %s; four distinct loopback endpoints, proxy health, database isolation, and restart retention\n' "$first_id" "$second_id"
