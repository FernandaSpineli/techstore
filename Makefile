GOLANGCI_LINT_VERSION := v2.14.0
LINT := docker run --rm -v $(CURDIR):/app -w /app \
	-v golangci-cache:/root/.cache \
	golangci/golangci-lint:$(GOLANGCI_LINT_VERSION) golangci-lint

.PHONY: help run build test test-unit cover lint fmt tidy up down logs seed admin

help: ## Show available targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*## "} {printf "  %-10s %s\n", $$1, $$2}'

run: ## Run the API on the host (reads .env)
	set -a && . ./.env && set +a && go run ./cmd/api

build: ## Build the API binary into bin/
	CGO_ENABLED=0 go build -trimpath -o bin/api ./cmd/api

test: ## Run all tests with the race detector
	go test -race ./...

test-unit: ## Run only tests that need no external services
	go test -race -short ./...

cover: ## Run tests and write an HTML coverage report
	go test -race -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

lint: ## Run golangci-lint (via Docker)
	$(LINT) run

fmt: ## Format code with gofumpt + goimports (via Docker)
	$(LINT) fmt

tidy: ## Tidy go.mod/go.sum
	go mod tidy

up: ## Start the full stack with Docker Compose
	docker compose up -d --build --wait

down: ## Stop the stack
	docker compose down

logs: ## Follow API logs
	docker compose logs -f api

seed: ## Load the sample catalog into the running stack
	docker compose run --rm migrate seed-demo

admin: ## Grant admin to an existing account: make admin EMAIL=you@example.com
	docker compose run --rm migrate user promote $(EMAIL)
