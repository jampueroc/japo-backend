# Load .env if present so `make dev` and `make migrate-up` see the same
# configuration the application expects. Values must not be quoted.
-include .env
export

MODULE      := github.com/jorgeampuero/japo-backend
BINARY      := api
BIN_DIR     := bin
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS     := -s -w -X main.version=$(VERSION)
GO          ?= go
API_IMAGE   ?= japo-api
API_TAG     ?= latest
TARGETARCH  ?= arm64

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z0-9_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
	| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

# --- Development ----------------------------------------------------------

.PHONY: dev
dev: ## Run the API natively (macOS), loading .env
	$(GO) run ./cmd/api

.PHONY: build
build: ## Build the native binary into bin/
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(BINARY) ./cmd/api

.PHONY: build-arm64
build-arm64: ## Build the static linux/arm64 binary for the Raspberry Pi
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" \
	-o $(BIN_DIR)/$(BINARY)-linux-arm64 ./cmd/api

.PHONY: tidy
tidy: ## Sync go.mod / go.sum
	$(GO) mod tidy

.PHONY: fmt
fmt: ## Format the code
	$(GO) fmt ./...

# --- Tests ----------------------------------------------------------------

.PHONY: test
test: ## Unit tests only: fast, no Docker, no database
	$(GO) test ./...

.PHONY: test-race
test-race: ## Unit tests with the race detector
	$(GO) test -race ./...

.PHONY: test-integration
test-integration: ## Repository tests against an ephemeral MariaDB (needs Docker)
	$(GO) test -tags=integration -count=1 ./...

.PHONY: test-all
test-all: ## Everything, with the race detector (needs Docker)
	$(GO) test -tags=integration -race -count=1 ./...

.PHONY: lint
lint: ## Run golangci-lint, or go vet when it is not installed
	@command -v dot_clean >/dev/null 2>&1 && dot_clean -m . || true
	@if command -v golangci-lint >/dev/null 2>&1; then \
	golangci-lint run ./...; \
	else \
	echo "golangci-lint not found, falling back to go vet"; \
	echo "install: brew install golangci-lint"; \
	$(GO) vet ./...; \
	fi

# --- Database -------------------------------------------------------------

.PHONY: migrate-up
migrate-up: ## Apply the embedded goose migrations
	$(GO) run ./cmd/migrate up

.PHONY: migrate-down
migrate-down: ## Roll back the last goose migration
	$(GO) run ./cmd/migrate down

.PHONY: migrate-status
migrate-status: ## Print the applied schema version
	$(GO) run ./cmd/migrate version

.PHONY: db-up
db-up: ## Start only MariaDB (development on macOS)
	docker compose up -d mariadb

.PHONY: db-down
db-down: ## Stop the compose stack
	docker compose down

.PHONY: db-shell
db-shell: ## Open a mysql shell in the MariaDB container
	docker compose exec mariadb mariadb -u$(DB_USER) -p$(DB_PASSWORD) $(DB_NAME)

# --- Docker ---------------------------------------------------------------

.PHONY: docker-build
docker-build: ## Build the linux/arm64 deploy image
	docker build --platform linux/$(TARGETARCH) \
	--build-arg TARGETARCH=$(TARGETARCH) \
	--build-arg VERSION=$(VERSION) \
	-t $(API_IMAGE):$(API_TAG) .

.PHONY: up
up: ## Start the whole stack (API + MariaDB)
	docker compose up -d

.PHONY: logs
logs: ## Follow the API logs
	docker compose logs -f api

.PHONY: clean
clean: ## Remove build artefacts
	rm -rf $(BIN_DIR)
