.PHONY: up down seed test test-local

# Database URL for the local test postgres instance
TEST_DB_URL ?= postgres://postgres:testpass@localhost:5433/aihub_test?sslmode=disable
AIHUB_URL   ?= http://localhost:8081

up:
	docker-compose up -d --wait

down:
	docker-compose down -v

# Run migrations and seed test data
seed:
	goose -dir ../../internal/db/migrations postgres "$(TEST_DB_URL)" up
	psql "$(TEST_DB_URL)" -f seed_test_data.sql

# Bring up containers, run migrations + seed, then run tests
test: up seed
	AIHUB_URL=$(AIHUB_URL) \
	AIHUB_API_KEY=test-api-key-writer \
	AIHUB_ADMIN_KEY=test-api-key-admin \
	  go test -v -tags=integration -timeout=120s ./...

# Run against an already-running aihub (for local dev — skips docker-compose)
test-local:
	AIHUB_URL=$(AIHUB_URL) \
	AIHUB_API_KEY=test-api-key-writer \
	AIHUB_ADMIN_KEY=test-api-key-admin \
	  go test -v -tags=integration -timeout=120s ./...
