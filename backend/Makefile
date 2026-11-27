COMPOSE = docker compose --env-file .env -f ../infra/dev/compose.yaml
LOAD_ENV = set -a && . ./.env && set +a

.PHONY: db-up db-down migrate run

db-up:
	$(COMPOSE) up -d --wait

db-down:
	$(COMPOSE) down

migrate:
	$(LOAD_ENV) && go tool goose -dir db/migrations postgres "$$DATABASE_URL" up

run:
	$(LOAD_ENV) && go run ./cmd/server
