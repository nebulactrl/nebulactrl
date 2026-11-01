# syntax=docker/dockerfile:1
#
# NebulaCtrl control-plane image. Build context: repository root.
#
#   docker build -t nebulactrl:dev .
#   docker build --build-arg VERSION=1.2.3 -t nebulactrl:1.2.3 .
#
# Stages: web (bun) -> build (Go, web embedded, agents for amd64+arm64) -> runtime
# (Alpine with the PostgreSQL client tools, non-root). The image holds:
#   /usr/local/bin/nebulactrl-server                      control plane (+ embedded web UI)
#   /usr/share/nebulactrl/agent/nebulactrl-agent-linux-*  agents served at /agent/v1/download/{amd64|arm64}
#   /usr/share/nebulactrl/cli/<os>-<arch>/nebulactrl[.exe] + SHA256SUMS
#                                                         the CLI (linux, darwin, windows; amd64, arm64), served at /download/cli/{os}/{arch} and installed by /cli.sh
# Agents are installed on target hosts by /install-agent.sh, never run from this image.

# ---- 1. web ------------------------------------------------------------------
FROM oven/bun:1@sha256:1d22548f177f4ff38e0c15baa3c2c46c6c2060ba2d1bc573bb4720b407548f1d AS web
WORKDIR /src
# One bun workspace (apps/*, packages/*): the lockfile needs every member's manifest.
COPY package.json bun.lock ./
COPY apps/web/package.json apps/web/
COPY apps/site/package.json apps/site/
COPY packages/ascii/package.json packages/ascii/
COPY packages/dither/package.json packages/dither/
COPY packages/docs/package.json packages/docs/
COPY packages/tokens/package.json packages/tokens/
RUN --mount=type=cache,target=/root/.bun/install/cache \
    bun install --frozen-lockfile --filter @nebulactrl/web
COPY apps/web/ apps/web/
# The docs pipeline, the ASCII Eclipse, the dither kit and the design tokens shared with apps/site.
COPY packages/ascii/ packages/ascii/
COPY packages/dither/ packages/dither/
COPY packages/docs/ packages/docs/
COPY packages/tokens/ packages/tokens/
# The in-app docs compile docs/**/*.md and the OpenAPI spec at build time.
COPY docs/ docs/
COPY openapi/openapi.json openapi/openapi.json
ARG VERSION=dev
# Public site URL for the in-app docs' "Latest at" note; empty leaves the note out.
ARG SITE_URL=
# Repository URL for the in-app docs' "Edit this page" and repository links; empty hides them.
ARG GITHUB_URL=
WORKDIR /src/apps/web
RUN VITE_APP_VERSION=${VERSION} VITE_SITE_URL=${SITE_URL} VITE_GITHUB_URL=${GITHUB_URL} bun run build

# ---- 2. Go binaries ----------------------------------------------------------
FROM golang:1.27@sha256:2920bb124f1a2e12a1d39f0cab756ae0068441ee1da7655dec7f5e374944095b AS build
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
# internal/webui embeds dist/; replace whatever the context held with the fresh build.
RUN find internal/webui/dist -mindepth 1 ! -name .keep -delete
COPY --from=web /src/apps/web/dist/ internal/webui/dist/
# Releases: the installer this control plane serves (GET /install.sh) installs this
# control plane's own version. Skipped for non-release builds (dev, tests).
RUN case "${VERSION}" in [0-9]*.[0-9]*.[0-9]*) sed -i "s|^NC_DEFAULT_VERSION=.*|NC_DEFAULT_VERSION=${VERSION}|" deploy/install/install.sh ;; esac
ENV CGO_ENABLED=0
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    set -eux; \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/nebulactrl-server ./cmd/server; \
    for arch in amd64 arm64; do \
      GOOS=linux GOARCH=$arch go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
        -o /out/agent/nebulactrl-agent-linux-$arch ./cmd/agent; \
    done

# The CLI for every platform a user may run it on, served by the control plane at
# /download/cli/{os}/{arch} (checksums at /download/cli/SHA256SUMS, installer at
# /cli.sh). One directory per platform, SHA256SUMS as `sha256sum` prints it.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    set -eux; \
    for os in linux darwin windows; do \
      for arch in amd64 arm64; do \
        bin=nebulactrl; [ "$os" = windows ] && bin=nebulactrl.exe; \
        GOOS=$os GOARCH=$arch go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
          -o /out/cli/$os-$arch/$bin ./cmd/cli; \
      done; \
    done; \
    cd /out/cli && sha256sum */nebulactrl* > SHA256SUMS

# ---- 3. runtime --------------------------------------------------------------
# Alpine, not distroless: the control-plane backup runs pg_dump and
# `nebulactrl-server restore` runs pg_restore. The client major version must be
# at least the database server's (the compose file runs Postgres 17).
FROM alpine:3.24@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a
ARG VERSION=dev
LABEL org.opencontainers.image.title="NebulaCtrl control plane" \
      org.opencontainers.image.description="Self-hosted Docker-native PaaS control plane" \
      org.opencontainers.image.source="https://github.com/nebulactrl/nebulactrl" \
      org.opencontainers.image.version="${VERSION}"
RUN apk add --no-cache postgresql17-client \
    && addgroup -g 65532 -S nonroot \
    && adduser -u 65532 -S -G nonroot -H -h /nonexistent -s /sbin/nologin nonroot \
    && install -d -o 65532 -g 65532 -m 0700 /var/lib/nebulactrl/backups
COPY --from=build /out/nebulactrl-server /usr/local/bin/nebulactrl-server
COPY --from=build /out/agent/ /usr/share/nebulactrl/agent/
COPY --from=build /out/cli/ /usr/share/nebulactrl/cli/
ENV NEBULACTRL_LISTEN=:8080 \
    NEBULACTRL_AGENT_BINARIES_DIR=/usr/share/nebulactrl/agent \
    NEBULACTRL_CLI_BINARIES_DIR=/usr/share/nebulactrl/cli \
    NEBULACTRL_BACKUP_DIR=/var/lib/nebulactrl/backups
# Local control-plane backup destinations live here; mount a volume to keep them.
VOLUME /var/lib/nebulactrl/backups
USER 65532:65532
EXPOSE 8080
# The server probes itself (GET /readyz), so the check needs no curl.
HEALTHCHECK --interval=10s --timeout=5s --start-period=20s --retries=3 \
    CMD ["/usr/local/bin/nebulactrl-server", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/nebulactrl-server"]
CMD ["serve"]
