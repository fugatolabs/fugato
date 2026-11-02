#!/usr/bin/env python3
"""Exercise the Next.js -> NestJS -> PostgreSQL Compose example in two worktrees."""

import http.client
import json
from pathlib import Path
import shutil
from urllib.parse import urlsplit

import pytest

from .harness import SOURCE, check, wait_for

pytestmark = [pytest.mark.fullstack, pytest.mark.docker]


EXAMPLE = SOURCE / "examples/example_1"


def fixture_repository(harness):
    repo = harness.root / "next-nest-repo"
    shutil.copytree(EXAMPLE, repo, ignore=shutil.ignore_patterns(".env", "node_modules", ".next", "dist"))
    (repo / ".env").write_text("EXAMPLE_GREETING=Prepared environment greeting\n")
    harness.run(["git", "init", "-q", repo])
    harness.run(["git", "add", "."], cwd=repo)
    harness.run(["git", "-c", "user.name=Fugato Acceptance",
              "-c", "user.email=acceptance@localhost", "commit", "-qm", "next-nest fixture"], cwd=repo)
    return repo

def proxy_request(url, path, *, host=None, body=None, method=None):
    parsed = urlsplit(url)
    conn = http.client.HTTPConnection("127.0.0.1", parsed.port, timeout=20)
    headers = {"Host": host or parsed.netloc}
    if body is not None:
        headers["Content-Type"] = "application/json"
    conn.request(method or ("POST" if body is not None else "GET"), path,
                 json.dumps(body) if body is not None else None, headers)
    response = conn.getresponse()
    status, payload = response.status, response.read()
    conn.close()
    return status, payload


def todos(url):
    status, raw = proxy_request(url, "/api/todos")
    check(status == 200, f"todo read through {url} returned {status}: {raw[:300]!r}")
    return json.loads(raw)


def test_next_nest_postgres(acceptance):
    harness = acceptance
    repo = fixture_repository(harness)
    first, second = (harness.worktree(repo, name) for name in ("next-nest-a", "next-nest-b"))
    for path in (first, second):
        check(not (path / ".env").exists(), "Git unexpectedly copied the ignored environment file")
        preview = harness.run([harness.binary, "prepare", "--worktree", path,
                               "--from", repo, "--dry-run", "--json"])
        check(any(item["path"] == ".env" and item["status"] == "would_copy"
                  for item in json.loads(preview.stdout)["entries"]), "dry run did not plan the ignored file")
        check(not (path / ".env").exists(), "prepare dry run copied the ignored file")
        harness.run([harness.binary, "prepare", "--worktree", path, "--from", repo])
    (first / ".env").write_text("EXAMPLE_GREETING=Worktree alpha environment\n")
    harness.run([harness.binary, "prepare", "--worktree", first, "--from", repo])
    check((first / ".env").read_text() == "EXAMPLE_GREETING=Worktree alpha environment\n",
          "repeated prepare overwrote a worktree environment edit")
    a_id, a_url = harness.up(first)
    b_id, b_url = harness.up(second)
    check(a_id != b_id and a_url != b_url, "worktrees share runtime identity or preview URL")
    check(urlsplit(a_url).port == urlsplit(b_url).port == harness.proxy_port,
          "preview URLs did not use the dedicated proxy port")
    for url, greeting in ((a_url, b"Worktree alpha environment"), (b_url, b"Prepared environment greeting")):
        status, page = proxy_request(url, "/")
        check(status == 200 and greeting in page, "Next page did not display its Nest environment greeting")
    harness.mark("ignored dotenv inputs prepare independently and reach Nest and the Next page")

    host_ports = []
    for path in (first, second):
        harness.run([harness.binary, "check", "ready", "--worktree", path])
        rows = harness.ports(path)
        check(len(rows) == 1 and rows[0][:3] == ("web", 3000, "tcp") and rows[0][3] == "127.0.0.1",
              f"expected only a dynamic loopback web binding: {rows}")
        host_ports.append(rows[0][4])
    check(host_ports[0] != host_ports[1], "worktrees share the published web host port")

    for url, label in ((a_url, "alpha"), (b_url, "beta")):
        status, body = proxy_request(url, "/api/todos", body={"title": label})
        check(status == 201 and json.loads(body)["title"] == label,
              f"Next -> Nest -> PostgreSQL write failed: {status} {body[:300]!r}")
        status, page = proxy_request(url, "/")
        check(status == 200 and label.encode() in page and b"Next.js" in page,
              f"Next page did not render database data: {status} {page[:300]!r}")
    check([item["title"] for item in todos(a_url)] == ["alpha"], "A has unexpected data")
    check([item["title"] for item in todos(b_url)] == ["beta"], "B has unexpected data")
    first_todo = todos(a_url)[0]
    check(first_todo["completed"] is False, "new todo should be incomplete")
    todo_path = f"/api/todos/{first_todo['id']}"
    for completed in (True, False, True):
        status, raw = proxy_request(a_url, todo_path, method="PATCH", body={"completed": completed})
        check(status == 200 and json.loads(raw)["completed"] is completed,
              f"todo completion update failed: {status} {raw[:300]!r}")
    check(todos(b_url)[0]["completed"] is False, "A completion changed B")
    status, raw = proxy_request(a_url, "/api/todos", body={"title": "delete me"})
    check(status == 201, f"delete fixture create failed: {status}")
    deleted_id = json.loads(raw)["id"]
    status, raw = proxy_request(a_url, f"/api/todos/{deleted_id}", method="DELETE")
    check(status == 204 and raw == b"", "delete should return an empty 204")
    check([item["title"] for item in todos(a_url)] == ["alpha"], "deleted todo remained")
    for path, method, body, expected in (
        ("/api/todos", "POST", {"title": "  "}, 400),
        ("/api/todos", "POST", {"title": "x" * 201}, 400),
        ("/api/todos", "POST", {"title": 42}, 400),
        (todo_path, "PATCH", {"completed": "true"}, 400),
        (f"/api/todos/{deleted_id}", "PATCH", {"completed": True}, 404),
        (f"/api/todos/{deleted_id}", "DELETE", None, 404),
        ("/api/todos/not-an-id", "DELETE", None, 400),
        ("/api/todos/2147483648", "DELETE", None, 400),
    ):
        status, raw = proxy_request(a_url, path, method=method, body=body)
        check(status == expected, f"{method} {path}: expected {expected}, got {status}: {raw[:300]!r}")
    harness.mark("todos create, complete, reopen, delete, and reject invalid or missing records")
    status, _ = proxy_request(a_url, "/api/todos", host=f"unknown.localhost:{harness.proxy_port}")
    check(status == 404, f"unknown proxy host routed to a worktree: HTTP {status}")

    def resources(runtime_id):
        containers = harness.containers(runtime_id)
        services = {item["Config"]["Labels"]["com.docker.compose.service"] for item in containers}
        volumes = {mount["Name"] for item in containers for mount in item["Mounts"] if mount["Type"] == "volume"}
        networks = {net["NetworkID"] for item in containers for net in item["NetworkSettings"]["Networks"].values()}
        return containers, services, volumes, networks

    _, a_services, a_volumes, a_networks = resources(a_id)
    b_containers, b_services, b_volumes, b_networks = resources(b_id)
    check(a_services == b_services == {"web", "api", "db"}, "Compose did not start all three services")
    check(a_volumes and b_volumes and a_volumes.isdisjoint(b_volumes), "PostgreSQL volumes overlap")
    check(a_networks.isdisjoint(b_networks), "Compose networks overlap")
    b_container_ids = {item["Id"] for item in b_containers}
    harness.mark("Next page and API route reach Nest/PostgreSQL through two isolated proxy hosts")

    a_container_ids = {item["Id"] for item in harness.containers(a_id)}
    web_page = first / "web/app/page.jsx"
    web_page.write_text(web_page.read_text().replace(
        "Add a task, check it off, and pick up where you left off.",
        "Next source reload reached this page."))
    wait_for(lambda: b"Next source reload reached this page." in proxy_request(a_url, "/")[1],
             60, "Next did not reload the mounted page source")
    controller = first / "api/src/todos.controller.ts"
    controller_source = controller.read_text()
    controller.write_text(controller_source.replace(
        "return this.todos.health();", "return this.todos.health().then(data => ({ ...data, reload: 'nest-source-reloaded' }));"))
    wait_for(lambda: b"nest-source-reloaded" in proxy_request(a_url, "/health")[1],
             60, "Nest did not automatically reload mounted controller source")
    controller.write_text(controller_source.replace(
        "return this.todos.health();", "return this.todos.health().then(data => ({ ...data, reload: 'nest-second-reload' }));"))
    wait_for(lambda: b"nest-second-reload" in proxy_request(a_url, "/health")[1],
             60, "Nest did not automatically reload a second source edit")
    check(b"nest-source-reloaded" not in proxy_request(b_url, "/health")[1],
          "source edit in A leaked into B")
    check(b"nest-second-reload" not in proxy_request(b_url, "/health")[1],
          "second source edit in A leaked into B")
    controller.write_text(controller_source)
    wait_for(lambda: proxy_request(a_url, "/health")[0] == 200
             and b"reload" not in proxy_request(a_url, "/health")[1],
             60, "Nest did not reload after restoring the source")
    check({item["Id"] for item in harness.containers(a_id)} == a_container_ids,
          "source reload replaced a container")
    harness.mark("Next and Nest source edits reload automatically without restarting containers")

    (first / ".env").write_text("EXAMPLE_GREETING=Updated alpha environment after restart\n")
    harness.down(first)
    status, _ = proxy_request(a_url, "/health")
    check(status == 404, f"stopped worktree A still has a proxy route: HTTP {status}")
    check([item["title"] for item in todos(b_url)] == ["beta"], "B failed while A was stopped")
    check(harness.up(first) == (a_id, a_url), "A restart changed identity or URL")
    check([item["title"] for item in todos(a_url)] == ["alpha"], "A lost data after restart")
    check(todos(a_url)[0]["completed"] is True, "A lost completion state after restart")
    check([item["title"] for item in todos(b_url)] == ["beta"], "B changed after A restart")
    check(b"Updated alpha environment after restart" in proxy_request(a_url, "/")[1],
          "A restart did not inject its edited environment file")
    check(b"Prepared environment greeting" in proxy_request(b_url, "/")[1],
          "A environment edit affected B")
    check({item["Id"] for item in harness.containers(b_id)} == b_container_ids,
          "B containers changed during A stop/restart")
    harness.mark("stopping and restarting A retains its data while B remains running")
