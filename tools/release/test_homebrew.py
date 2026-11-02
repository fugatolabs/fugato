#!/usr/bin/env python3
"""Check that formula generation cannot silently accept wrong release hashes."""

import hashlib
import importlib.util
from pathlib import Path
import tempfile
import unittest


spec = importlib.util.spec_from_file_location(
    "homebrew_formula", Path(__file__).with_name("homebrew.py"))
formula = importlib.util.module_from_spec(spec)
spec.loader.exec_module(formula)


class FormulaTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.directory = Path(self.temporary.name)
        self.entries = []
        for system, architecture in formula.TARGETS:
            name = f"fugato_0.0.1_{system}_{architecture}.tar.gz"
            # Hash-validation fixture, not a distributable release archive.
            content = name.encode()
            (self.directory / name).write_bytes(content)
            self.entries.append(f"{hashlib.sha256(content).hexdigest()}  {name}\n")
        self.manifest = self.directory / "checksums.txt"
        self.manifest.write_text("".join(self.entries))

    def test_generates_all_verified_target_urls_and_binaries(self):
        output = formula.generate(self.directory, "0.0.1")
        for entry in self.entries:
            checksum, name = entry.strip().split("  ")
            self.assertIn(f'sha256 "{checksum}"', output)
            self.assertIn(f"/v0.0.1/{name}", output)
        self.assertIn('bin.install "fugato", "fugato-mcp"', output)
        self.assertIn('"third_party"', output)
        self.assertNotIn('setup-agent.mjs', output)
        self.assertIn('fugato setup --help', output)
        # Homebrew infers the version from the URL and rejects redundant stanzas.
        self.assertNotIn('  version "', output)

    def test_rejects_changed_archive(self):
        next(self.directory.glob("*.tar.gz")).write_bytes(b"changed")
        with self.assertRaisesRegex(ValueError, "checksum mismatch"):
            formula.generate(self.directory, "0.0.1")

    def test_rejects_missing_and_duplicate_checksums(self):
        for entries, message in ((self.entries[:-1], "missing checksum"),
                                 (self.entries + [self.entries[0]], "duplicate")):
            with self.subTest(message=message):
                self.manifest.write_text("".join(entries))
                with self.assertRaisesRegex(ValueError, message):
                    formula.generate(self.directory, "0.0.1")

    def test_rejects_unsafe_version_and_manifest_paths(self):
        with self.assertRaisesRegex(ValueError, "version"):
            formula.generate(self.directory, '0.0.1"; raise "bad')
        self.manifest.write_text("0" * 64 + "  ../outside.tar.gz\n")
        with self.assertRaisesRegex(ValueError, "malformed"):
            formula.generate(self.directory, "0.0.1")


if __name__ == "__main__":
    unittest.main()
