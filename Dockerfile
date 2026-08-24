# syntax=docker/dockerfile:1

# Base images are pinned by tag AND digest for reproducible, tamper-evident
# builds: the tag stays human-readable while the @sha256 digest fixes the exact
# multi-arch manifest. Dependabot (`docker` ecosystem in .github/dependabot.yml)
# bumps both on a schedule, so pins stay current without manual toil. To refresh
# by hand: `docker buildx imagetools inspect <image>:<tag>` and copy the digest.

# --- build stage ------------------------------------------------------------
FROM golang:1.27-alpine@sha256:4c9fe60190a2a3350ddc51de80d0224b8a6698d12bdfc999fee45ea9d6c46dbc AS build

WORKDIR /src

# Cache module downloads.
COPY go.mod go.sum ./
RUN go mod download

# Build a static binary (CGO disabled) for a minimal final image.
COPY . .
ARG VERSION=0.0.0-docker
# Feature build tags. Default includes "console" so the admin console (referenced
# by the /etc/jul editable-config story below) is present. Override for lean or
# full builds, e.g. --build-arg BUILD_TAGS="" (lean) or the full set:
#   --build-arg BUILD_TAGS="brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf"
ARG BUILD_TAGS="console"
RUN CGO_ENABLED=0 GOOS=linux go build \
    -tags "${BUILD_TAGS}" \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/jul ./cmd/jul

# Stage empty runtime state directories so they can be copied into the
# (shell-less) distroless image with the correct nonroot ownership.
RUN mkdir -p /seed/etc/jul /seed/var/lib/jul /seed/var/cache/jul /seed/var/log/jul

# --- runtime stage ----------------------------------------------------------
# distroless provides CA certificates and a nonroot user, with no shell.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab

COPY --from=build /out/jul /usr/local/bin/jul

# Writable runtime state, owned by the nonroot user (uid/gid 65532):
#   /etc/jul       editable config (admin console "Apply" + history rollback)
#   /var/lib/jul   config history snapshots
#   /var/cache/jul HTTP disk cache + ACME certificate cache
#   /var/log/jul   access-log "file" sink output
# These are declared as VOLUMEs so they persist across container restarts.
# Mount named volumes (or host paths) to retain config/history/certs/logs:
#   docker run -v jul-config:/etc/jul -v jul-state:/var/lib/jul \
#              -v jul-cache:/var/cache/jul -v jul-log:/var/log/jul ...
# For a named volume Docker seeds it from the image content on first use, so the
# baked server.toml below survives; for a bind mount, place server.toml yourself.
# Copy the staged tree as directory entities (not contents) in one chowned COPY
# so each created directory is owned by nonroot.
COPY --from=build --chown=nonroot:nonroot /seed/ /
# Container-tailored default config (admin enabled on loopback for the
# HEALTHCHECK; static root at /var/www) plus the placeholder site it serves, so
# the image starts cleanly and its health probe passes with no host mounts.
COPY --chown=nonroot:nonroot deploy/docker/server.toml /etc/jul/server.toml
COPY --chown=nonroot:nonroot deploy/docker/index.html /var/www/index.html
VOLUME ["/etc/jul", "/var/lib/jul", "/var/cache/jul", "/var/log/jul"]

# Document the default traffic and admin ports (adjust to your config).
EXPOSE 8080 8443 9090

# Liveness probe without a shell or curl (distroless ships neither): the binary
# probes its own admin /healthz and maps the result to an exit code. Uses the
# exec form (no shell) and --quiet so only the exit status is reported.
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD ["/usr/local/bin/jul", "healthcheck", "--config", "/etc/jul/server.toml", "--quiet"]

USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/jul"]
CMD ["--config", "/etc/jul/server.toml"]
