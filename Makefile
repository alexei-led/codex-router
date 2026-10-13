BINARY := codex-router
BIN_DIR := .bin
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.DEFAULT_GOAL := help

.PHONY: help
help: ## show available commands
	@grep -hE '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## build a static binary in .bin/
	CGO_ENABLED=0 go build -ldflags "-X main.version=$(VERSION)" -o $(BIN_DIR)/$(BINARY) ./cmd/codex-router

.PHONY: test
test: ## run race tests with coverage
	go test -race -coverprofile=coverage.out ./...

.PHONY: lint
lint: ## run golangci-lint
	golangci-lint run ./...

.PHONY: fmt
fmt: ## format Go files (gofmt + goimports via golangci-lint)
	golangci-lint fmt ./...

.PHONY: fmt-check
fmt-check: ## verify Go formatting
	golangci-lint fmt --diff ./...

.PHONY: all
all: fmt lint test build ## format, lint, test, and build
