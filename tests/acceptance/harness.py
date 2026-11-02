"""Shared isolated daemon, worktree, evidence, and owned-resource fixtures."""
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
import tempfile
import time
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
    def __init__(self, args, *, docker=True):
        self.root = Path(args.artifacts or tempfile.mkdtemp(prefix="fugato-acceptance-"))
        self.root.mkdir(parents=True, exist_ok=True)
        check(not list(self.root.iterdir()), f"artifact directory is not empty: {self.root}")
        self.binary = self.root / "fugato"
        self.mcp_binary = self.root / "fugato-mcp"
        self.external_binary = args.binary
        self.external_mcp_binary = args.mcp_binary
        self.needs_mcp = args.needs_mcp
        self.docker = docker
        self.docker_ready = False
        self.extra_resources = []
        self.forbidden_output = ()
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

    def run(self, argv, *, cwd=None, timeout=120, check_exit=True, env=None, stdin=None):
        argv = [str(arg) for arg in argv]
        result = subprocess.run(argv, cwd=cwd, env=env or self.env, text=True,
                                stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=timeout, input=stdin)
        check(not any(marker in result.stdout + result.stderr for marker in self.forbidden_output),
              "command disclosed protected fixture content")
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
        """Inspect tools and stage executables; discovery tests start the daemon later."""
        if self.docker:
            self.run(["docker", "info", "--format", "{{.ServerVersion}}"])
            self.run(["docker", "compose", "version"])
            self.before_containers = set(self.run(["docker", "ps", "-q"]).stdout.split())
            self.before_volumes = set(self.run(["docker", "volume", "ls", "-q"]).stdout.split())
            for name in ("node:20-alpine", "postgres:17-alpine"):
                self.base_images_preexisting[name] = (
                    self.run(["docker", "image", "inspect", name], check_exit=False).returncode == 0)
            self.docker_ready = True
        supplied = Path(self.external_binary).expanduser().resolve(strict=True)
        check(supplied.is_file(), f"binary is not a regular file: {supplied}")
        shutil.copy2(supplied, self.binary)
        if self.needs_mcp:
            supplied_mcp = Path(self.external_mcp_binary).expanduser().resolve(strict=True)
            check(supplied_mcp.is_file(), f"MCP binary is not a regular file: {supplied_mcp}")
            shutil.copy2(supplied_mcp, self.mcp_binary)
        git_commit = self.run(["git", "rev-parse", "HEAD"], cwd=SOURCE, check_exit=False)
        evidence = {
            "platform": self.run(["uname", "-srm"]).stdout.strip(),
            "go": self.run(["go", "version"]).stdout.strip() if shutil.which("go") else "unavailable (supplied binary)",
            "docker": self.run(["docker", "info", "--format", "{{.ServerVersion}}"]).stdout.strip() if self.docker else None,
            "compose": self.run(["docker", "compose", "version", "--short"]).stdout.strip() if self.docker else None,
            "binary_sha256": hashlib.sha256(self.binary.read_bytes()).hexdigest(),
            "mcp_binary_sha256": hashlib.sha256(self.mcp_binary.read_bytes()).hexdigest() if self.needs_mcp else None,
            "git_commit": git_commit.stdout.strip() if git_commit.returncode == 0 else "source copy without Git metadata",
            "base_images_preexisting": self.base_images_preexisting,
            "api_port": self.api_port, "proxy_port": self.proxy_port,
        }
        (self.root / "environment.json").write_text(json.dumps(evidence, indent=2) + "\n")
        self.mark("toolchain and isolated state", **evidence)


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
        check(not any(marker in result.stdout + result.stderr for marker in self.forbidden_output),
              "MCP disclosed protected fixture content")
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
                for runtime_id in self.projects:
                    try:
                        self.api(f"/v1/runtimes/{runtime_id}/down", {}, timeout=5)
                    except Exception as cleanup_error:
                        errors.append(f"stop {runtime_id}: {cleanup_error}")
                self.daemon.kill(); self.daemon.wait()
        for runtime_id, path in self.projects.items() if self.docker_ready else ():
            compose = path / "compose.yaml"
            try:
                if compose.exists():
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
                for service in ("web", "api"):
                    image = f"fugato-{runtime_id}-{service}:latest"
                    result = self.run(["docker", "image", "rm", image], check_exit=False)
                    if result.returncode and "No such image" not in result.stderr:
                        errors.append(f"owned image {image}: {result.stderr.strip()}")
            except Exception as exc:
                errors.append(f"fugato-{runtime_id}: {exc}")
        for kind, name in self.extra_resources:
            try:
                self.run(["docker", kind, "rm", name])
            except Exception as exc:
                errors.append(f"{kind} {name}: {exc}")
        for repo, worktree in self.worktrees:
            try:
                if worktree.exists():
                    self.run(["git", "worktree", "remove", "--force", worktree], cwd=repo)
            except Exception as exc:
                errors.append(f"worktree {worktree}: {exc}")
        if self.docker_ready:
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
            self.mark("owned resources cleaned; pre-existing resources retained")
        self.log.close()
        return errors



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
