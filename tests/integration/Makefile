.PHONY: up down seed test test-local check-goose

# Database URL for the local test postgres instance
TEST_DB_URL ?= postgres://postgres:testpass@localhost:5433/aihub_test?sslmode=disable
AIHUB_URL   ?= http://localhost:8081

# pressly/goose migration tool — go install github.com/pressly/goose/v3/cmd/goose@latest
# Use GOOSE_BIN to override (e.g. GOOSE_BIN=/custom/path/goose make seed).
# /go/bin is prepended so the Go-installed pressly/goose takes priority over any
# other binary named 'goose' in PATH (e.g. the Goose AI agent).
GOOSE_BIN   ?= $(shell PATH=/go/bin:$(PATH) which goose)

# Guard: verify GOOSE_BIN is pressly/goose (not some other binary named 'goose').
# Fails loudly rather than running the wrong tool against the database.
check-goose:
	@$(GOOSE_BIN) --version 2>&1 | grep -q "goose version:" || \
	  { echo "ERROR: $(GOOSE_BIN) does not appear to be pressly/goose."; \
	    echo "       Install with: go install github.com/pressly/goose/v3/cmd/goose@latest"; \
	    echo "       Or override:  GOOSE_BIN=/path/to/pressly-goose make seed"; \
	    exit 1; }

up:
	docker compose up -d --wait

down:
	docker compose down -v

# Run migrations and seed test data
seed: check-goose
	$(GOOSE_BIN) -dir ../../internal/db/migrations postgres "$(TEST_DB_URL)" up
	psql "$(TEST_DB_URL)" -f seed_test_data.sql

# Bring up containers, run migrations + seed, then run tests.
# GOWORK=off disables workspace mode so go.mod in the repo root is used directly
# (the workspace go.work at the repo parent only lists .repo/*, not worktrees).
test: up seed
	GOWORK=off \
	AIHUB_URL=$(AIHUB_URL) \
	AIHUB_API_KEY=test-api-key-writer \
	AIHUB_ADMIN_KEY=test-api-key-admin \
	  go test -v -tags=integration -timeout=120s ../../tests/integration/...

# Run against an already-running aihub (for local dev — skips docker compose)
test-local:
	GOWORK=off \
	AIHUB_URL=$(AIHUB_URL) \
	AIHUB_API_KEY=test-api-key-writer \
	AIHUB_ADMIN_KEY=test-api-key-admin \
	  go test -v -tags=integration -timeout=120s ../../tests/integration/...
