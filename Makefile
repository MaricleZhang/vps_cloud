.PHONY: dev build test

dev:
	bash scripts/dev.sh

build:
	npm --prefix web run build
	mkdir -p .local
	cd backend && go build -o ../.local/cloud .

test:
	npm --prefix web run build
	cd backend && go test ./...
