.PHONY: build migrate-up migrate-down test lint

VERSION ?= dev
GIT_COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
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
