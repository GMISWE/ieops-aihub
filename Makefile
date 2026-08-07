.PHONY: build migrate-up migrate-down test lint deploy

PROD_HOST  ?= 10.146.0.16
GCR_IMAGE  := us-west1-docker.pkg.dev/devv-404803/public/aihub
COMPOSE_DIR := /root/manifests/aihub-v1

VERSION ?= dev
# Full 40-char SHA, NOT --short. polyforge-mcp.sh's update check extracts the
# running binary's commit with `grep -oE '[a-f0-9]{40}'`; a 7-char short SHA
# never matches, so the check could not read the version. Before aihub#237 that
# was silently treated as "already up to date" and pinned the binary forever —
# so every `make build` produced a binary that could never self-update. Keep
# this in step with .github/workflows/publish-bins.yml, which uses the full SHA
# for the same reason.
GIT_COMMIT ?= $(shell git rev-parse HEAD 2>/dev/null || echo unknown)
BUILD_TIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -X github.com/GMISWE/ieops-aihub/internal/version.Version=$(VERSION) \
           -X github.com/GMISWE/ieops-aihub/internal/version.GitCommit=$(GIT_COMMIT) \
           -X github.com/GMISWE/ieops-aihub/internal/version.BuildTime=$(BUILD_TIME)

build:
	go build -ldflags "$(LDFLAGS)" -o bin/aihub ./cmd/aihub/
	go build -ldflags "$(LDFLAGS)" -o bin/polyforge ./cmd/polyforge/

migrate-up:
	goose -dir internal/db/migrations postgres "$(DATABASE_URL)" up

migrate-down:
	goose -dir internal/db/migrations postgres "$(DATABASE_URL)" down

test:
	go test ./...

lint:
	golangci-lint run ./...

# Deploy latest image to prod server.
# GCR auth must be configured on $(PROD_HOST): docker login us-west1-docker.pkg.dev
# Key: ~/.gcp/devv-404803-2ab2fee8bad0.json (artifact-service@devv-404803)
deploy:
	ssh $(PROD_HOST) " \
	  docker pull $(GCR_IMAGE):latest && \
	  cd $(COMPOSE_DIR) && \
	  docker compose up -d --no-deps --force-recreate aihub \
	"
