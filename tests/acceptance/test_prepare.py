#!/usr/bin/env python3
"""Exercise installed preparation CLI/MCP in disposable Git worktrees, without Docker."""

from functools import partial
import json


import pytest

from .harness import check as require

pytestmark = [pytest.mark.prepare, pytest.mark.mcp]

CONFIG = """version: 1
prepare:
  files:
    - path: .env.local
    - path: certs
  exclude: [certs/backups/**]
"""
MARKER = "synthetic-prepare-acceptance-value"


def git_command(cwd, *args, runner):
    result = runner(["git", *args], cwd=cwd)
    require(result.returncode == 0, f"Git fixture setup failed: {result.stderr}")


def prepare_command(binary, target, source, *flags, ok=True, runner):
    result = runner([binary, "prepare", "--worktree", target, "--from", source, "--json", *flags])
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


def exercise_mcp(harness, source):
    target = harness.root / "mcp-target"
    harness.run(["git", "worktree", "add", "-q", "--detach", target, "HEAD"], cwd=source)
    harness.worktrees.append((source, target))
    harness.start_daemon()
    status, _ = harness.api("/v1/agent/prepare", {"path": str(target), "from": str(source), "dry_run": False}, auth=False)
    require(status == 401, "Unauthenticated preparation was accepted")
    require(not (target / ".env.local").exists(), "Unauthenticated request copied input")
    args = {"path": str(target), "from": str(source), "dry_run": True}
    reply = harness.mcp("prepare_worktree", args)
    require(MARKER not in json.dumps(reply), "MCP disclosed file content")
    require(not reply.get("isError") and reply["structuredContent"]["ok"], "MCP dry run failed")
    require(not (target / ".env.local").exists(), "MCP dry run mutated target")
    args["dry_run"] = False
    reply = harness.mcp("prepare_worktree", args)
    require(MARKER not in json.dumps(reply), "MCP disclosed file content")
    require(not reply.get("isError") and reply["structuredContent"]["ok"], "MCP apply failed")
    assert_materialized(target)
    status, runtimes = harness.api("/v1/agent/runtimes")
    require(status == 200 and runtimes == [], "Preparation unexpectedly registered a runtime")
    harness.mark("authenticated MCP prepares an unregistered worktree; unauthenticated writes rejected")


def test_preparation(acceptance):
    harness = acceptance
    cli, root = harness.binary, harness.root
    harness.forbidden_output = (MARKER,)
    run = partial(harness.run, check_exit=False, timeout=30)
    git = partial(git_command, runner=run)
    prepare = partial(prepare_command, runner=run)
    source, target = root / "source", root / "target"
    source.mkdir()
    (source / ".fugato.yaml").write_text(CONFIG)
    (source / ".gitignore").write_text(".env.local\ncerts/\n")
    git(source, "init", "-q")
    git(source, "add", ".")
    git(source, "-c", "user.name=Preparation Acceptance", "-c", "user.email=test@localhost",
        "commit", "-qm", "Preparation fixture")
    git(source, "worktree", "add", "-q", "--detach", str(target), "HEAD")
    harness.worktrees.append((source, target))
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
    harness.mark("CLI preflight, dry run, recursive copying, filters, private permissions, and existing-file preservation")

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
    harness.mark("explicit default exception, protected Git metadata, and symlink rejection")
    exercise_mcp(harness, source)
