# netward's development tasks. Run "make" to generate the sqlc code, lint and
# build netward for all platforms in PLATFORMS into bin/.

GO ?= go

# Tool versions for the install targets. "latest" installs the newest
# release; set a version such as v1.31.1 to pin one.
SQLC_VERSION ?= latest
GOOSE_VERSION ?= latest
GOLANGCI_LINT_VERSION ?= latest

# Platforms to build for, as GOOS/GOARCH. linux/arm builds for ARMv7, such as
# a Raspberry Pi with a 32-bit system.
PLATFORMS ?= linux/amd64 linux/arm64 linux/arm darwin/amd64 darwin/arm64

BIN_DIR := bin

# The tools are installed into GOBIN, and the recipes run them with GOBIN
# first in PATH so that the installed versions are the ones that run.
GOBIN ?= $(shell $(GO) env GOBIN 2>/dev/null)
ifeq ($(GOBIN),)
GOPATH := $(shell $(GO) env GOPATH 2>/dev/null)
ifneq ($(GOPATH),)
GOBIN := $(GOPATH)/bin
endif
endif

ifneq ($(GOBIN),)
export GOBIN
WITH_TOOLS := PATH="$(GOBIN):$$PATH"
endif

# ensure runs the install target $(2) if the command $(1) is not installed.
ensure = $(if $(WITH_TOOLS),export $(WITH_TOOLS);) command -v $(1) >/dev/null 2>&1 || \
	$(MAKE) --no-print-directory $(2)

.DEFAULT_GOAL := build

.PHONY: check-go
check-go:
	@command -v $(GO) >/dev/null 2>&1 || { \
		echo "error: go is not installed or not in PATH, see https://go.dev/doc/install" >&2; \
		exit 1; \
	}

.PHONY: install-tools
install-tools: install-sqlc install-goose install-golangci-lint

# sqlc needs cgo and therefore a C compiler to build.
.PHONY: install-sqlc
install-sqlc: check-go
	$(GO) install github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION)

# goose is only needed to create new migrations with "goose -dir
# internal/adapter/sqlite/migrations create <name> sql". netward runs its
# migrations itself.
.PHONY: install-goose
install-goose: check-go
	$(GO) install github.com/pressly/goose/v3/cmd/goose@$(GOOSE_VERSION)

# golangci-lint comes with all its linters.
.PHONY: install-golangci-lint
install-golangci-lint: check-go
	$(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

.PHONY: sqlcgen
sqlcgen: check-go
	@$(call ensure,sqlc,install-sqlc)
	@echo sqlc generate
	@$(WITH_TOOLS) sqlc generate

.PHONY: lint
lint: sqlcgen
	@$(call ensure,golangci-lint,install-golangci-lint)
	@echo golangci-lint run ./...
	@$(WITH_TOOLS) golangci-lint run ./...

.PHONY: build
build: lint
	@for platform in $(PLATFORMS); do \
		os=$${platform%/*}; \
		arch=$${platform#*/}; \
		out=$(BIN_DIR)/netward-$$os-$$arch; \
		if [ "$$os" = windows ]; then out=$$out.exe; fi; \
		echo "building $$out"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -trimpath -ldflags="-s -w" -o $$out ./cmd/netward || exit 1; \
	done

.PHONY: clean
clean:
	rm -rf $(BIN_DIR)
