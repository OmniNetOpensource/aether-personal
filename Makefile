FLUTTER ?= $(if $(wildcard $(HOME)/develop/flutter/bin/flutter),$(HOME)/develop/flutter/bin/flutter,flutter)

.PHONY: dev frontend check check-integration check-live check-research

frontend:
	cd frontend && "$(FLUTTER)" pub get
	cd frontend && "$(FLUTTER)" build web --no-web-resources-cdn

dev: frontend
	$(MAKE) -C backend db-up
	$(MAKE) -C backend migrate
	$(MAKE) -C backend run

check:
	cd backend && go build ./... && go vet ./... && go test -race ./...
	cd frontend && "$(FLUTTER)" analyze && "$(FLUTTER)" test

check-integration:
	$(MAKE) -C backend db-up
	cd backend && set -a && . ./.env && set +a && TEST_DATABASE_URL="$$DATABASE_URL" go test -race ./internal/agent -count=1

check-live:
	cd backend && set -a && . ./.env && set +a && RUN_LIVE_TOOLS=1 go test ./internal/agent -run TestLiveWebTools -count=1 -v

RESEARCH_OUTPUT ?= $(CURDIR)/docs/experiments/agent-research/$(shell date +%Y%m%d-%H%M%S)

check-research:
	$(MAKE) -C backend db-up
	cd backend && set -a && . ./.env && set +a && TEST_DATABASE_URL="$$DATABASE_URL" RUN_LIVE_AGENT_RESEARCH=1 AGENT_RESEARCH_OUTPUT="$(RESEARCH_OUTPUT)" go test ./internal/agent -run TestLiveAgentResearch -count=1 -v -timeout 75m
