#!/usr/bin/env python3
"""Exercise installed preparation CLI/MCP in disposable Git worktrees, without Docker."""

import argparse
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.request


CONFIG = """version: 1
prepare:
  files:
    - path: .env.local
    - path: certs
  exclude: [certs/backups/**]
"""
MARKER = "synthetic-prepare-acceptance-value"


def run(argv, cwd=None, env=None):
    return subprocess.run([str(arg) for arg in argv], cwd=cwd, env=env,
                          text=True, capture_output=True, timeout=30)


def require(condition, message):
    if not condition:
        raise AssertionError(message)


def git(cwd, *args):
    result = run(["git", *args], cwd=cwd)
    require(result.returncode == 0, f"Git fixture setup failed: {result.stderr}")


def prepare(binary, target, source, *flags, ok=True):
    result = run([binary, "prepare", "--worktree", target, "--from", source, "--json", *flags])
    require(MARKER not in result.stdout + result.stderr, "Preparation disclosed file content")
    require((result.returncode == 0) == ok, "Unexpected preparation exit status: " + result.stderr)
    report = json.loads(result.stdout)
    require(report["ok"] == ok, "Preparation report disagrees with exit status")
    return report


def assert_materialized(target):
    require((target / ".env.local").read_text() == f"TEST_TOKEN={MARKER}\n", "Environment file missing")
    require((target / "certs/development.pem").read_text() == "synthetic certificate\n", "Recursive file missing")
    require(not (target / "certs/node_modules").exists(), "Default exclusion was copied")
    require(not (target / "certs/backups").exists(), "Project exclusion was copied")
    require((target / ".env.local").stat().st_mode & 0o777 == 0o600, "Environment file is not user-only")


def mcp_call(binary, env, arguments):
    messages = [
        {"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"protocolVersion": "2025-11-25"}},
        {"jsonrpc": "2.0", "method": "notifications/initialized"},
        {"jsonrpc": "2.0", "id": 2, "method": "tools/call",
         "params": {"name": "prepare_worktree", "arguments": arguments}},
    ]
    result = subprocess.run([str(binary)], input="".join(json.dumps(m) + "\n" for m in messages),
                            text=True, capture_output=True, env=env, timeout=30)
    require(result.returncode == 0, "MCP process failed: " + result.stderr)
    require(MARKER not in result.stdout + result.stderr, "MCP disclosed file content")
    responses = [json.loads(line) for line in result.stdout.splitlines() if line.strip()]
    reply = next(item for item in responses if item.get("id") == 2)
    require("error" not in reply, "MCP protocol error")
    return reply["result"]


def exercise_mcp(cli, mcp, root, source):
    # Reserve distinct ports until immediately before starting the isolated daemon.
    api = socket.socket()
    proxy = socket.socket()
    api.bind(("127.0.0.1", 0))
    proxy.bind(("127.0.0.1", 0))
    api_port, proxy_port = api.getsockname()[1], proxy.getsockname()[1]
    state = root / "state"
    env = {**os.environ, "FUGATO_STATE_DIR": str(state), "FUGATO_API_PORT": str(api_port),
           "FUGATO_PROXY_PORT": str(proxy_port), "FUGATO_API_URL": f"http://127.0.0.1:{api_port}",
           "FUGATO_API_TOKEN_FILE": str(state / "token")}
    target = root / "mcp-target"
    git(source, "worktree", "add", "-q", "--detach", str(target), "HEAD")
    api.close()
    proxy.close()
    with (root / "daemon.log").open("w") as log:
        daemon = subprocess.Popen([str(cli), "daemon"], env=env, stdout=log, stderr=log)
        try:
            for _ in range(100):
                if daemon.poll() is not None:
                    raise AssertionError("Isolated daemon exited before readiness")
                probe = run([mcp, "--check"], env=env)
                if probe.returncode == 0:
                    break
                time.sleep(0.1)
            else:
                raise AssertionError("Isolated daemon did not become ready")

            request_body = json.dumps({"path": str(target), "from": str(source), "dry_run": False}).encode()
            request = urllib.request.Request(env["FUGATO_API_URL"] + "/v1/agent/prepare", data=request_body,
                                             headers={"Content-Type": "application/json"})
            try:
                urllib.request.urlopen(request, timeout=5)
                raise AssertionError("Unauthenticated preparation was accepted")
            except urllib.error.HTTPError as error:
                require(error.code == 401, "Expected authentication rejection")
            require(not (target / ".env.local").exists(), "Unauthenticated request copied input")

            args = {"path": str(target), "from": str(source), "dry_run": True}
            reply = mcp_call(mcp, env, args)
            require(not reply.get("isError") and reply["structuredContent"]["ok"], "MCP dry run failed")
            require(not (target / ".env.local").exists(), "MCP dry run mutated target")
            args["dry_run"] = False
            reply = mcp_call(mcp, env, args)
            require(not reply.get("isError") and reply["structuredContent"]["ok"], "MCP apply failed")
            assert_materialized(target)
            # No runtime registration is necessary for preparation.
            token = (state / "token").read_text().strip()
            request = urllib.request.Request(env["FUGATO_API_URL"] + "/v1/agent/runtimes",
                                             headers={"Authorization": "Bearer " + token})
            with urllib.request.urlopen(request, timeout=5) as response:
                runtimes = json.load(response)
            require(runtimes == [], "Preparation unexpectedly registered a runtime")
            print("PASS authenticated MCP prepares an unregistered worktree; unauthenticated writes rejected")
        finally:
            daemon.terminate()
            try:
                daemon.wait(timeout=15)
            except subprocess.TimeoutExpired:
                daemon.kill()
                daemon.wait()


def exercise(cli, mcp, root):
    source, target = root / "source", root / "target"
    source.mkdir()
    (source / ".fugato.yaml").write_text(CONFIG)
    (source / ".gitignore").write_text(".env.local\ncerts/\n")
    git(source, "init", "-q")
    git(source, "add", ".")
    git(source, "-c", "user.name=Preparation Acceptance", "-c", "user.email=test@localhost",
        "commit", "-qm", "Preparation fixture")
    git(source, "worktree", "add", "-q", "--detach", str(target), "HEAD")
    prepare(cli, target, source, "--dry-run", ok=False)
    require(not (target / "certs").exists(), "Blocked plan created directories")
    (source / ".env.local").write_text(f"TEST_TOKEN={MARKER}\n")
    for path, content in [("certs/development.pem", "synthetic certificate\n"),
                          ("certs/node_modules/dependency.js", "must be excluded\n"),
                          ("certs/backups/old.pem", "must be excluded\n")]:
        file = source / path
        file.parent.mkdir(parents=True, exist_ok=True)
        file.write_text(content)
    prepare(cli, target, source, "--dry-run")
    require(not (target / ".env.local").exists() and not (target / "certs").exists(), "Dry run mutated target")
    prepare(cli, target, source)
    assert_materialized(target)
    (target / ".env.local").write_text("USER_EDIT=preserved\n")
    prepare(cli, target, source)
    require((target / ".env.local").read_text() == "USER_EDIT=preserved\n", "Existing file overwritten")
    print("PASS CLI preflight, dry run, recursive copying, filters, private permissions, and existing-file preservation")

    # Explicit project filtering can be changed without rebuilding Fugato.
    config = target / ".fugato.yaml"
    config.write_text(CONFIG + "  allow: [certs/node_modules/**]\n")
    prepare(cli, target, source)
    require((target / "certs/node_modules/dependency.js").exists(), "Default exception not honored")
    require(not (target / "certs/backups").exists(), "Exception bypassed project exclusion")
    config.write_text("version: 1\nprepare:\n  files: [{path: .git/config}]\n")
    blocked = run([cli, "prepare", "--worktree", target, "--from", source, "--json"])
    require(blocked.returncode != 0, "Git metadata declaration accepted")
    config.write_text(CONFIG)
    (source / "certs/link").symlink_to(source / ".env.local")
    prepare(cli, target, source, "--dry-run", ok=False)
    require(not (target / "certs/link").exists(), "Symlink copied")
    (source / "certs/link").unlink()
    print("PASS explicit default exception, protected Git metadata, and symlink rejection")
    if mcp:
        exercise_mcp(cli, mcp, root, source)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--mcp-binary", type=Path)
    parser.add_argument("--keep-fixture", action="store_true", help="retain synthetic files for inspection")
    args = parser.parse_args()
    root = Path(tempfile.mkdtemp(prefix="fugato-prepare-acceptance-"))
    try:
        exercise(args.binary.resolve(), args.mcp_binary.resolve() if args.mcp_binary else None, root)
        print("PASS preparation acceptance")
    finally:
        if args.keep_fixture:
            print("Synthetic fixture: " + str(root))
        else:
            shutil.rmtree(root)


if __name__ == "__main__":
    main()
