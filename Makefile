.PHONY: dev build test traffic-smoke

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
