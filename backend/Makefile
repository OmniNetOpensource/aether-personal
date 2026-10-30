COMPOSE = docker compose --env-file .env -f ../infra/dev/compose.yaml
LOAD_ENV = set -a && . ./.env && set +a

.PHONY: db-up db-down migrate import-services run

db-up:
	$(COMPOSE) up -d --wait

db-down:
	$(COMPOSE) down

migrate:
	$(LOAD_ENV) && go tool goose -dir db/migrations postgres "$$DATABASE_URL" up

# 一次性把旧 .env 中的服务 Key 导入数据库；已保存或已清除的记录不覆盖。
import-services:
	$(LOAD_ENV) && go run ./cmd/import-services

run:
	$(LOAD_ENV) && go run ./cmd/server
