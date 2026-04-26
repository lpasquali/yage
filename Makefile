# yage — build, test, and pull in local toolchain + module deps
#
# Quick start:   make deps && make
# System packages (Debian/Ubuntu):  make system-deps
#
# Variables (override: make VAR=value):
#   GO      — go binary (default: go)
#   OUT     — path to built binary (default: bin/yage)
#   GOPROXY — e.g. direct or https://proxy.golang.org

SHELL      := /bin/sh
GO         ?= go
OUT        ?= bin/yage
MODULE     := ./cmd/yage
GOMINOR    := 23

export GOTOOLCHAIN ?= auto
export GOPROXY     ?=

.PHONY: all help deps check-go tidy mod-verify build test install clean system-deps

all: build

help:
	@echo "yage Makefile"
	@echo ""
	@echo "  make deps         — verify Go, tidy modules, download + verify (run first on a new clone)"
	@echo "  make build        — compile to $(OUT)"
	@echo "  make test         — go test ./..."
	@echo "  make install      — go install $(MODULE) (uses GOBIN or GOPATH/bin)"
	@echo "  make clean        — remove $(OUT)"
	@echo "  make system-deps  — install OS packages (git, curl, build tools) on apt-based systems"
	@echo ""
	@echo "Set GO, OUT, or GOTOOLCHAIN as needed. Module requires Go 1.$(GOMINOR)+ (see go.mod)."

# --- Build requirements (Go modules) ---

deps: check-go tidy mod-verify
	@echo "deps: OK"

# Ensure Go is on PATH; ensure version is at least 1.$(GOMINOR) (same as go.mod)
check-go:
	@command -v $(GO) >/dev/null || (echo "Go not found. Install Go 1.$(GOMINOR)+ from https://go.dev/dl/ or your package manager."; exit 1)
	@ver="$$($(GO) env GOVERSION 2>/dev/null)"; \
	case "$$ver" in \
	  go2.*|devel|devel-*) echo "Go $$ver — OK";; \
	  go1.*) \
	    minor=$$(printf '%s' "$$ver" | sed 's/^go1\.//;s/[^0-9].*//'); \
	    if [ -z "$$minor" ] || [ "$$minor" -lt $(GOMINOR) ] 2>/dev/null; then \
	      echo "Need Go 1.$(GOMINOR)+, found $$ver"; exit 1; \
	    fi; \
	    echo "Go $$ver — OK";; \
	  *) \
	    if $(GO) version 2>/dev/null | grep -q 'devel +'; then echo "Go devel — OK"; exit 0; fi; \
	    echo "Need Go 1.$(GOMINOR)+, could not parse: $$ver"; exit 1;; \
	esac

tidy:
	$(GO) mod tidy

mod-verify:
	$(GO) mod download
	$(GO) mod verify

# --- Build & test ---

build: | $(dir $(OUT))
	$(GO) build -o $(OUT) -trimpath $(MODULE)

$(dir $(OUT)):
	mkdir -p "$@"

test:
	$(GO) test ./...

# Installs to $$GOBIN, or $$HOME/go/bin if GOBIN is empty (same as go install default)
install: check-go tidy
	$(GO) install -trimpath $(MODULE)

clean:
	rm -f $(OUT)

# --- Optional host packages (for building and for running the full bootstrap) ---

system-deps:
	@if ! command -v apt-get >/dev/null 2>&1; then \
	  echo "No apt-get found. Install: git, curl, build-essential (and python3) via your package manager."; \
	  exit 0; \
	fi
	sudo apt-get update
	sudo apt-get install -y --no-install-recommends build-essential git curl ca-certificates python3
