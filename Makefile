GO ?= go
CONFIG ?= config.yml
BINARY ?= bin/baraholler
PACKAGE ?= ./cmd/baraholler
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS ?= -X main.version=$(VERSION)

.PHONY: prepare run check-config build test vet fmt fmt-check check

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

vet:
	$(GO) vet ./...

fmt:
	gofmt -w cmd internal

fmt-check:
	@test -z "$$(gofmt -l cmd internal)" || { \
		echo "The following Go files need formatting:" >&2; \
		gofmt -l cmd internal >&2; \
		exit 1; \
	}

check: fmt-check vet test
