.PHONY: build test bench fuzz soak format format-check lint vulncheck clean \
        console-dev console-build console-check build-console build-full license-check \
        hooks waf-churn

# ── Default ──────────────────────────────────────────────────────────
build:
	go build -ldflags "-X main.version=$$(git describe --tags --always --dirty 2>/dev/null || echo dev)" -o jul ./cmd/jul

test:
	go test ./...

test-full:
	go test -tags "$(FULL_TAGS)" ./...

bench:
	scripts/bench.sh

# Compare benchmark results against docs/benchmarks-baseline.txt using benchstat.
# Install benchstat: go install golang.org/x/perf/cmd/benchstat@latest
# Update the baseline: make bench-compare ARGS=--update-baseline
bench-compare:
	scripts/bench-compare.sh $(ARGS)

fuzz:
	scripts/fuzz.sh

# Post-GA soak gate (ADR 0005). Override SOAK_DURATION/SOAK_WORKERS for a longer,
# release-style run, e.g. `SOAK_DURATION=5m SOAK_WORKERS=32 make soak`.
soak:
	scripts/soak.sh

# WAF reload-churn leak/stability gate (AUX-06). Rebuilds the Coraza/CRS engine
# on a sustained reload churn and asserts flat goroutines + bounded heap. Runs in
# the default `waf`-tagged test lane at 30 cycles; override for a longer soak,
# e.g. `WAF_CHURN_ITERS=500 make waf-churn`.
waf-churn:
	go test -tags waf -run '^TestWAFReloadChurnNoLeak$$' -count=1 -v ./internal/waf/

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

ci-fast: format-check lint test build license-check

# Full-tag Go gates (build, lint, test, vulncheck, license).
# Closest local equivalent to the merge gate; does not cover race, coverage
# floors, platform lanes, frontend, or E2E (those run only in CI).
ci-full: format-check lint-full test-full vulncheck-full build-full license-check

# Extended local gate: ci-full + go vet + docs structural check.
# Covers the most common CI-only failures without requiring CGO, pnpm, or
# external services. See CONTRIBUTING.md for the full exclusion list.
ci-pr: ci-full
	go vet -tags "$(FULL_TAGS)" ./...
	python3 scripts/docs-check.py

# Install the repo-managed Git hooks (local CI gate parity, SEQ-08). One command;
# safe to re-run. Uninstall with `git config --unset core.hooksPath`.
hooks:
	git config core.hooksPath .githooks
	@echo "Installed Git hooks (core.hooksPath -> .githooks). Bypass with --no-verify; disable with JUL_SKIP_HOOKS=1."

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

license-check:
	addlicense -check -c "Victor Niharra <vniharrafe@gmail.com>" -l agpl -s \
	  -ignore "**/node_modules/**" \
	  -ignore "**/.git/**" \
	  -ignore "**/assets/dist/**" \
	  -ignore "**/coverage/**" \
	  -ignore "**/pnpm-lock.yaml" \
	  -ignore "**/*.log" \
	  -ignore "**/*.exe" \
	  -ignore "**/go.mod" \
	  -ignore "**/go.sum" \
	  -ignore "**/tmp/**" \
	  -ignore "**/jul-data/**" \
	  -ignore "**/*-golden.*" \
	  cmd/ internal/ examples/
