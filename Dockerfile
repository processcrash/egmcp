# syntax=docker/dockerfile:1.7

# ─────────────────────────────────────────────────────────────────────────
# Stage 1 — build the React admin console
# ─────────────────────────────────────────────────────────────────────────
FROM node:20-alpine AS web

WORKDIR /web
COPY web/package.json web/package-lock.json* ./
RUN npm ci || npm install

COPY web/ ./
# Vite emits static files to web/dist by default. We mirror them into
# internal/server/assets so the backend can `go:embed` them.
RUN npm run build \
 && mkdir -p /out/assets \
 && cp -r dist/* /out/assets/

# ─────────────────────────────────────────────────────────────────────────
# Stage 2 — build the Go backend
# CGO=1 is required so we can use plugin.Open for third-party connectors
# (introduced in M6). Until then, a non-plugins-enabled image also works.
# The runtime base is glibc-based (debian-slim) to keep plugin compatibility
# identical to the build image.
# ─────────────────────────────────────────────────────────────────────────
FROM golang:1.22-bookworm AS go

WORKDIR /src

# Cache go module downloads.
COPY go.mod go.sum* ./
RUN go mod download

# Copy source.
COPY . .
COPY --from=web /out/assets/ ./internal/server/assets/

# Build a static binary. ldflags strip the symbol table and DWARF.
RUN CGO_ENABLED=1 go build \
      -ldflags "-s -w -X main.version=$(date -u +%Y%m%d) " \
      -o /out/egmcp ./cmd/egmcp

# ─────────────────────────────────────────────────────────────────────────
# Stage 3 — runtime image
# ─────────────────────────────────────────────────────────────────────────
FROM debian:bookworm-slim

RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates tzdata \
 && rm -rf /var/lib/apt/lists/* \
 && groupadd --system egmcp \
 && useradd --system --gid egmcp --home /data --shell /sbin/nologin egmcp

WORKDIR /app
COPY --from=go /out/egmcp /usr/local/bin/egmcp
COPY deploy/docker/entrypoint.sh /usr/local/bin/entrypoint.sh

# Default directories created/owned by the entrypoint.
RUN mkdir -p /data/configs /data/instances /data/plugins /data/logs \
 && chown -R egmcp:egmcp /data

USER egmcp
ENV EGMCP_CONFIG=/data/configs/admin.yaml \
    EGMCP_DATA_DIR=/data \
    EGMCP_INSTANCES_DIR=/data/instances \
    EGMCP_PLUGINS_DIR=/data/plugins \
    TZ=UTC

EXPOSE 8080
VOLUME ["/data"]

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
CMD ["egmcp"]

# Build-time metadata.
ARG GIT_SHA=unknown
ARG BUILD_DATE=unknown
LABEL org.opencontainers.image.title="egmcp" \
      org.opencontainers.image.description="Everything Go MCP — management plane for Model Context Protocol" \
      org.opencontainers.image.source="https://github.com/processcrash/egmcp" \
      org.opencontainers.image.revision="${GIT_SHA}" \
      org.opencontainers.image.created="${BUILD_DATE}"
