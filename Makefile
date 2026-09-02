.PHONY: build migrate-up migrate-down test lint deploy

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

# Deploying is deliberately not a make target.
#
# It needs four things this file cannot carry: a database backup, a recorded
# rollback anchor, migrations applied strictly BEFORE the new binary starts,
# and a check afterwards that the read path still answers. A one-line target
# invites skipping all four, which is how the previous one came to be wrong.
#
# What was here was written for a setup that has not existed for months: it
# ssh'd to a host that no longer answers, ran `docker compose up` in a compose
# directory production does not have (it runs bare `docker run` against Cloud
# SQL), pulled `:latest` where the documented flow pins a git-SHA tag, and
# never ran migrations at all. That last one stopped being survivable when
# 0032 added a column every project READ selects: a binary-first rollout now
# fails GET /v1/projects outright with SQLSTATE 42703.
#
# Correcting only the host would have turned a loud failure into a quiet wrong
# deploy, so this points at the real procedure instead. It carries no host, no
# image tag and no directory, so there is nothing here left to go stale.
deploy:
	@echo 'make deploy is not the deploy path, and has not been for months.'
	@echo
	@echo 'The procedure is in docs/deployment.md, section'
	@echo '  "Current production (Cloud SQL + bare docker run)"'
	@echo 'and is run on the host, not from here.'
	@echo
	@echo 'It backs up the database, records a rollback anchor, pulls the image'
	@echo 'by git-SHA tag, applies migrations BEFORE swapping the container, and'
	@echo 'verifies GET /v1/projects still answers afterwards.'
	@exit 1
