BINARY := codex-router
CMD := ./cmd/codex-router
BIN_DIR := .bin
DIST_DIR := dist

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

# Windows is not a target: the launcher uses Setpgid and syscall.Kill.
PLATFORMS := darwin/amd64 darwin/arm64 linux/amd64 linux/arm64

# Pinned so a tool release nobody chose cannot change a verdict. Bump deliberately.
GOLANGCI_LINT_VERSION := v2.14.0
GOVULNCHECK_VERSION := v1.9.0

.DEFAULT_GOAL := help

.PHONY: help
help: ## show available commands
	@grep -hE '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## build a static binary in .bin/
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(BINARY) $(CMD)

.PHONY: test
test: ## run race tests with coverage
	go test -race -coverprofile=coverage.out ./...

.PHONY: coverage
coverage: test ## print per-function coverage
	go tool cover -func=coverage.out

.PHONY: vet
vet: ## run go vet
	go vet ./...

.PHONY: lint
lint: ## run golangci-lint
	golangci-lint run ./...

.PHONY: vuln
vuln: ## scan dependencies and stdlib for known vulnerabilities
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

.PHONY: fmt
fmt: ## format Go files (gofmt + goimports via golangci-lint)
	golangci-lint fmt ./...

.PHONY: fmt-check
fmt-check: ## verify Go formatting
	golangci-lint fmt --diff ./...

.PHONY: tools
tools: ## install pinned golangci-lint
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

.PHONY: release-build
release-build: clean-dist ## cross-build codex-router-<os>-<arch> and checksums.txt into dist/
	@mkdir -p $(DIST_DIR)
	@for platform in $(PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		out=$(DIST_DIR)/$(BINARY)-$$os-$$arch; \
		echo "building $$out"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "-s -w $(LDFLAGS)" -o $$out $(CMD) || exit 1; \
	done
	@cd $(DIST_DIR) && { command -v sha256sum >/dev/null 2>&1 && sha256sum $(BINARY)-* || shasum -a 256 $(BINARY)-*; } > checksums.txt

.PHONY: clean-dist
clean-dist:
	rm -rf $(DIST_DIR)

.PHONY: clean
clean: clean-dist ## remove build output and coverage data
	rm -rf $(BIN_DIR) coverage.out

.PHONY: all
all: fmt lint test build ## format, lint, test, and build
