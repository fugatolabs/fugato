"""Destroy retains or deletes only the explicitly selected runtime's resources."""
import pytest

from .harness import check

pytestmark = [pytest.mark.destroy, pytest.mark.docker]

CONFIG = """version: 1
compose: compose.yaml
start:
  services: [worker]
"""
COMPOSE = """services:
  worker:
    image: postgres:17-alpine
    command: [sh, -c, 'while :; do sleep 1; done']
    cpus: 0.5
    mem_limit: 128m
    volumes:
      - data:/var/lib/postgresql/data
volumes:
  data:
"""


def test_destroy_isolation(acceptance):
    harness = acceptance
    source = harness.repository("destroy", {".fugato.yaml": CONFIG, "compose.yaml": COMPOSE})
    first, second = (harness.worktree(source, name) for name in ("destroy-a", "destroy-b"))
    first_id = next(key for key, path in harness.projects.items() if path == first)
    second_id = next(key for key, path in harness.projects.items() if path == second)
    check(first_id != second_id, "destroy fixtures shared a runtime ID")
    unrelated = f"fugato-{first_id}_unowned"
    for kind in ("volume", "network"):
        harness.run(["docker", kind, "create", unrelated])
        # The fixture owns exactly these two unlabelled resources. They are not
        # selected through a prefix, and their removal is guaranteed on failure.
        harness.extra_resources.append((kind, unrelated))
    for path in (first, second):
        harness.run([harness.binary, "up", "--worktree", path], timeout=360)

    def container(runtime_id):
        items = harness.containers(runtime_id)
        check(len(items) == 1, f"expected one worker for {runtime_id}: {items}")
        return items[0]

    first_container, second_container = container(first_id), container(second_id)
    image_id = first_container["Image"]
    for item, value in ((first_container, "first"), (second_container, "second")):
        harness.run(["docker", "exec", item["Id"], "sh", "-c", f"printf {value} > /var/lib/postgresql/data/marker"])

    def marker(item):
        return harness.run(["docker", "exec", item["Id"], "cat", "/var/lib/postgresql/data/marker"]).stdout

    def absent(runtime_id, kinds):
        label = f"label=com.docker.compose.project=fugato-{runtime_id}"
        for kind in kinds:
            argv = ["docker", "ps", "-aq", "--filter", label] if kind == "container" else ["docker", kind, "ls", "-q", "--filter", label]
            check(not harness.run(argv).stdout.strip(), f"destroy left owned {kind} for {runtime_id}")

    harness.run([harness.binary, "destroy", "--worktree", first])
    absent(first_id, ("container", "network"))
    harness.run(["docker", "volume", "inspect", f"fugato-{first_id}_data"])
    check(marker(second_container) == "second", "destroy affected another runtime")
    harness.run([harness.binary, "up", "--worktree", first], timeout=360)
    first_container = container(first_id)
    check(marker(first_container) == "first", "default destroy lost retained data")
    harness.mark("default destroy retains data and re-registration reuses it")

    harness.run(["git", "worktree", "remove", "--force", second], cwd=source)
    harness.run([harness.binary, "destroy", "--runtime", second_id, "--volumes"])
    absent(second_id, ("container", "network", "volume"))
    check(marker(first_container) == "first", "runtime-ID cleanup affected another runtime")
    harness.run([harness.binary, "destroy", "--runtime", first_id, "--volumes"])
    absent(first_id, ("container", "network", "volume"))
    harness.run(["docker", "image", "inspect", image_id])
    for kind in ("volume", "network"):
        harness.run(["docker", kind, "inspect", unrelated])
    status, runtimes = harness.api("/v1/runtimes")
    check(status == 200 and runtimes == [], "destroy left registry records")
    harness.mark("deleted-worktree cleanup removes owned volumes; unrelated resources and shared image survive")
