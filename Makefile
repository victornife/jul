.PHONY: build test bench fuzz format lint vulncheck clean \
        console-dev console-build console-check build-console

# ── Default ──────────────────────────────────────────────────────────
build:
	go build -o jul ./cmd/jul

test:
	go test ./...

bench:
	scripts/bench.sh

fuzz:
	scripts/fuzz.sh

format:
	gofmt -w .

lint:
	golangci-lint run

vulncheck:
	govulncheck ./...

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
