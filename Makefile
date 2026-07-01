.PHONY: build test bench fuzz soak format format-check lint vulncheck clean \
        console-dev console-build console-check build-console build-full

# ── Default ──────────────────────────────────────────────────────────
build:
	go build -o jul ./cmd/jul

test:
	go test ./...

test-full:
	go test -tags "$(FULL_TAGS)" ./...

bench:
	scripts/bench.sh

fuzz:
	scripts/fuzz.sh

# Post-GA soak gate (ADR 0005). Override SOAK_DURATION/SOAK_WORKERS for a longer,
# release-style run, e.g. `SOAK_DURATION=5m SOAK_WORKERS=32 make soak`.
soak:
	scripts/soak.sh

format:
	gofmt -w .

# Non-mutating formatting gate (matches CI). Fails if any file needs gofmt.
format-check:
	@files=$$(gofmt -l .); \
	if [ -n "$$files" ]; then \
		echo "gofmt needed on:"; echo "$$files"; exit 1; \
	fi

lint:
	golangci-lint run

lint-full:
	golangci-lint run --build-tags "$(FULL_TAGS)"

vulncheck:
	govulncheck ./...

vulncheck-full:
	govulncheck -tags "$(FULL_TAGS)" ./...

ci-fast: format-check lint test build

ci-full: format-check lint-full test-full vulncheck-full build-full

clean:
	go clean
	rm -f jul bench-results.txt

# ── Console v2 (frontend) ───────────────────────────────────────────
CONSOLE_UI := internal/admin/ui
CONSOLE_DIST := internal/admin/assets/dist

console-dev:
	cd $(CONSOLE_UI) && pnpm dev

console-build: console-check
	cd $(CONSOLE_UI) && pnpm run build

console-check:
	cd $(CONSOLE_UI) && pnpm run typecheck
	cd $(CONSOLE_UI) && pnpm run lint
	cd $(CONSOLE_UI) && pnpm run test

# Build frontend assets then the full Go binary with console tag.
build-console: console-build
	go build -tags "console" -o jul ./cmd/jul

# Build the full Go binary with all tags.
build-full:
	go build -tags "$(FULL_TAGS)" -o jul ./cmd/jul

# Full opt-in feature tag set — keep in sync with .github/workflows/ci.yml
FULL_TAGS := brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf
