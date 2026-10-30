.PHONY: dev frontend admin check check-integration check-live check-research edge-check edge-dry-run edge-dev edge-deploy prod-build prod-db-up prod-migrate prod-import-services prod-up prod-status prod-backup

PROD_COMPOSE = docker compose --env-file infra/prod/.env -f infra/prod/compose.yaml

edge-check:
	cd infra/cloudflare && npm ci && npm run check

edge-dry-run: frontend edge-check
	cd infra/cloudflare && npm run dry-run

edge-dev:
	cd infra/cloudflare && npm run dev

edge-deploy: edge-dry-run
	cd infra/cloudflare && npm run deploy

prod-build:
	$(PROD_COMPOSE) build

prod-db-up:
	$(PROD_COMPOSE) up -d --wait postgres

prod-migrate:
	$(PROD_COMPOSE) run --rm migrate

prod-import-services:
	$(PROD_COMPOSE) run --rm import-services

prod-up:
	$(PROD_COMPOSE) up -d --wait api admin gateway

prod-status:
	$(PROD_COMPOSE) ps

prod-backup:
	sh scripts/backup-prod.sh

frontend:
	cd frontend && npm ci
	cd frontend && npm run build

dev: frontend
	$(MAKE) -C backend db-up
	$(MAKE) -C backend migrate
	$(MAKE) -C backend run

admin:
	cd admin && npm start

check:
	cd backend && go build ./... && go vet ./... && go test -race ./...
	cd frontend && npm run check
	cd admin && npm run check

check-integration:
	$(MAKE) -C backend db-up
	cd backend && set -a && . ./.env && set +a && TEST_DATABASE_URL="$$DATABASE_URL" go test -race ./internal/agent -count=1

check-live:
	cd backend && set -a && . ./.env && set +a && RUN_LIVE_TOOLS=1 go test ./internal/agent -run TestLiveWebTools -count=1 -v

RESEARCH_OUTPUT ?= $(CURDIR)/docs/experiments/agent-research/$(shell date +%Y%m%d-%H%M%S)

check-research:
	$(MAKE) -C backend db-up
	cd backend && set -a && . ./.env && set +a && TEST_DATABASE_URL="$$DATABASE_URL" RUN_LIVE_AGENT_RESEARCH=1 AGENT_RESEARCH_OUTPUT="$(RESEARCH_OUTPUT)" go test ./internal/agent -run TestLiveAgentResearch -count=1 -v -timeout 75m
