BINARY := bin/gateway

.PHONY: help build run test vet fmt tidy docker-build compose-up compose-down

help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

##@ Development
fmt: ## Format code
	go fmt ./...

vet: ## Run go vet
	go vet ./...

test: ## Run unit tests
	go test ./...

tidy: ## Tidy modules
	go mod tidy

##@ Build & run
build: ## Build the gateway binary into bin/
	CGO_ENABLED=0 go build -trimpath -o $(BINARY) ./cmd/gateway

run: ## Run the gateway locally (uses configs/config.yaml)
	go run ./cmd/gateway

docker-build: ## Build the gateway container image
	docker build -t promptrelay:local .

##@ Local stack
compose-up: ## Start redis-stack, ollama, prometheus, grafana, and the gateway
	docker compose -f deploy/docker-compose.yml up -d --build

compose-down: ## Tear down the local stack
	docker compose -f deploy/docker-compose.yml down
