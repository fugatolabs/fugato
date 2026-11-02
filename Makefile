SHELL := /bin/sh
GO ?= go
PYTHON ?= python3
TEST_PYTHON ?= $(if $(wildcard .venv/bin/python),.venv/bin/python,$(PYTHON))
GORELEASER ?= goreleaser
PREFIX ?= $(HOME)/.local
ARTIFACTS ?= artifacts/acceptance
BINARY ?= $(CURDIR)/bin/fugato
MCP_BINARY ?= $(CURDIR)/bin/fugato-mcp
SUITE ?= native or prepare or compose or environments or destroy or fullstack
ACCEPTANCE_ARGS ?=
export FUGATO_VERSION := $(shell sed -n 's/^const Version = "\([^"]*\)"/\1/p' packages/fugato/internal/version/version.go)
COMMIT := $(shell git rev-parse HEAD)$(shell test -z "$$(git status --porcelain)" || printf '%s' '-dirty')
LDFLAGS := -X github.com/fugatolabs/fugato/packages/fugato/internal/version.Commit=$(COMMIT)

.PHONY: help build install-dev test-deps test test-tooling check check-generated fmt-check vet vulnerabilities docs skill notices acceptance extension-check extension-package extension-install release-prepare release-snapshot release package

help:
	@printf '%s\n' 'build / install-dev  Build binaries / install into PREFIX/bin' 'test-deps           Create .venv and install pinned test dependencies' 'check               Generated files, formatting, Go race tests, tooling tests and vet' 'vulnerabilities     Scan reachable Go dependencies' 'docs / skill / notices  Refresh generated documentation, embedded skill or licenses' 'acceptance          Run acceptance tests; select with SUITE=native (or compose, etc.)' 'extension-package / extension-install  Package VSIX / install into Cursor' 'release-snapshot    Assemble all release assets locally with GoReleaser' 'Set WITH_EXTENSION=1 to include the optional VSIX in release assets.'

build:
	@mkdir -p bin
	cd packages/fugato && CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o ../../bin/fugato ./cmd/fugato
	cd packages/fugato && CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o ../../bin/fugato-mcp ./cmd/fugato-mcp

install-dev: build
	install -d "$(PREFIX)/bin"
	install -m 755 bin/fugato bin/fugato-mcp "$(PREFIX)/bin/"
	@printf '%s\n' 'Binaries installed. Run fugato setup --agent NAME --project /path/to/project to connect an agent.'

test-deps:
	$(PYTHON) -m venv .venv
	.venv/bin/python -m pip install -r requirements-test.txt

test:
	cd packages/fugato && $(GO) test -race -shuffle=on ./...

test-tooling:
	$(TEST_PYTHON) -m pytest tests/tooling tools/release

fmt-check:
	@test -z "$$(cd packages/fugato && gofmt -l cmd internal)"

vet:
	cd packages/fugato && $(GO) vet ./...

vulnerabilities:
	cd packages/fugato && $(GO) run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...

check-generated:
	$(PYTHON) tools/docs/sync_skill.py --check
	$(PYTHON) tools/licenses/notices.py --check

check: check-generated fmt-check test test-tooling vet

docs: build
	$(PYTHON) tools/docs/export.py --output artifacts/docs.json --cli "$(BINARY)"

skill:
	$(PYTHON) tools/docs/sync_skill.py

notices:
	$(PYTHON) tools/licenses/notices.py

acceptance:
	$(TEST_PYTHON) -m pytest tests/acceptance -m '$(SUITE)' --binary "$(BINARY)" --mcp-binary "$(MCP_BINARY)" --artifacts "$(ARTIFACTS)" $(ACCEPTANCE_ARGS)

extension-check:
	cd packages/cursor-extension && npm ci && npm audit --audit-level=high && npm run check && npm test

extension-package: extension-check
	cd packages/cursor-extension && npm run package

extension-install: extension-package
	cursor --install-extension "packages/cursor-extension/fugato-$(FUGATO_VERSION).vsix"

release-prepare: check-generated
	$(PYTHON) tools/licenses/notices.py
	$(PYTHON) tools/release/prepare.py

release-snapshot:
	$(GORELEASER) release --snapshot --skip=publish --clean

release:
	$(GORELEASER) release --skip=publish --clean

package: release-snapshot
