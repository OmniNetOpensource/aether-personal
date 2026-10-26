.PHONY: dev frontend admin check check-integration check-live check-research sandbox-up sandbox-build sandbox-create sandbox-status sandbox-pause sandbox-resume

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

sandbox-up:
	docker compose -f infra/sandbox/compose.yaml --env-file infra/sandbox/.env up -d --build

sandbox-build:
	docker build -t aether-personal-desktop:dev infra/sandbox

sandbox-create sandbox-status sandbox-pause sandbox-resume:
	set -a && . ./infra/sandbox/.env && set +a && python3 infra/sandbox/sandbox.py $(patsubst sandbox-%,%,$@)
