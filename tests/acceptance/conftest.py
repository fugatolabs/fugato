"""Acceptance runner options and lifecycle fixtures shared by every scenario."""
import json
from pathlib import Path
import re
import subprocess
import tempfile
from types import SimpleNamespace

import pytest

from .harness import Acceptance, SOURCE, check


def pytest_addoption(parser):
    group = parser.getgroup("fugato acceptance")
    group.addoption("--binary", help="installed Fugato CLI; defaults to a session build")
    group.addoption("--mcp-binary", help="installed MCP binary; defaults to the CLI's sibling")
    group.addoption("--artifacts", help="parent directory for per-test command logs and JSON evidence")
    group.addoption("--skip-browser", action="store_true", help="omit Playwright checks; does not satisfy full release acceptance")


@pytest.hookimpl(hookwrapper=True)
def pytest_runtest_makereport(item, call):
    outcome = yield
    setattr(item, "report_" + call.when, outcome.get_result())


@pytest.fixture(scope="session")
def binaries(request, tmp_path_factory):
    supplied = request.config.getoption("--binary")
    supplied_mcp = request.config.getoption("--mcp-binary")
    if supplied:
        cli = Path(supplied).expanduser().resolve(strict=True)
        check(cli.is_file(), f"not a regular CLI binary: {cli}")
        mcp = Path(supplied_mcp).expanduser().resolve(strict=True) if supplied_mcp else cli.with_name("fugato-mcp")
    else:
        directory = tmp_path_factory.mktemp("fugato-binaries")
        cli, mcp = directory / "fugato", directory / "fugato-mcp"
        for target, output in (("fugato", cli), ("fugato-mcp", mcp)):
            subprocess.run(["go", "build", "-o", str(output), "./cmd/" + target],
                           cwd=SOURCE / "packages/fugato", check=True, timeout=240)
        if supplied_mcp:
            mcp = Path(supplied_mcp).expanduser().resolve(strict=True)
    return cli, mcp


@pytest.fixture
def acceptance(request, binaries):
    cli, mcp = binaries
    artifact_parent = request.config.getoption("--artifacts")
    if artifact_parent:
        parent = Path(artifact_parent).expanduser().resolve()
        parent.mkdir(parents=True, exist_ok=True)
    else:
        parent = None
    name = re.sub(r"[^a-zA-Z0-9_.-]", "-", request.node.name)
    directory = tempfile.mkdtemp(prefix=name + "-", dir=parent)
    needs_mcp = request.node.get_closest_marker("mcp") is not None
    docker = request.node.get_closest_marker("docker") is not None
    options = SimpleNamespace(artifacts=directory, binary=str(cli), mcp_binary=str(mcp),
                              needs_mcp=needs_mcp, skip_browser=request.config.getoption("--skip-browser"))
    harness = Acceptance(options, docker=docker)
    print(f"Acceptance artifacts: {harness.root}", flush=True)
    failure = None
    try:
        harness.prepare()
        if docker:
            harness.start_daemon()
            check(harness.api("/v1/health", auth=False)[0] == 401, "unauthenticated API accepted")
        yield harness
    except BaseException as error:
        failure = repr(error)
        raise
    finally:
        report = getattr(request.node, "report_call", None)
        if report is not None and report.failed:
            failure = str(report.longrepr)
        cleanup_errors = harness.cleanup()
        summary = {"passed": failure is None and not cleanup_errors,
                   "failure": failure, "cleanup_errors": cleanup_errors, "checks": harness.checks,
                   "browser_checks_skipped": harness.skip_browser}
        (harness.root / "summary.json").write_text(json.dumps(summary, indent=2) + "\n")
        if cleanup_errors:
            pytest.fail("acceptance cleanup failed: " + "; ".join(cleanup_errors))
