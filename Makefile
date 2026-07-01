# swarm-hpa — build & development tasks
BINARY   := swarm-hpa
MAIN     := ./cmd/swarm-hpa
BIN_DIR  := bin
OUT      := $(BIN_DIR)/$(BINARY)
PKGS     := ./...
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -ldflags "-s -w -X main.version=$(VERSION)"
GOLANGCI := golangci-lint

.DEFAULT_GOAL := build
.PHONY: build run test test-race test-integration cover lint fmt fmt-check vet tidy clean help

build: ## Build the daemon binary into bin/
	@mkdir -p $(BIN_DIR)
	go build $(LDFLAGS) -o $(OUT) $(MAIN)

run: ## Run the daemon (pass flags via ARGS="--dry-run")
	go run $(MAIN) $(ARGS)

test: ## Run unit tests
	go test $(PKGS)

test-race: ## Run unit tests with the race detector
	go test -race $(PKGS)

test-integration: ## Run all tests including the integration-tagged harness
	go test -tags integration -race $(PKGS)

cover: ## Run tests and print a coverage summary
	go test -coverprofile=coverage.out $(PKGS)
	go tool cover -func=coverage.out

lint: ## Run golangci-lint (v2)
	@command -v $(GOLANGCI) >/dev/null 2>&1 || { \
		echo "golangci-lint not installed — see https://golangci-lint.run/welcome/install/"; \
		exit 1; }
	$(GOLANGCI) run

fmt: ## Format the code (gofmt -s)
	gofmt -s -w .

fmt-check: ## Verify all files are gofmt-clean (CI gate; non-mutating)
	@out=$$(gofmt -s -l .); \
	if [ -n "$$out" ]; then \
		echo "gofmt needed on:"; echo "$$out"; exit 1; \
	fi

vet: ## Run go vet
	go vet $(PKGS)

tidy: ## Tidy module dependencies
	go mod tidy

clean: ## Remove build artifacts
	rm -rf $(BIN_DIR) coverage.out

help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | \
		awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'
