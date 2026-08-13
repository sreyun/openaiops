# syntax=docker/dockerfile:1
# ============================================================================
# Multi-arch (linux/amd64 + linux/arm64) build with version injection.
# - BASE_REGISTRY defaults to the Huawei SWR mirror (fast for local/China builds);
#   CI passes BASE_REGISTRY=docker.io to use the official multi-arch base images.
# - VERSION is injected into main.appVersion (set to the git tag by the release CI).
# - The Go builds are cross-compiled on the native build host (CGO off), so an
#   arm64 image does NOT pay for QEMU emulation of the (slow) Go compile.
# ============================================================================
ARG BASE_REGISTRY=swr.cn-east-3.myhuaweicloud.com/sreyun/docker.io
# GO_VERSION 默认 1.22（SWR 镜像仓现只有此版本，保证本地构建可用）。
# Vitess sqlparser（SQL 工具）与安全修复需要 Go ≥1.26。
# SWR 若无对应标签：docker build --build-arg BASE_REGISTRY=docker.io --build-arg GO_VERSION=1.26 ...
ARG GO_VERSION=1.26

# ---- Build stage (always runs on the native BUILDPLATFORM) ----
FROM --platform=$BUILDPLATFORM ${BASE_REGISTRY}/golang:${GO_VERSION}-alpine AS builder
WORKDIR /src
# TARGETOS/TARGETARCH are provided automatically by buildx per target platform.
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
COPY go.mod go.sum ./
COPY vendor/ vendor/
COPY shared/ shared/
COPY cmd/ cmd/
# 将配置示例复制到 cmd/agent/ 以供 go:embed 内嵌（.gitignore 排除，构建时复制）
COPY config.example.yaml cmd/agent/config_example.yaml

# Server + agent binaries for THIS image's target arch (native cross-compile).
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH GOFLAGS=-mod=vendor \
    go build -ldflags="-s -w -X main.appVersion=${VERSION}" -o /out/aiops-server ./cmd/server
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH GOFLAGS=-mod=vendor \
    go build -ldflags="-s -w -X main.appVersion=${VERSION}" -o /out/aiops-agent  ./cmd/agent

# All-platform agent binaries for the one-click install distribution (served by the
# server via /dl/, so they must cover every platform regardless of the image arch).
RUN set -e; LD="-s -w -X main.appVersion=${VERSION}"; \
    GOOS=linux   GOARCH=amd64 GOFLAGS=-mod=vendor go build -ldflags="$LD" -o /out/dist/aiops-agent-linux-amd64           ./cmd/agent; \
    GOOS=linux   GOARCH=arm64 GOFLAGS=-mod=vendor go build -ldflags="$LD" -o /out/dist/aiops-agent-linux-arm64           ./cmd/agent; \
    GOOS=linux   GOARCH=loong64 GOFLAGS=-mod=vendor go build -ldflags="$LD" -o /out/dist/aiops-agent-linux-loong64       ./cmd/agent; \
    GOOS=linux   GOARCH=riscv64 GOFLAGS=-mod=vendor go build -ldflags="$LD" -o /out/dist/aiops-agent-linux-riscv64       ./cmd/agent; \
    GOOS=linux   GOARCH=386   GOFLAGS=-mod=vendor go build -ldflags="$LD" -o /out/dist/aiops-agent-linux-386             ./cmd/agent; \
    GOOS=linux   GOARCH=arm GOARM=7 GOFLAGS=-mod=vendor go build -ldflags="$LD" -o /out/dist/aiops-agent-linux-arm       ./cmd/agent; \
    GOOS=darwin  GOARCH=amd64 GOFLAGS=-mod=vendor go build -ldflags="$LD" -o /out/dist/aiops-agent-darwin-amd64          ./cmd/agent; \
    GOOS=darwin  GOARCH=arm64 GOFLAGS=-mod=vendor go build -ldflags="$LD" -o /out/dist/aiops-agent-darwin-arm64          ./cmd/agent; \
    GOOS=windows GOARCH=amd64 GOFLAGS=-mod=vendor go build -ldflags="$LD" -o /out/dist/aiops-agent.exe                   ./cmd/agent; \
    GOOS=windows GOARCH=arm64 GOFLAGS=-mod=vendor go build -ldflags="$LD" -o /out/dist/aiops-agent-windows-arm64.exe     ./cmd/agent

# Optional agent-binary compression. UPX roughly halves the /dl download (7.5MB -> ~3MB).
# DEFAULT OFF: a packed binary can trip AV/EDR heuristics, and for a security-monitoring
# agent a false-positive quarantine (agent deleted, monitoring dies) is worse than 4MB.
# Enable with:  docker build --build-arg UPX=1 ...
# Only linux-amd64 + windows-amd64 are packed — UPX Mach-O(darwin) support is unreliable,
# and arm64 packing is best avoided; those stay uncompressed.
ARG UPX=0
RUN if [ "$UPX" = "1" ]; then \
      apk add --no-cache upx && \
      upx --best --lzma /out/dist/aiops-agent-linux-amd64 /out/dist/aiops-agent.exe && \
      echo "[build] UPX applied to linux-amd64 + windows-amd64"; \
    else echo "[build] UPX skipped (opt-in via --build-arg UPX=1)"; fi

# Legacy Windows Server 2012 / 2012 R2 Agent (Go 1.20). Modern Go ≥1.21 binaries
# refuse to start on those kernels; install.ps1 selects this artifact automatically.
# Pin docker.io — SWR golang mirrors often lack the 1.20 tag.
# Module pins live in scripts/win2012/ (must keep a pre-graduation x/exp that still
# ships golang.org/x/exp/slog — later revisions 404 or drop the package).
FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.20-bullseye AS win2012-builder
WORKDIR /src
ARG VERSION=dev
ENV GOPROXY=https://proxy.golang.org,https://goproxy.cn,direct
COPY shared/ shared/
COPY cmd/agent/ cmd/agent/
COPY config.example.yaml cmd/agent/config_example.yaml
COPY scripts/win2012/go.mod scripts/win2012/go.sum scripts/win2012/rewrite_slog.sh ./
RUN set -e; \
    bash ./rewrite_slog.sh ./cmd/agent; \
    go mod download; \
    CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
      go build -trimpath -ldflags="-s -w -X main.appVersion=${VERSION}" \
      -o /out/aiops-agent-windows-amd64-win2012.exe ./cmd/agent

# ---- Server image ----
FROM ${BASE_REGISTRY}/alpine:3.23 AS server
ARG TARGETARCH
ARG NUCLEI_VERSION=3.3.9
# postgresql18-client：备份/还原依赖 pg_dump / pg_restore；客户端版本必须 ≥ 数据库版本
# （默认 pgvector:pg18），高版本客户端可向下兼容 dump 旧版服务端。
ARG PG_CLIENT_PKG=postgresql18-client
RUN apk add --no-cache ca-certificates tzdata zip curl unzip ${PG_CLIENT_PKG} \
 && curl -fsSL "https://github.com/projectdiscovery/nuclei/releases/download/v${NUCLEI_VERSION}/nuclei_${NUCLEI_VERSION}_linux_${TARGETARCH}.zip" -o /tmp/nuclei.zip \
 && unzip -j /tmp/nuclei.zip nuclei -d /usr/local/bin \
 && chmod +x /usr/local/bin/nuclei \
 && rm -f /tmp/nuclei.zip \
 && nuclei -version || true
WORKDIR /app

COPY --from=builder /out/aiops-server /app/aiops-server
COPY --from=builder /out/dist/ /app/dist/
COPY --from=win2012-builder /out/aiops-agent-windows-amd64-win2012.exe /app/dist/aiops-agent-windows-amd64-win2012.exe
COPY config.example.yaml /app/dist/config.example.yaml
COPY plugins/ /tmp/plugins/
RUN cd /tmp && zip -r /app/dist/plugins.zip plugins/ && rm -rf /tmp/plugins \
 && sha256sum /app/dist/aiops-agent-windows-amd64-win2012.exe | awk '{print $1"  aiops-agent-windows-amd64-win2012.exe"}' \
      > /app/dist/aiops-agent-windows-amd64-win2012.exe.sha256

EXPOSE 8529
HEALTHCHECK --interval=15s --timeout=5s --retries=3 CMD wget -qO- http://127.0.0.1:8529/healthz || exit 1
VOLUME ["/app/data"]

# 备份目录落盘到持久化卷，避免容器重建后丢失
ENV AIOPS_BACKUP_DIR=/app/data/backups
ENTRYPOINT ["/app/aiops-server"]
CMD ["-addr", ":8529", "-config", "/app/data/server_config.json", "-dist", "/app/dist"]

# ---- Agent image ----
# Demo-safe default: long-running process is unprivileged uid 10001 (aiops).
# Entrypoint may start as root solely to fix volume ownership, then su-exec drops.
FROM ${BASE_REGISTRY}/alpine:3.20 AS agent
RUN apk add --no-cache ca-certificates tzdata python3 py3-pip su-exec \
 && addgroup -S -g 10001 aiops \
 && adduser -S -u 10001 -G aiops -h /home/aiops -s /bin/sh aiops \
 && mkdir -p /home/aiops \
 && chown -R aiops:aiops /home/aiops
WORKDIR /app

COPY --from=builder /out/aiops-agent /app/aiops-agent
COPY plugins/ /app/plugins/
COPY docker/agent-entrypoint.sh /entrypoint.sh

RUN pip3 install --no-cache-dir --break-system-packages psutil 2>/dev/null || true \
 && mkdir -p /app/data \
 && chown -R aiops:aiops /app \
 && chmod 0755 /entrypoint.sh /app/aiops-agent

ENTRYPOINT ["/entrypoint.sh"]
# Server/token 由 compose 注入 AIOPS_SERVER / AIOPS_TOKEN / AIOPS_TOKEN_FILE
CMD []
