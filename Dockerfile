# ---- Build stage -----------------------------------------------------------
FROM golang:1.23-bookworm AS build

WORKDIR /src

# Dependencies are vendored (vendor/) and committed to the repository, so the
# build never needs outbound network access to proxy.golang.org. This also
# keeps builds reproducible and fast behind restrictive/offline Docker hosts.
COPY go.mod go.sum ./
COPY vendor/ vendor/
COPY . .

# CGO_ENABLED=0 with a pure-Go sqlite driver (modernc.org/sqlite) produces a
# fully static binary with no CGO/libc dependency, so the runtime image can
# be a minimal distroless/scratch-like base.
RUN CGO_ENABLED=0 GOOS=linux go build \
    -mod=vendor \
    -trimpath \
    -ldflags="-s -w -X main.version=$(git describe --tags --always --dirty 2>/dev/null || echo docker)" \
    -o /out/dbsync \
    ./cmd/dbsync

# Build the runtime CA bundle. Starts from the builder's standard public CA
# bundle, and appends any PEM certificates found in certs/ (e.g. a corporate
# TLS-inspection root CA such as Zscaler, required on networks that
# intercept outbound HTTPS). certs/ is empty by default and gitignored for
# its actual certificate contents -- see certs/README.md. This step never
# fails even if certs/ contains no *.pem files.
RUN cp /etc/ssl/certs/ca-certificates.crt /out/ca-certificates.crt \
    && for f in certs/*.pem; do \
         [ -f "$f" ] && cat "$f" >> /out/ca-certificates.crt; \
       done; \
    true

# ---- Runtime stage ----------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot AS runtime

# The distroless "nonroot" variant already runs as uid/gid 65532, avoiding
# the need to run as root at any point in the final image.
WORKDIR /app

COPY --from=build /out/dbsync /usr/local/bin/dbsync
# Overwrite the default CA bundle with the (possibly extended) one built
# above, so any corporate root CA in certs/*.pem is trusted by outbound
# HTTPS calls to the Dropbox API.
COPY --from=build /out/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

# Persistent state (SQLite database + WAL files) lives here; mount a volume
# at this path so synchronization history survives container restarts.
VOLUME ["/app/state"]

ENV DBSYNC_CONFIG=/app/config.yaml

# No network ports are published: dbsync only makes outbound HTTPS calls to
# Dropbox. The one-time `auth login` flow (which needs an inbound loopback
# callback) is intended to be run on the host machine with
# `go run ./cmd/dbsync auth login`, not inside this container -- see
# README.md for the full walkthrough and the --manual fallback.

ENTRYPOINT ["/usr/local/bin/dbsync"]
CMD ["run"]
