GO ?= go
PNPM ?= pnpm

.PHONY: setup web build test lint check release release-images integration
setup:
	$(GO) mod download
	$(PNPM) --dir web install --frozen-lockfile
web:
	$(PNPM) --dir web build
build: web
	mkdir -p bin
	CGO_ENABLED=0 $(GO) build -trimpath -o bin/docklane ./cmd/server
test: web
	$(GO) test ./...
	$(PNPM) --dir web test
lint: web
	test -z "$$(gofmt -l cmd internal web/embed.go)"
	$(GO) vet ./...
	$(PNPM) --dir web typecheck
	for script in scripts/*.sh; do sh -n "$$script"; done
	python3 -m py_compile scripts/*.py
	python3 scripts/check-acceptance.py
check: lint test
	$(GO) test -race ./...
release: web
	./scripts/release.sh
release-images:
	./scripts/build-images.sh
integration:
	./scripts/integration.sh
