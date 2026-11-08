.PHONY: dev build test traffic-smoke fleet-smoke release release-center release-node

RELEASE_ARCH ?= native
RELEASE_VERSION ?= dev

dev:
	bash scripts/dev.sh

build:
	npm --prefix web run build
	mkdir -p .local
	cd backend && go build -o ../.local/cloud .
	cd node-agent && go build -o ../.local/node-agent .

test:
	npm --prefix web run build
	cd backend && go test ./...
	cd node-agent && go test ./...

# Requires an official Xray executable; uses only temporary local data/ports.
traffic-smoke: build
	python3 scripts/traffic-smoke.py --core "$(XRAY_CORE)"

# One center, five independent Agents, and real connections for two users.
fleet-smoke: build
	python3 scripts/traffic-fleet-smoke.py --core "$(XRAY_CORE)"

# The center's SQLite CGO build requires native Linux of the target architecture.
release:
	bash scripts/build-release.sh --component all --arch "$(RELEASE_ARCH)" --version "$(RELEASE_VERSION)"

release-center:
	bash scripts/build-release.sh --component center --arch "$(RELEASE_ARCH)" --version "$(RELEASE_VERSION)"

release-node:
	bash scripts/build-release.sh --component node --arch "$(RELEASE_ARCH)" --version "$(RELEASE_VERSION)"
