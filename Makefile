GO ?= go
PNPM ?= pnpm
VERSION := $(shell python3 -c 'import json; print(json.load(open("web/package.json"))["version"])')

.PHONY: setup web build test lint check release release-images integration
setup:
	$(GO) mod download
	$(PNPM) --dir web install --frozen-lockfile
web:
	$(PNPM) --dir web build
build: web
	mkdir -p bin
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags="-X github.com/docker-manager/docker-manager/internal/buildinfo.Version=$(VERSION)" -o bin/docklane ./cmd/server
test: web
	$(GO) test ./...
	$(PNPM) --dir web test
lint: web
	test -z "$$(gofmt -l cmd internal web/embed.go)"
	$(GO) vet ./...
	$(PNPM) --dir web typecheck
	for script in scripts/*.sh; do case "$$script" in *install-online.sh) bash -n "$$script" ;; *) sh -n "$$script" ;; esac; done
	python3 -m py_compile scripts/*.py
	python3 scripts/test-install-online.py
	python3 scripts/test-install-support.py
	python3 scripts/test-package-installation.py
	python3 scripts/test-release-preflight.py
	python3 scripts/check-acceptance.py
check: lint test
	$(GO) test -race ./...
release: web
	./scripts/release.sh
release-images:
	./scripts/build-images.sh
integration:
	./scripts/integration.sh
