COMPOSE = docker compose --env-file .env -f ../infra/dev/compose.yaml

.PHONY: db-up db-down run

db-up:
	$(COMPOSE) up -d --wait

db-down:
	$(COMPOSE) down

run:
	set -a && . ./.env && set +a && go run ./cmd/server
