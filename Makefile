# Makefile 编排全仓构建、静态检查、本地开发与生产一键发布；不包含任何本地测试目标。
.PHONY: dev frontend admin check edge-check edge-dry-run edge-dev edge-deploy prod-build prod-db-up prod-migrate prod-import-services prod-up prod-status prod-backup publish publish-check

PROD_COMPOSE = docker compose --env-file infra/prod/.env -f infra/prod/compose.yaml

publish:
	python3 scripts/publish.py

publish-check:
	python3 scripts/publish.py --check

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
	$(PROD_COMPOSE) up -d --wait redis api-1 api-2 admin gateway

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
	cd admin && npm ci && npm run build && npm start

check:
	cd backend && go build ./... && go vet ./...
	cd frontend && npm run check
	cd admin && npm run check
	cd infra/cloudflare && npm run check
