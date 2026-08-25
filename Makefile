GO ?= go
CONFIG ?= config.yml
BINARY ?= bin/baraholler
PACKAGE ?= ./cmd/baraholler
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS ?= -X main.version=$(VERSION)
GOLANGCI_LINT_VERSION ?= v2.7.0
GOLANGCI_LINT_DIR ?= $(CURDIR)/bin/tools/golangci-lint/$(GOLANGCI_LINT_VERSION)
GOLANGCI_LINT ?= $(GOLANGCI_LINT_DIR)/golangci-lint

.PHONY: prepare run check-config build test race vet lint fmt fmt-check check

prepare:
	@if [ -e "$(CONFIG)" ]; then \
		echo "$(CONFIG) already exists; refusing to overwrite it" >&2; \
		exit 1; \
	fi
	@cp config.example.yml "$(CONFIG)"
	@chmod 600 "$(CONFIG)"
	@echo "Created $(CONFIG). Fill it in before running Baraholler."

run:
	$(GO) run $(PACKAGE) run -config "$(CONFIG)"

check-config:
	$(GO) run $(PACKAGE) check-config -config "$(CONFIG)"

build:
	@mkdir -p "$(dir $(BINARY))"
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o "$(BINARY)" $(PACKAGE)

test:
	$(GO) test ./...

race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

$(GOLANGCI_LINT):
	@mkdir -p "$(GOLANGCI_LINT_DIR)"
	GOBIN="$(GOLANGCI_LINT_DIR)" $(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

lint: $(GOLANGCI_LINT)
	$(GOLANGCI_LINT) run

fmt:
	gofmt -w cmd internal

fmt-check:
	@test -z "$$(gofmt -l cmd internal)" || { \
		echo "The following Go files need formatting:" >&2; \
		gofmt -l cmd internal >&2; \
		exit 1; \
	}

check: fmt-check vet lint race
