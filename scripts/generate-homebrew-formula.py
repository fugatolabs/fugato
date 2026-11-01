#!/usr/bin/env python3
"""Print a Homebrew formula from verified Fugato release archives; never publish."""

import argparse
import hashlib
from pathlib import Path
import re
import sys


TARGETS = (("darwin", "arm64"), ("darwin", "amd64"),
           ("linux", "arm64"), ("linux", "amd64"))


def generate(directory, version):
    if not re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?", version):
        raise ValueError("version must be a release number such as 0.0.1")
    manifest = {}
    for line in (directory / "checksums.txt").read_text().splitlines():
        match = re.fullmatch(r"([0-9a-f]{64})  ([^/\\]+)", line)
        if not match:
            raise ValueError("malformed checksum manifest line")
        checksum, name = match.groups()
        if name in manifest:
            raise ValueError(f"duplicate checksum entry: {name}")
        manifest[name] = checksum

    hashes = {}
    for system, architecture in TARGETS:
        name = f"fugato_{version}_{system}_{architecture}.tar.gz"
        if name not in manifest:
            raise ValueError(f"missing checksum: {name}")
        digest = hashlib.sha256()
        with (directory / name).open("rb") as archive:
            for chunk in iter(lambda: archive.read(1024 * 1024), b""):
                digest.update(chunk)
        actual = digest.hexdigest()
        if actual != manifest[name]:
            raise ValueError(f"checksum mismatch: {name}")
        hashes[(system, architecture)] = actual

    lines = [
        "# Generated from verified release archives; review before publishing.",
        "class Fugato < Formula",
        '  desc "Local development environments for Git worktrees"',
        '  homepage "https://fugato.dev"',
        '  license "Apache-2.0"',
    ]
    for system, brew_system in (("darwin", "macos"), ("linux", "linux")):
        lines += ["", f"  on_{brew_system} do"]
        for architecture, brew_architecture in (("arm64", "arm"), ("amd64", "intel")):
            name = f"fugato_{version}_{system}_{architecture}.tar.gz"
            lines += [
                f"    on_{brew_architecture} do",
                f'      url "https://github.com/fugatolabs/fugato/releases/download/v{version}/{name}"',
                f'      sha256 "{hashes[(system, architecture)]}"',
                "    end",
            ]
        lines.append("  end")
    lines += [
        "",
        "  def install",
        '    bin.install "fugato", "fugato-mcp"',
        '    pkgshare.install "INSTALL.md", "LICENSE", "NOTICE", "THIRD_PARTY_NOTICES.md",',
        '                     "setup-agent.mjs", "skills", "third_party"',
        "  end",
        "",
        "  test do",
        '    assert_match version.to_s, shell_output("#{bin}/fugato --version")',
        '    assert_match version.to_s, shell_output("#{bin}/fugato-mcp --version")',
        "  end",
        "end",
        "",
    ]
    return "\n".join(lines)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("release_directory", type=Path,
                        help="directory with four release archives and checksums.txt")
    parser.add_argument("--version", required=True)
    args = parser.parse_args()
    try:
        result = generate(args.release_directory, args.version)
    except (OSError, ValueError) as error:
        parser.exit(1, f"Cannot generate formula: {error}\n")
    sys.stdout.write(result)


if __name__ == "__main__":
    main()
