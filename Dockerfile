# syntax=docker/dockerfile:1

# --- build stage ------------------------------------------------------------
FROM golang:1.26.4-alpine AS build

WORKDIR /src

# Cache module downloads.
COPY go.mod go.sum ./
RUN go mod download

# Build a static binary (CGO disabled) for a minimal final image.
COPY . .
ARG VERSION=0.0.0-docker
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/jul ./cmd/jul

# Stage empty runtime state directories so they can be copied into the
# (shell-less) distroless image with the correct nonroot ownership.
RUN mkdir -p /seed/etc/jul /seed/var/lib/jul /seed/var/cache/jul /seed/var/log/jul

# --- runtime stage ----------------------------------------------------------
# distroless provides CA certificates and a nonroot user, with no shell.
FROM gcr.io/distroless/static-debian12:nonroot

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
COPY --chown=nonroot:nonroot server.toml /etc/jul/server.toml
VOLUME ["/etc/jul", "/var/lib/jul", "/var/cache/jul", "/var/log/jul"]

# Document the default traffic and admin ports (adjust to your config).
EXPOSE 8080 8443 9090

USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/jul"]
CMD ["--config", "/etc/jul/server.toml"]
