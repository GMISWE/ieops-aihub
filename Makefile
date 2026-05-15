## ieops-mem — build / push / deploy automation
##
## Tags two immutable images per build:
##   - :v<semver>          (e.g. v0.1.0; tracks pyproject.toml)
##   - :<YYYYMMDD>-<sha>   (e.g. 20260515-3f5918d)
##
## Deploy patches the remote docker-compose.yml in place to point at the
## new date-sha tag and runs `docker compose up -d`. Rollback is a manual
## `make redeploy TAG=<previous-date-sha>`.

SHELL := /bin/bash

REGISTRY    := us-west1-docker.pkg.dev/devv-404803/public
IMAGE       := ieops-mem
VERSION     := $(shell grep -m1 '^version = ' pyproject.toml | cut -d'"' -f2)
DATE        := $(shell date -u +%Y%m%d)
SHA         := $(shell git rev-parse --short HEAD)
DIRTY       := $(shell git diff --quiet 2>/dev/null || echo "-dirty")

TAG_VERSION := $(REGISTRY)/$(IMAGE):v$(VERSION)
TAG_DATESHA := $(REGISTRY)/$(IMAGE):$(DATE)-$(SHA)$(DIRTY)

DEPLOY_HOST := 10.146.0.16
DEPLOY_DIR  := /opt/ieops-mem
PLATFORM    := linux/amd64

.PHONY: help version build push deploy redeploy all test logs ps health clean snapshot-before-deploy deploy-safe

help:  ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk -F':.*?## ' '{printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2}'

version:  ## Show computed tags
	@echo "version : v$(VERSION)"
	@echo "datesha : $(DATE)-$(SHA)$(DIRTY)"
	@echo "tags    :"
	@echo "  $(TAG_VERSION)"
	@echo "  $(TAG_DATESHA)"

build:  ## Build image with both tags
	@test -z "$(DIRTY)" || echo ">>> WARNING: working tree dirty; tag suffixed with -dirty"
	docker build --platform $(PLATFORM) \
		-t $(TAG_VERSION) \
		-t $(TAG_DATESHA) \
		.

push: build  ## Push both tags to Artifact Registry
	docker push $(TAG_VERSION)
	docker push $(TAG_DATESHA)

deploy: push  ## Build + push + ssh-update compose + restart container
	@test -z "$(DIRTY)" || (echo ">>> ERROR: refusing to deploy dirty build"; exit 1)
	@echo ">>> patching $(DEPLOY_HOST):$(DEPLOY_DIR)/docker-compose.yml → $(TAG_DATESHA)"
	ssh $(DEPLOY_HOST) "sudo sed -i 's|image: .*|image: $(TAG_DATESHA)|' $(DEPLOY_DIR)/docker-compose.yml"
	@echo ">>> docker compose pull + up -d"
	ssh $(DEPLOY_HOST) "cd $(DEPLOY_DIR) && sudo docker compose pull && sudo docker compose up -d"
	@$(MAKE) --no-print-directory health

redeploy:  ## Rollback / pin to a specific date-sha tag.  Usage: make redeploy TAG=20260514-abc1234
	@test -n "$(TAG)" || (echo "Usage: make redeploy TAG=<date-sha>"; exit 1)
	@echo ">>> redeploying $(REGISTRY)/$(IMAGE):$(TAG)"
	ssh $(DEPLOY_HOST) "sudo sed -i 's|image: .*|image: $(REGISTRY)/$(IMAGE):$(TAG)|' $(DEPLOY_DIR)/docker-compose.yml"
	ssh $(DEPLOY_HOST) "cd $(DEPLOY_DIR) && sudo docker compose pull && sudo docker compose up -d"
	@$(MAKE) --no-print-directory health

all: deploy  ## Alias for `deploy` (build + push + deploy)

test:  ## Run pytest locally (mimics CI invocation)
	pip install -q ".[test]"
	pytest tests/ -v

logs:  ## Tail remote container logs
	ssh $(DEPLOY_HOST) "sudo docker logs -f --tail 50 ieops-mem"

ps:  ## Show remote container status
	ssh $(DEPLOY_HOST) "cd $(DEPLOY_DIR) && sudo docker compose ps"

health:  ## Hit /health on the deployed instance (retries during post-deploy uvicorn cold start)
	@echo ">>> http://$(DEPLOY_HOST)/health"
	@for i in 1 2 3 4 5 6 7 8 9 10; do \
		out=$$(curl -sS --max-time 3 http://$(DEPLOY_HOST)/health 2>/dev/null) && echo "$$out" && exit 0; \
		sleep 2; \
	done; echo; echo "FAIL after 10 attempts (~20s)"; exit 1

clean:  ## Remove locally-built images
	-docker rmi $(TAG_VERSION) $(TAG_DATESHA) 2>/dev/null

# Host data dir: docker volume source for ieops-mem's /data inside the container.
# Verified via `docker inspect ieops-mem --format '{{json .Mounts}}'` → Source.
DATA_DIR      := /data/ieops-mem/data
SNAPSHOTS_DIR := /opt/ieops-mem/snapshots
SKIP_PREDEPLOY_SNAPSHOT ?=

snapshot-before-deploy:  ## Take a snapshot of the prod DB before a release
	@ts=$$(date -u +%Y%m%dT%H%M%SZ); \
	echo ">>> snapshotting $(DEPLOY_HOST):$(DATA_DIR)/ieops-mem.db → $(SNAPSHOTS_DIR)/pre-v$(VERSION)-$$ts.db"; \
	ssh $(DEPLOY_HOST) "sudo mkdir -p $(SNAPSHOTS_DIR) \
	  && sudo docker exec ieops-mem python3 -c 'import sqlite3; sqlite3.connect(\"/data/ieops-mem.db\").execute(\"PRAGMA wal_checkpoint(FULL)\")' \
	  && sudo cp $(DATA_DIR)/ieops-mem.db $(SNAPSHOTS_DIR)/pre-v$(VERSION)-$$ts.db \
	  && sudo ls -la $(SNAPSHOTS_DIR) | tail -3"

deploy-safe:  ## Snapshot then deploy (recommended for MINOR/MAJOR bumps)
	@[ "$$SKIP_PREDEPLOY_SNAPSHOT" = "1" ] || $(MAKE) snapshot-before-deploy
	@$(MAKE) deploy
