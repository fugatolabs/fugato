#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 || ! -x $1 ]]; then
  echo "usage: $0 /absolute/path/to/fugato" >&2
  exit 2
fi
binary=$1
temporary=$(mktemp -d)
source_repo="$temporary/source"
second_worktree="$temporary/second"
export FUGATO_STATE_DIR="$temporary/state"
export FUGATO_API_PORT=${FUGATO_API_PORT:-48655}
export FUGATO_PROXY_PORT=${FUGATO_PROXY_PORT:-48656}
daemon_pid=
first_id=
second_id=
unrelated_volume=
unrelated_network=
cleanup() {
  for id in "$first_id" "$second_id"; do
    if [[ -n "$id" ]]; then
      "$binary" destroy --runtime "$id" --volumes >/dev/null 2>&1 || true
      # The fixture owns these exact projects, including volumes retained by a
      # successful default destroy if a later test step fails.
      docker compose -p "fugato-$id" -f "$source_repo/compose.yaml" down --volumes --remove-orphans >/dev/null 2>&1 || true
    fi
  done
  if [[ -n "$unrelated_volume" ]]; then
    docker volume rm "$unrelated_volume" >/dev/null 2>&1 || true
  fi
  if [[ -n "$unrelated_network" ]]; then
    docker network rm "$unrelated_network" >/dev/null 2>&1 || true
  fi
  if [[ -n "$daemon_pid" ]]; then
    kill "$daemon_pid" >/dev/null 2>&1 || true
    wait "$daemon_pid" >/dev/null 2>&1 || true
  fi
  rm -rf "$temporary"
}
trap cleanup EXIT

mkdir -p "$source_repo"
cat > "$source_repo/.fugato.yaml" <<'YAML'
version: 1
compose: compose.yaml
start:
  services: [worker]
YAML
cat > "$source_repo/compose.yaml" <<'YAML'
services:
  worker:
    image: postgres:17-alpine
    command: [sh, -c, 'while :; do sleep 1; done']
    cpus: 0.5
    mem_limit: 128m
    volumes:
      - data:/var/lib/postgresql/data
volumes:
  data:
YAML
git -C "$source_repo" init -q
git -C "$source_repo" add -A
git -C "$source_repo" -c user.name='Fugato Acceptance' -c user.email='fugato-acceptance@example.invalid' commit -qm fixture
git -C "$source_repo" worktree add --detach "$second_worktree" HEAD >/dev/null
"$binary" daemon >"$temporary/daemon.log" 2>&1 &
daemon_pid=$!
for ((attempt=0; attempt<100; attempt++)); do
  if [[ -f "$FUGATO_STATE_DIR/token" ]] && curl --silent --fail --max-time 1 -H "Authorization: Bearer $(cat "$FUGATO_STATE_DIR/token")" "http://127.0.0.1:$FUGATO_API_PORT/v1/health" >/dev/null; then
    break
  fi
  sleep 0.1
done
first_id=$("$binary" doctor --worktree "$source_repo" --json | python3 -c 'import json,sys; print(json.load(sys.stdin)["project"]["runtime_id"])')
second_id=$("$binary" doctor --worktree "$second_worktree" --json | python3 -c 'import json,sys; print(json.load(sys.stdin)["project"]["runtime_id"])')
[[ -n "$first_id" && -n "$second_id" && "$first_id" != "$second_id" ]]
# Matching names without Compose ownership labels must never be selected.
unrelated_volume="fugato-${first_id}_unowned"
unrelated_network="fugato-${first_id}_unowned"
docker volume create "$unrelated_volume" >/dev/null
docker network create "$unrelated_network" >/dev/null
"$binary" up --worktree "$source_repo" >/dev/null
"$binary" up --worktree "$second_worktree" >/dev/null
first_container=$(docker ps -q --filter "label=com.docker.compose.project=fugato-$first_id")
second_container=$(docker ps -q --filter "label=com.docker.compose.project=fugato-$second_id")
[[ -n "$first_container" && -n "$second_container" ]]
image_id=$(docker inspect --format '{{.Image}}' "$first_container")
docker exec "$first_container" sh -c 'printf first > /var/lib/postgresql/data/marker'
docker exec "$second_container" sh -c 'printf second > /var/lib/postgresql/data/marker'
"$binary" destroy --worktree "$source_repo" >/dev/null
[[ -z $(docker ps -aq --filter "label=com.docker.compose.project=fugato-$first_id") ]]
[[ -z $(docker network ls -q --filter "label=com.docker.compose.project=fugato-$first_id") ]]
docker volume inspect "fugato-${first_id}_data" >/dev/null
[[ $(docker exec "$second_container" cat /var/lib/postgresql/data/marker) == second ]]
"$binary" up --worktree "$source_repo" >/dev/null
first_container=$(docker ps -q --filter "label=com.docker.compose.project=fugato-$first_id")
[[ $(docker exec "$first_container" cat /var/lib/postgresql/data/marker) == first ]]
git -C "$source_repo" worktree remove --force "$second_worktree"
"$binary" destroy --runtime "$second_id" --volumes >/dev/null
[[ -z $(docker ps -aq --filter "label=com.docker.compose.project=fugato-$second_id") ]]
[[ -z $(docker network ls -q --filter "label=com.docker.compose.project=fugato-$second_id") ]]
[[ -z $(docker volume ls -q --filter "label=com.docker.compose.project=fugato-$second_id") ]]
[[ $(docker exec "$first_container" cat /var/lib/postgresql/data/marker) == first ]]
"$binary" destroy --runtime "$first_id" --volumes >/dev/null
[[ -z $(docker volume ls -q --filter "label=com.docker.compose.project=fugato-$first_id") ]]
docker image inspect "$image_id" >/dev/null
docker volume inspect "$unrelated_volume" >/dev/null
docker network inspect "$unrelated_network" >/dev/null
curl --silent --fail -H "Authorization: Bearer $(cat "$FUGATO_STATE_DIR/token")" "http://127.0.0.1:$FUGATO_API_PORT/v1/runtimes" | python3 -c 'import json,sys; assert json.load(sys.stdin) == []'
printf 'destroy passed: data retention/reuse, deleted worktree cleanup, runtime isolation, registry removal, unowned resource preservation, and image preservation\n'
