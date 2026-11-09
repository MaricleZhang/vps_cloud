.PHONY: dev build build-kernel test traffic-smoke fleet-smoke protocol-smoke release release-center release-node

RELEASE_ARCH ?= native
RELEASE_VERSION ?= dev
KERNEL_TAGS := with_utls,with_v2ray_api

dev:
	bash scripts/dev.sh

build:
	npm --prefix web run build
	mkdir -p .local
	cd backend && go build -o ../.local/cloud .
	cd node-agent && go build -o ../.local/node-agent .
	cd proxy-kernel && go build -tags=$(KERNEL_TAGS) -o ../.local/nicecloud-kernel .

build-kernel:
	mkdir -p .local
	cd proxy-kernel && go build -tags=$(KERNEL_TAGS) -o ../.local/nicecloud-kernel .

test:
	npm --prefix web run build
	cd backend && go test ./...
	cd node-agent && go test ./...
	cd proxy-kernel && go test -tags=$(KERNEL_TAGS) ./...

# Requires an official Xray executable; uses only temporary local data/ports.
traffic-smoke: build
	python3 scripts/traffic-smoke.py --core "$(XRAY_CORE)"

# One center, five independent Agents, and real connections for two users.
fleet-smoke: build
	python3 scripts/traffic-fleet-smoke.py --core "$(XRAY_CORE)"

# Real Mihomo client against REALITY/Vision and AnyTLS/ECH server profiles.
protocol-smoke: build
	python3 scripts/clash-protocol-smoke.py --client "$(MIHOMO_CORE)"

# The center's SQLite CGO build requires native Linux of the target architecture.
release:
	bash scripts/build-release.sh --component all --arch "$(RELEASE_ARCH)" --version "$(RELEASE_VERSION)"

release-center:
	bash scripts/build-release.sh --component center --arch "$(RELEASE_ARCH)" --version "$(RELEASE_VERSION)"

release-node:
	bash scripts/build-release.sh --component node --arch "$(RELEASE_ARCH)" --version "$(RELEASE_VERSION)"
