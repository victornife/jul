# syntax=docker/dockerfile:1

# --- build stage ------------------------------------------------------------
FROM golang:1.24-alpine AS build

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

# --- runtime stage ----------------------------------------------------------
# distroless provides CA certificates and a nonroot user, with no shell.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/jul /usr/local/bin/jul
COPY server.toml /etc/jul/server.toml

# Document the default traffic and admin ports (adjust to your config).
EXPOSE 8080 8443 9090

USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/jul"]
CMD ["--config", "/etc/jul/server.toml"]
