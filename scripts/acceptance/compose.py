#!/usr/bin/env python3
"""Run Fugato's disposable Docker acceptance matrix on macOS or Linux.

Requires Python 3.10+, Git, Docker Engine, Compose, and either the Go toolchain
from packages/fugato/go.mod or --binary. All Docker resources are selected by
the unique runtime IDs created in this run. The script preserves its JSON
evidence and logs under a temporary dir.
"""

import argparse
import concurrent.futures
import hashlib
import http.client
import json
import os
from pathlib import Path
import re
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import time
import traceback
from urllib.parse import urlsplit


SOURCE = Path(__file__).resolve().parents[2]
EXAMPLE = SOURCE / "examples/example_2"


def check(condition, message):
    if not condition:
        raise AssertionError(message)


def available_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


class Acceptance:
    def __init__(self, args):
        self.root = Path(args.artifacts or tempfile.mkdtemp(prefix="fugato-acceptance-"))
        self.root.mkdir(parents=True, exist_ok=True)
        check(not list(self.root.iterdir()), f"artifact directory is not empty: {self.root}")
        self.binary = self.root / "fugato"
        self.mcp_binary = self.root / "fugato-mcp"
        self.external_binary = args.binary
        self.external_mcp_binary = args.mcp_binary
        self.needs_mcp = args.only in ("all", "sample")
        self.env = os.environ.copy()
        self.api_port, self.proxy_port = available_port(), available_port()
        while self.proxy_port == self.api_port:
            self.proxy_port = available_port()
        self.env.update(FUGATO_STATE_DIR=str(self.root / "state"),
                        FUGATO_API_PORT=str(self.api_port), FUGATO_PROXY_PORT=str(self.proxy_port))
        self.log = (self.root / "commands.log").open("w")
        self.daemon = None
        self.followers = []
        self.worktrees = []
        self.projects = {}
        self.checks = []
        self.skip_browser = args.skip_browser
        self.before_containers = set()
        self.before_volumes = set()
        self.base_images_preexisting = {}

    def run(self, argv, *, cwd=None, timeout=120, check_exit=True, env=None):
        argv = [str(arg) for arg in argv]
        result = subprocess.run(argv, cwd=cwd, env=env or self.env, text=True,
                                stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=timeout)
        self.log.write(json.dumps(argv) + "\n" + result.stdout + result.stderr + "\n")
        self.log.flush()
        if check_exit and result.returncode:
            raise AssertionError(f"{argv[:4]} exited {result.returncode}: {(result.stdout + result.stderr)[-2000:]}")
        return result

    def mark(self, title, **evidence):
        item = {"check": title, **evidence}
        self.checks.append(item)
        (self.root / "results.json").write_text(json.dumps(self.checks, indent=2) + "\n")
        print("PASS " + title, flush=True)

    def prepare(self):
        self.run(["docker", "info", "--format", "{{.ServerVersion}}"])
        self.run(["docker", "compose", "version"])
        self.before_containers = set(self.run(["docker", "ps", "-q"]).stdout.split())
        self.before_volumes = set(self.run(["docker", "volume", "ls", "-q"]).stdout.split())
        for name in ("node:20-alpine", "postgres:17-alpine"):
            self.base_images_preexisting[name] = (
                self.run(["docker", "image", "inspect", name], check_exit=False).returncode == 0)
        if self.external_binary:
            supplied = Path(self.external_binary).expanduser().resolve(strict=True)
            check(supplied.is_file(), f"binary is not a regular file: {supplied}")
            shutil.copy2(supplied, self.binary)
        else:
            self.run(["go", "build", "-o", self.binary, "./cmd/fugato"],
                     cwd=SOURCE / "packages/fugato", timeout=180)
        if self.needs_mcp:
            if self.external_binary or self.external_mcp_binary:
                source = self.external_mcp_binary or str(supplied.with_name("fugato-mcp"))
                supplied_mcp = Path(source).expanduser().resolve(strict=True)
                check(supplied_mcp.is_file(), f"MCP binary is not a regular file: {supplied_mcp}")
                shutil.copy2(supplied_mcp, self.mcp_binary)
            else:
                self.run(["go", "build", "-o", self.mcp_binary, "./cmd/fugato-mcp"],
                         cwd=SOURCE / "packages/fugato", timeout=180)
        git_commit = self.run(["git", "rev-parse", "HEAD"], cwd=SOURCE, check_exit=False)
        evidence = {
            "platform": self.run(["uname", "-srm"]).stdout.strip(),
            "go": self.run(["go", "version"]).stdout.strip() if shutil.which("go") else "unavailable (supplied binary)",
            "docker": self.run(["docker", "info", "--format", "{{.ServerVersion}}"]).stdout.strip(),
            "compose": self.run(["docker", "compose", "version", "--short"]).stdout.strip(),
            "binary_sha256": hashlib.sha256(self.binary.read_bytes()).hexdigest(),
            "mcp_binary_sha256": hashlib.sha256(self.mcp_binary.read_bytes()).hexdigest() if self.needs_mcp else None,
            "git_commit": git_commit.stdout.strip() if git_commit.returncode == 0 else "source copy without Git metadata",
            "base_images_preexisting": self.base_images_preexisting,
            "api_port": self.api_port, "proxy_port": self.proxy_port,
        }
        (self.root / "environment.json").write_text(json.dumps(evidence, indent=2) + "\n")
        self.mark("toolchain and isolated daemon", **evidence)
        self.start_daemon()
        check(self.api("/v1/health", auth=False)[0] == 401, "unauthenticated API accepted")

    def start_daemon(self):
        output = (self.root / "daemon.log").open("ab")
        self.daemon = subprocess.Popen([str(self.binary), "daemon"], env=self.env,
                                       stdout=output, stderr=output)
        output.close()
        deadline = time.monotonic() + 15
        while time.monotonic() < deadline:
            if self.daemon.poll() is not None:
                raise AssertionError(f"daemon exited: {(self.root / 'daemon.log').read_text()[-2000:]}")
            try:
                if self.api("/v1/health")[0] == 200:
                    return
            except (OSError, http.client.HTTPException):
                pass
            time.sleep(0.1)
        raise AssertionError("daemon did not become healthy")

    def stop_daemon(self):
        if self.daemon is None or self.daemon.poll() is not None:
            return 0.0
        start = time.monotonic()
        self.daemon.send_signal(signal.SIGTERM)
        code = self.daemon.wait(timeout=15)
        check(code == 0, f"daemon exited {code}")
        return time.monotonic() - start

    def api(self, path, body=None, method=None, auth=True, timeout=75):
        conn = http.client.HTTPConnection("127.0.0.1", self.api_port, timeout=timeout)
        headers = {"Authorization": "Bearer " + (self.root / "state/token").read_text().strip()} if auth else {}
        if body is not None:
            headers["Content-Type"] = "application/json"
        conn.request(method or ("POST" if body is not None else "GET"), path,
                     json.dumps(body) if body is not None else None, headers)
        response = conn.getresponse()
        status, raw = response.status, response.read()
        conn.close()
        try:
            return status, json.loads(raw)
        except ValueError:
            return status, raw.decode()

    def mcp(self, name=None, arguments=None):
        request = {"jsonrpc": "2.0", "id": 2, "method": "tools/list"} if name is None else {
            "jsonrpc": "2.0", "id": 2, "method": "tools/call",
            "params": {"name": name, "arguments": arguments or {}}}
        messages = [
            {"jsonrpc": "2.0", "id": 1, "method": "initialize",
             "params": {"protocolVersion": "2025-11-25"}},
            {"jsonrpc": "2.0", "method": "notifications/initialized"},
            request,
        ]
        env = self.env | {"FUGATO_API_URL": f"http://127.0.0.1:{self.api_port}",
                          "FUGATO_API_TOKEN_FILE": str(self.root / "state/token")}
        result = subprocess.run([str(self.mcp_binary)], env=env,
                                input="\n".join(json.dumps(item) for item in messages) + "\n",
                                text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=120)
        self.log.write(f"MCP {name or 'tools/list'}: exit={result.returncode}\n"
                       + result.stdout + result.stderr + "\n")
        self.log.flush()
        check(result.returncode == 0, f"MCP {name} exited {result.returncode}: {result.stderr[-1000:]}")
        lines = [json.loads(line) for line in result.stdout.splitlines()]
        check(len(lines) == 2 and lines[0].get("result", {}).get("protocolVersion") == "2025-11-25",
              f"MCP handshake failed: {result.stdout[-1000:]}")
        check(lines[1].get("id") == 2 and not lines[1].get("error"),
              f"MCP {name} protocol failure: {lines[1]}")
        return lines[1]["result"]

    def preview(self, base, path="", body=None, expected=200):
        parsed = urlsplit(base)
        conn = http.client.HTTPConnection("127.0.0.1", parsed.port, timeout=15)
        headers = {"Host": parsed.netloc}
        if body is not None:
            headers["Content-Type"] = "application/json"
        conn.request("POST" if body is not None else "GET", "/" + path.lstrip("/"),
                     json.dumps(body) if body is not None else None, headers)
        response = conn.getresponse()
        status, raw = response.status, response.read()
        conn.close()
        check(status == expected, f"{base}{path}: HTTP {status}: {raw[:300]!r}")
        try:
            return json.loads(raw)
        except ValueError:
            return raw.decode()

    def repository(self, name, files=None, *, example=False):
        repo = self.root / f"{name}-repo"
        if example:
            shutil.copytree(EXAMPLE, repo, ignore=shutil.ignore_patterns("node_modules", ".git", ".DS_Store"))
        else:
            repo.mkdir()
        for rel, content in (files or {}).items():
            path = repo / rel
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(content)
        self.run(["git", "init", "-q", repo])
        self.run(["git", "add", "."], cwd=repo)
        self.run(["git", "-c", "user.name=Fugato Acceptance",
                  "-c", "user.email=acceptance@localhost", "commit", "-qm", name], cwd=repo)
        return repo

    def worktree(self, repo, name, subdir=""):
        root = self.root / name
        self.run(["git", "worktree", "add", "--detach", root, "HEAD"], cwd=repo)
        self.worktrees.append((repo, root))
        path = root / subdir
        self.register_project(path)
        return path

    def register_project(self, path):
        result = self.run([self.binary, "doctor", "--worktree", path], check_exit=False)
        match = re.search(r"Runtime ID: ([a-f0-9]{16})", result.stdout)
        check(match is not None, f"doctor did not identify {path}: {result.stdout} {result.stderr}")
        self.projects[match.group(1)] = path
        return result

    def up(self, path):
        output = self.run([self.binary, "up", "--worktree", path], timeout=360).stdout.strip()
        lines = output.splitlines()
        check(lines and lines[0].split(), f"unexpected up output: {output}")
        runtime_id = lines[0].split()[0]
        check(runtime_id in self.projects, f"unregistered runtime {runtime_id}")
        status, runtime = self.api(f"/v1/runtimes/{runtime_id}")
        check(status == 200 and runtime.get("id") == runtime_id, f"runtime lookup failed: {runtime}")
        url = runtime.get("preview_url") or ""
        if url:
            wait_for(lambda: self.preview(url, "health").get("ok"), 60,
                     f"preview did not become ready: {url}")
        return runtime_id, url

    def ports(self, path):
        output = self.run([self.binary, "ports", "--worktree", path]).stdout
        rows = []
        for line in output.splitlines()[1:]:
            service, container, protocol, host = line.split()
            transport = "tcp" if protocol in ("http", "https") else protocol
            ip, host_port = host.rsplit(":", 1)
            rows.append((service, int(container), transport, ip, int(host_port)))
        return rows

    def containers(self, runtime_id):
        ids = self.run(["docker", "ps", "-aq", "--filter",
                        f"label=com.docker.compose.project=fugato-{runtime_id}"]).stdout.split()
        return json.loads(self.run(["docker", "inspect", *ids]).stdout) if ids else []

    def down(self, path):
        self.run([self.binary, "down", "--worktree", path], timeout=180)

    def expect_rejected(self, path, code, fragment):
        doctor = self.register_project(path)
        check(doctor.returncode != 0 and code in doctor.stdout and fragment in doctor.stdout,
              f"doctor missed {code}/{fragment}: {doctor.stdout} {doctor.stderr}")
        up = self.run([self.binary, "up", "--worktree", path], check_exit=False)
        check(up.returncode != 0, f"up accepted unsupported project {path}")
        match = re.search(r"Runtime ID: ([a-f0-9]{16})", doctor.stdout)
        check(not self.containers(match.group(1)), f"rejected project created containers: {path}")

    def release_running_projects(self):
        """Keep the Linux fixture matrix within the small guest's memory budget."""
        for runtime_id, path in self.projects.items():
            if not self.containers(runtime_id):
                continue
            self.run([self.binary, "down", "--worktree", path], timeout=180)
            result = self.run(["docker", "compose", "-p", f"fugato-{runtime_id}",
                               "-f", path / "compose.yaml", "down", "-v", "--remove-orphans"],
                              cwd=path, timeout=90, check_exit=False)
            check(result.returncode == 0, f"could not release {runtime_id}: {result.stderr[-500:]}")

    def cleanup(self):
        errors = []
        for follower in self.followers:
            if follower.poll() is None:
                follower.send_signal(signal.SIGTERM)
                try:
                    follower.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    follower.kill(); follower.wait()
        try:
            self.stop_daemon()
        except Exception as exc:
            errors.append(f"daemon: {exc}")
            if self.daemon and self.daemon.poll() is None:
                self.daemon.kill(); self.daemon.wait()
        for runtime_id, path in self.projects.items():
            compose = path / "compose.yaml"
            if not compose.exists():
                continue
            try:
                self.run(["docker", "compose", "-p", f"fugato-{runtime_id}",
                          "-f", compose, "down", "-v", "--remove-orphans"],
                         cwd=path, timeout=90, check_exit=False)
                # A failed Compose parse must not leave a project behind.
                ids = self.run(["docker", "ps", "-aq", "--filter",
                                f"label=com.docker.compose.project=fugato-{runtime_id}"]).stdout.split()
                if ids:
                    self.run(["docker", "rm", "-f", "-v", *ids], timeout=60)
                for kind in ("volume", "network"):
                    owned = self.run(["docker", kind, "ls", "-q", "--filter",
                                      f"label=com.docker.compose.project=fugato-{runtime_id}"]).stdout.split()
                    if owned:
                        self.run(["docker", kind, "rm", *owned], timeout=60)
                self.run(["docker", "image", "rm", f"fugato-{runtime_id}-web:latest"], check_exit=False)
            except Exception as exc:
                errors.append(f"fugato-{runtime_id}: {exc}")
        for repo, worktree in self.worktrees:
            try:
                self.run(["git", "worktree", "remove", "--force", worktree], cwd=repo)
            except Exception as exc:
                errors.append(f"worktree {worktree}: {exc}")
        try:
            after_containers = set(self.run(["docker", "ps", "-q"]).stdout.split())
            after_volumes = set(self.run(["docker", "volume", "ls", "-q"]).stdout.split())
            check(self.before_containers <= after_containers, "a pre-existing running container stopped")
            check(self.before_volumes <= after_volumes, "a pre-existing volume disappeared")
            for runtime_id in self.projects:
                label = f"label=com.docker.compose.project=fugato-{runtime_id}"
                for kind in ("ps", "volume", "network"):
                    argv = ["docker", kind, "ls", "-q", "--filter", label] if kind != "ps" else ["docker", "ps", "-aq", "--filter", label]
                    check(not self.run(argv).stdout.strip(), f"leftover {kind} for {runtime_id}")
        except Exception as exc:
            errors.append(f"cleanup verification: {exc}")
        if not errors:
            self.mark("owned resources cleaned; pre-existing Docker resources retained")
        self.log.close()
        return errors


WEB_ENVIRONMENT = """version: 1
start:
  services: [web]
endpoints:
  website:
    service: web
    port: 3000
    protocol: http
    browser: true
"""


SERVER = """const http=require('node:http');
http.createServer((req,res)=>{res.writeHead(200,{'content-type':'application/json'});
res.end(JSON.stringify({ok:true,path:req.url}));}).listen(3000,'0.0.0.0');
"""
WEB_ONLY = """services:
  web:
    image: node:20-alpine
    working_dir: /app
    command: ["node", "server.js"]
    expose: ["3000"]
    volumes: ["./server.js:/app/server.js:ro"]
"""


def sample_worktrees(harness):
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


def wait_for(predicate, seconds, message):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        try:
            if predicate():
                return
        except (OSError, http.client.HTTPException, AssertionError):
            pass
        time.sleep(0.25)
    raise AssertionError(message)


def fixed_ports(harness):
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
        check(doctor.returncode == 0 and "PORT_LAN_BINDING_CHANGED" in doctor.stdout,
              f"doctor missed changed LAN reachability: {doctor.stdout}")
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


def web_only_and_monorepo(harness):
    repo = harness.repository("web-only", {"compose.yaml": WEB_ONLY, "server.js": SERVER, ".fugato.yaml": WEB_ENVIRONMENT})
    path = harness.worktree(repo, "web-only")
    doctor = harness.register_project(path)
    check(doctor.returncode == 0, f"explicit web project failed setup: {doctor.stdout}")
    runtime_id, url = harness.up(path)
    check(harness.preview(url, "health")["ok"], "web-only preview unhealthy")
    status, issue = harness.api(f"/v1/agent/runtimes/{runtime_id}/verify/browser", {"path": "/", "endpoint": "website"})
    check(status >= 400 and "unconfigured" in json.dumps(issue), "browser capability incorrectly advertised")
    harness.mark("web-only exposed port and optional database/browser")

    dockerfile = "FROM node:20-alpine\nWORKDIR /app\nCOPY server.js .\nCMD [\"node\",\"server.js\"]\n"
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


def ambiguity_and_rejections(harness):
    cases = [
        ("two services", 'services:\n  web:\n    image: node:20-alpine\n    ports: ["3000:3000"]\n  other:\n    image: node:20-alpine\n    ports: ["3001:3001"]\n', {"web", "other"}),
        ("two ports", 'services:\n  web:\n    image: node:20-alpine\n    ports: ["3000:3000", "3001:3001"]\n', {"web"}),
        ("no port", 'services:\n  web:\n    image: node:20-alpine\n', {"web"}),
    ]
    for index, (name, compose, services) in enumerate(cases):
        compose = compose.replace("image: node:20-alpine\n", "image: node:20-alpine\n    command: ['node', '-e', 'setInterval(()=>{},1000)']\n")
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
        compose = "services:\n  web:\n    image: node:20-alpine\n    " + service + "\n"
        repo = harness.repository(name, {"compose.yaml": compose, ".fugato.yaml": explicit})
        harness.expect_rejected(harness.worktree(repo, name), code, fragment)
    harness.mark("host network, external volume, duplicate ports and outside mount rejected before start")


def range_and_udp(harness):
    explicit = WEB_ENVIRONMENT
    cases = [
        ("range", "ports: ['3000-3001:3000-3001']", {(3000, "tcp"), (3001, "tcp")}),
        ("udp", "ports: ['3000:3000', '5353:5353/udp']", {(3000, "tcp"), (5353, "udp")}),
    ]
    for name, publish, expected in cases:
        compose = ("services:\n  web:\n    image: node:20-alpine\n    working_dir: /app\n"
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


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--artifacts", help="new or empty directory for logs and JSON results")
    parser.add_argument("--binary", help="use a supplied Fugato binary instead of building with Go")
    parser.add_argument("--mcp-binary", help="use a supplied fugato-mcp binary (defaults to sibling of --binary)")
    parser.add_argument("--skip-browser", action="store_true", help="skip the Playwright image and npm dependency install")
    parser.add_argument("--only", choices=("all", "sample", "fixed", "web", "diagnostics", "ports"),
                        default="all", help="run one fixture group while developing the suite")
    args = parser.parse_args()
    harness = Acceptance(args)
    print(f"Acceptance artifacts: {harness.root}", flush=True)
    failure = None
    try:
        harness.prepare()
        groups = (("sample", sample_worktrees), ("fixed", fixed_ports),
                  ("web", web_only_and_monorepo),
                  ("diagnostics", ambiguity_and_rejections), ("ports", range_and_udp))
        for name, group in groups:
            if args.only in ("all", name):
                group(harness)
                harness.release_running_projects()
    except BaseException as exc:
        failure = repr(exc)
        traceback.print_exc()
    cleanup_errors = harness.cleanup()
    summary = {"passed": failure is None and not cleanup_errors,
               "failure": failure, "cleanup_errors": cleanup_errors,
               "checks": harness.checks}
    (harness.root / "summary.json").write_text(json.dumps(summary, indent=2) + "\n")
    print("PASS complete local Compose acceptance" if summary["passed"] else
          f"FAIL acceptance: {failure}; cleanup: {cleanup_errors}", flush=True)
    return 0 if summary["passed"] else 1


if __name__ == "__main__":
    sys.exit(main())
