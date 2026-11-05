"""Compose, browser, MCP, worktree, and port integration scenarios."""
import concurrent.futures
import json
from pathlib import Path
import socket
import time

import pytest

from .harness import check, wait_for
from .fixtures import EXAMPLE, WEB_ENVIRONMENT, SERVER, WEB_ONLY

pytestmark = [pytest.mark.compose, pytest.mark.docker]

@pytest.mark.mcp
def test_sample_worktrees(acceptance):
    harness = acceptance
    repo = harness.repository("sample", example=True)
    first, second = (harness.worktree(repo, name) for name in ("sample-a", "sample-b"))
    fresh_start = time.monotonic()
    with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
        first_result, second_result = pool.map(harness.up, (first, second))
    fresh_pair_seconds = time.monotonic() - fresh_start
    a_id, a_url = first_result
    b_id, b_url = second_result
    check(a_id != b_id and a_url != b_url, "worktrees shared runtime identity or URL")
    for name, url in (("a", a_url), ("b", b_url)):
        check(harness.preview(url, "health")["ok"], f"{name} unhealthy")
        harness.preview(url, "api/todos", {"title": f"acceptance-{name}"}, expected=201)
    for name, url in (("a", a_url), ("b", b_url)):
        check([item["title"] for item in harness.preview(url, "api/todos")] == [f"acceptance-{name}"],
              f"{name} database was not isolated")
    def resources(runtime_id):
        containers = harness.containers(runtime_id)
        volumes = {mount["Name"] for item in containers for mount in item["Mounts"] if mount["Type"] == "volume"}
        networks = {net["NetworkID"] for item in containers for net in item["NetworkSettings"]["Networks"].values()}
        return containers, volumes, networks
    a_items, a_volumes, a_networks = resources(a_id)
    b_items, b_volumes, b_networks = resources(b_id)
    check(a_volumes and b_volumes and a_volumes.isdisjoint(b_volumes), "database volumes overlapped")
    check(a_networks.isdisjoint(b_networks), "Compose networks overlapped")
    b_containers = {item["Id"] for item in b_items}
    harness.mark("two worktrees: distinct URLs, data, volumes and networks")

    harness.down(first)
    harness.preview(a_url, "health", expected=404)
    check(harness.preview(b_url, "health")["ok"], "B failed while A was stopped")
    (first / ".fugato.yaml").unlink()
    doctor = harness.register_project(first)
    check(doctor.returncode == 0 and f"Runtime ID: {a_id}" in doctor.stdout,
          "no-config discovery changed identity or failed")
    (first / ".fugato.yaml").write_text(WEB_ENVIRONMENT)
    restart_start = time.monotonic()
    check(harness.up(first) == first_result, "restart changed A's identity or URL")
    restart_seconds = time.monotonic() - restart_start
    for name, url in (("a", a_url), ("b", b_url)):
        check([item["title"] for item in harness.preview(url, "api/todos")] == [f"acceptance-{name}"],
              f"{name} lost data after restart")
    check({item["Id"] for item in harness.containers(b_id)} == b_containers, "B containers changed")
    check(f"Runtime ID: {a_id}" in harness.register_project(first).stdout, "minimal config changed identity")
    status, issue = harness.api(f"/v1/agent/runtimes/{a_id}/verify/browser", {"path": "/", "endpoint": "website"})
    check(status >= 400 and "unconfigured" in json.dumps(issue), "unselected browser appeared configured")
    harness.mark("configured and absent config retain runtime identity; explicit restart retains data")
    harness.mark("fresh worktree pair and retained-data restart timed",
                 fresh_pair_seconds=round(fresh_pair_seconds, 3),
                 restart_seconds=round(restart_seconds, 3),
                 base_images_preexisting=harness.base_images_preexisting)

    before_a = harness.preview(a_url, "dev/version")["version"]
    before_b = harness.preview(b_url, "dev/version")["version"]
    with (first / "src/public/style.css").open("a") as css:
        css.write("\n/* Fugato acceptance */\n")
    wait_for(lambda: harness.preview(a_url, "dev/version")["version"] != before_a, 20,
             "CSS edit did not propagate to A")
    check(harness.preview(b_url, "dev/version")["version"] == before_b, "A source edit affected B")
    with (first / "src/server.js").open("a") as js:
        js.write("\n// Fugato acceptance watcher trigger\n")
    wait_for(lambda: harness.preview(a_url, "dev/version")["version"].split(":")[0] != before_a.split(":")[0],
             30, "Node file watcher did not restart A")
    check(harness.preview(b_url, "dev/version")["version"] == before_b, "A watcher affected B")
    harness.mark("CSS propagation and server file watching stay in one worktree")

    for path in (first, second):
        logs = harness.run([harness.binary, "logs", "--worktree", path]).stdout
        check("To-do app listening" in logs, f"no successful application logs for {path}")
    tools = {item["name"] for item in harness.mcp()["tools"]}
    check({"list_runtimes", "verify_http", "verify_browser", "run_command", "stop_runtime", "start_runtime"} <= tools,
          f"MCP tools missing: {tools}")
    listing = harness.mcp("list_runtimes")["structuredContent"]["runtimes"]
    check({a_id, b_id} <= {item["id"] for item in listing}, "MCP lost a registered worktree")
    verified = harness.mcp("verify_http", {"runtime_id": b_id, "endpoint": "website", "path": "/health"})
    check(not verified["isError"] and verified["structuredContent"]["ok"], "MCP HTTP verification failed")
    command = {"runtime_id": b_id, "service": "web", "argv": ["node", "--version"]}
    check(harness.mcp("run_command", command)["isError"], "MCP command ran without a policy")
    policy = harness.root / "state/command-policy.json"
    policy.write_text(json.dumps({"version": 1, "runtimes": {b_id: {"web": [["node", "--version"]]}}}) + "\n")
    policy.chmod(0o600)
    allowed = harness.mcp("run_command", command)
    check(not allowed["isError"] and allowed["structuredContent"]["exit_code"] == 0
          and allowed["structuredContent"]["stdout"].startswith("v"), "MCP policy did not allow selected command")
    check(harness.mcp("run_command", command | {"argv": ["node", "--help"]})["isError"],
          "MCP policy allowed another argv")
    check(harness.mcp("run_command", command | {"runtime_id": a_id})["isError"],
          "MCP policy allowed another runtime")
    harness.mark("MCP stdio handshake, scoped worktrees, HTTP verification and command policy")
    if not harness.skip_browser:
        harness.run(["npm", "ci", "--ignore-scripts", "--no-audit", "--no-fund"], cwd=second, timeout=240)
        status, evidence = harness.api(f"/v1/agent/runtimes/{b_id}/verify/browser",
                                       {"path": "/", "endpoint": "website", "timeout_ms": 60000})
        check(status == 200 and evidence.get("ok") and evidence.get("screenshot_path"),
              f"configured browser failed: {status} {evidence}")
        check(Path(evidence["screenshot_path"]).exists(), "browser screenshot missing")
        browser = harness.run(["docker", "compose", "-p", f"fugato-{b_id}", "-f", second / "compose.yaml",
                               "--profile", "test", "run", "--no-deps", "--rm", "-T", "browser"],
                              cwd=second, timeout=180)
        check("Browser create, reload, complete, and delete: PASS" in browser.stdout,
              "browser interaction did not complete")
        harness.mark("browser API and Playwright create/reload/complete/delete")
        clean = harness.mcp("verify_browser", {"runtime_id": b_id, "endpoint": "website", "path": "/"})
        check(not clean["isError"] and clean["structuredContent"]["ok"]
              and Path(clean["structuredContent"]["screenshot_path"]).exists(),
              f"MCP clean browser verification failed: {clean}")
        app_script = second / "src/public/app.js"
        original = app_script.read_text()
        try:
            app_script.write_text(original + "\nthrow new Error('Fugato acceptance page error');\n")
            broken = harness.mcp("verify_browser", {"runtime_id": b_id, "endpoint": "website", "path": "/"})
            check(not broken["isError"] and not broken["structuredContent"]["ok"]
                  and any(issue["kind"] == "page" for issue in broken["structuredContent"]["issues"]),
                  f"MCP browser did not report page error: {broken}")
        finally:
            app_script.write_text(original)
        repaired = harness.mcp("verify_browser", {"runtime_id": b_id, "endpoint": "website", "path": "/"})
        check(not repaired["isError"] and repaired["structuredContent"]["ok"],
              f"MCP browser did not recover after fix: {repaired}")
        harness.mark("MCP browser clean/fail/fix verification and screenshot")

    elapsed = harness.stop_daemon()
    check(elapsed < 11, f"daemon shutdown exceeded its budget: {elapsed:.2f}s")
    harness.start_daemon()
    wait_for(lambda: all(item.get("observed") == "running" for item in harness.api("/v1/runtimes")[1]),
             20, "restart reconciliation did not restore both runtimes")
    check([item["title"] for item in harness.preview(a_url, "api/todos")] == ["acceptance-a"],
          "A data unavailable after daemon restart")
    check([item["title"] for item in harness.preview(b_url, "api/todos")] == ["acceptance-b"],
          "B data unavailable after daemon restart")
    harness.mark("daemon restart reconciles both runtimes", shutdown_seconds=round(elapsed, 3))
    stopped = harness.mcp("stop_runtime", {"runtime_id": a_id})
    check(not stopped["isError"], f"MCP stop failed: {stopped}")
    harness.preview(a_url, "health", expected=404)
    check(harness.preview(b_url, "health")["ok"], "MCP stop affected the other runtime")
    started = harness.mcp("start_runtime", {"runtime_id": a_id})
    check(not started["isError"] and started["structuredContent"]["preview_url"] == a_url,
          f"MCP restart changed preview identity: {started}")
    wait_for(lambda: harness.preview(a_url, "health")["ok"], 60, "MCP restart preview did not become ready")
    check([item["title"] for item in harness.preview(a_url, "api/todos")] == ["acceptance-a"],
          "MCP restart lost retained database data")
    harness.mark("MCP stop/restart retains data and leaves second runtime healthy")


def test_fixed_ports(acceptance):
    harness = acceptance
    files = (EXAMPLE / "compose.yaml").read_text()
    check('"127.0.0.1::3000"' in files, "example web binding changed")
    files = files.replace('"127.0.0.1::3000"', '"3000:3000"')
    files = files.replace("  db:\n    image:", '  db:\n    ports: ["5432:5432"]\n    image:')
    check('ports: ["5432:5432"]' in files, "example db structure changed")
    repo = harness.repository("fixed", {"compose.yaml": files,
                                        ".fugato.yaml": WEB_ENVIRONMENT}, example=True)
    first, second = (harness.worktree(repo, name) for name in ("fixed-a", "fixed-b"))
    for path in (first, second):
        doctor = harness.register_project(path)
        check(doctor.returncode == 0 and all(
            any("PORT_LAN_BINDING_CHANGED" in line and subject in line for line in doctor.stdout.splitlines())
            for subject in ("web:3000", "db:5432")),
              f"doctor missed per-service changed LAN reachability: {doctor.stdout}")
    with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
        first_result, second_result = pool.map(harness.up, (first, second))
    first_ports, second_ports = harness.ports(first), harness.ports(second)
    expected = {("web", 3000, "tcp"), ("db", 5432, "tcp")}
    bound = set()
    for path, output, rows in ((first, first_result, first_ports), (second, second_result, second_ports)):
        check({row[:3] for row in rows} == expected, f"wrong remapped ports: {rows}")
        for _, _, _, host, port in rows:
            check(host == "127.0.0.1" and port not in (3000, 5432) and port not in bound,
                  f"fixed port leaked or collided: {rows}")
            bound.add(port)
        check(harness.preview(output[1], "health")["ok"], f"fixed project unhealthy: {path}")
        db_host, db_port = next((host, port) for svc, _, _, host, port in rows if svc == "db")
        with socket.create_connection((db_host, db_port), timeout=5):
            pass
    check(first_result[0] != second_result[0], "fixed projects shared identity")
    harness.mark("two fixed-port worktrees remap web and PostgreSQL without collision")
    a_id, a_url = first_result
    b_id, b_url = second_result
    harness.preview(a_url, "api/todos", {"title": "fixed-first"}, expected=201)
    check(harness.preview(b_url, "api/todos") == [], "fixed-port databases shared data")
    harness.down(first)
    stopped = harness.run([harness.binary, "ports", "--worktree", first], check_exit=False)
    check(stopped.returncode != 0, "stopped fixed-port runtime reported active ports")
    check(harness.up(first) == (a_id, a_url), "fixed-port restart changed identity or preview")
    check(any(item["title"] == "fixed-first" for item in harness.preview(a_url, "api/todos")),
          "fixed-port restart lost retained database data")
    check(harness.preview(b_url, "health")["ok"], "fixed-port restart affected second runtime")
    harness.mark("fixed-port databases remain isolated and restart retains data")



def test_web_only_and_monorepo(acceptance):
    harness = acceptance
    repo = harness.repository("web-only", {"compose.yaml": WEB_ONLY, "server.js": SERVER, ".fugato.yaml": WEB_ENVIRONMENT})
    path = harness.worktree(repo, "web-only")
    doctor = harness.register_project(path)
    check(doctor.returncode == 0, f"explicit web project failed setup: {doctor.stdout}")
    runtime_id, url = harness.up(path)
    check(harness.preview(url, "health")["ok"], "web-only preview unhealthy")
    status, issue = harness.api(f"/v1/agent/runtimes/{runtime_id}/verify/browser", {"path": "/", "endpoint": "website"})
    check(status >= 400 and "unconfigured" in json.dumps(issue), "browser capability incorrectly advertised")
    harness.mark("web-only exposed port and optional database/browser")

    dockerfile = "FROM node:24-alpine\nWORKDIR /app\nCOPY server.js .\nCMD [\"node\",\"server.js\"]\n"
    compose = """services:
  web:
    build: ../src
    expose: ["3000"]
    volumes: ["../src/server.js:/app/server.js:ro"]
"""
    repo = harness.repository("monorepo", {"apps/web/compose.yaml": compose,
                                            "apps/web/.fugato.yaml": WEB_ENVIRONMENT,
                                            "apps/src/Dockerfile": dockerfile,
                                            "apps/src/server.js": SERVER})
    path = harness.worktree(repo, "monorepo", "apps/web")
    doctor = harness.register_project(path)
    check(doctor.returncode == 0, f"sibling source/build rejected: {doctor.stdout}")
    _, url = harness.up(path)
    check(harness.preview(url, "health")["ok"], "monorepo preview unhealthy")
    harness.mark("monorepo sibling source and build paths inside worktree")


def assert_source_ports(harness, path, runtime_id, expected, name):
    rows = harness.ports(path)
    check({(service, container) for service, container, _, _, _ in rows} == expected,
          f"{name} lost source Compose port publications: {rows}")
    check(all(protocol == "tcp" and host == "127.0.0.1" and port not in (3000, 3001)
              for _, _, protocol, host, port in rows), f"{name} source ports were not remapped: {rows}")
    status, bindings = harness.api(f"/v1/runtimes/{runtime_id}/ports")
    check(status == 200 and all(not item.get("name") and not item.get("browser") and not item.get("url")
                                for item in bindings), f"{name} inferred named/browser endpoints: {bindings}")


def test_ambiguity_and_rejections(acceptance):
    harness = acceptance
    cases = [
        ("two services", 'services:\n  web:\n    image: node:24-alpine\n    ports: ["3000:3000"]\n  other:\n    image: node:24-alpine\n    ports: ["3001:3001"]\n', {"web", "other"}),
        ("two ports", 'services:\n  web:\n    image: node:24-alpine\n    ports: ["3000:3000", "3001:3001"]\n', {"web"}),
        ("no port", 'services:\n  web:\n    image: node:24-alpine\n', {"web"}),
    ]
    for index, (name, compose, services) in enumerate(cases):
        compose = compose.replace("image: node:24-alpine\n", "image: node:24-alpine\n    command: ['node', '-e', 'setInterval(()=>{},1000)']\n")
        repo = harness.repository(f"discovery-{index}", {"compose.yaml": compose})
        path = harness.worktree(repo, f"discovery-{index}")
        doctor = harness.register_project(path)
        check(doctor.returncode == 0, f"{name} setup unexpectedly required a browser: {doctor.stdout}")
        runtime_id, url = harness.up(path)
        check(not url, f"{name} inferred a browser preview")
        expected_ports = ({("web", 3000), ("other", 3001)} if name == "two services" else
                          {("web", 3000), ("web", 3001)} if name == "two ports" else set())
        assert_source_ports(harness, path, runtime_id, expected_ports, name)
        containers = harness.containers(runtime_id)
        check({item["Config"]["Labels"]["com.docker.compose.service"] for item in containers} == services,
              f"{name} did not start all unprofiled Compose roots")
        harness.down(path)
        init = harness.run([harness.binary, "init", "--worktree", path, "--service", "web"], check_exit=False)
        check(init.returncode == 0 and (path / ".fugato.yaml").exists(),
              f"init could not select {name}: {init.stdout} {init.stderr}")
        check(harness.register_project(path).returncode == 0, f"init output did not satisfy doctor: {name}")
        selected_id, selected_url = harness.up(path)
        check(selected_id == runtime_id and not selected_url,
              f"init changed identity or inferred preview for {name}")
        assert_source_ports(harness, path, runtime_id,
                            {item for item in expected_ports if item[0] == "web"}, name)
        check({item["Config"]["Labels"]["com.docker.compose.service"] for item in harness.containers(runtime_id)} == {"web"},
              f"explicit startup selection did not select only web for {name}")
        harness.down(path)
    harness.mark("services and ports need no browser choice; init records explicit startup roots")

    explicit = WEB_ENVIRONMENT
    rejection_cases = [
        ("host-network", "network_mode: host\n    expose: ['3000']", "COMPOSE_HOST_NETWORK", "network_mode"),
        ("external-volume", "expose: ['3000']\n    volumes: ['shared:/data']\nvolumes:\n  shared:\n    external: true", "COMPOSE_EXTERNAL_VOLUME", "external"),
        ("duplicate-port", "ports: ['3000:3000', '3001:3000']", "PORT_DUPLICATE", "more than once"),
        ("outside-mount", "expose: ['3000']\n    volumes: ['/etc:/outside:ro']", "COMPOSE_OUTSIDE_WORKTREE", "outside"),
    ]
    for name, service, code, fragment in rejection_cases:
        compose = "services:\n  web:\n    image: node:24-alpine\n    " + service + "\n"
        repo = harness.repository(name, {"compose.yaml": compose, ".fugato.yaml": explicit})
        harness.expect_rejected(harness.worktree(repo, name), code, fragment)
    harness.mark("host network, external volume, duplicate ports and outside mount rejected before start")


def test_range_and_udp(acceptance):
    harness = acceptance
    explicit = WEB_ENVIRONMENT
    cases = [
        ("range", "ports: ['3000-3001:3000-3001']", {(3000, "tcp"), (3001, "tcp")}),
        ("udp", "ports: ['3000:3000', '5353:5353/udp']", {(3000, "tcp"), (5353, "udp")}),
    ]
    for name, publish, expected in cases:
        compose = ("services:\n  web:\n    image: node:24-alpine\n    working_dir: /app\n"
                   "    command: ['node', 'server.js']\n    volumes: ['./server.js:/app/server.js:ro']\n    "
                   + publish + "\n")
        repo = harness.repository(name, {"compose.yaml": compose, "server.js": SERVER,
                                         ".fugato.yaml": explicit})
        path = harness.worktree(repo, name)
        doctor = harness.register_project(path)
        check(doctor.returncode == 0, f"{name} doctor failed: {doctor.stdout}")
        _, url = harness.up(path)
        check(harness.preview(url, "health")["ok"], f"{name} preview unhealthy")
        rows = harness.ports(path)
        check({(container, protocol) for _, container, protocol, _, _ in rows} == expected,
              f"{name} missing remapped ports: {rows}")
        check(all(host == "127.0.0.1" and port not in (3000, 3001, 5353)
                  for _, _, _, host, port in rows), f"{name} did not remap to ephemeral loopback: {rows}")
        harness.down(path)
        stopped = harness.run([harness.binary, "ports", "--worktree", path], check_exit=False)
        check(stopped.returncode != 0 and "runtime is not running" in stopped.stderr,
              f"{name} reported active ports after down: {stopped.stdout} {stopped.stderr}")
    harness.mark("port range and UDP remapped to loopback; stopped ports absent")
