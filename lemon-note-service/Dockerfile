# Lemon Note 服务镜像：前端构建 → Go 构建（纯 Go，关闭 CGO）→ distroless 运行镜像（非 root）。
# 构建上下文是仓库根目录：
#   docker build -f lemon-note-service/Dockerfile -t lemon-note .
# 运行：
#   docker run -d -p 8686:8686 -v lemon-note-data:/data -v lemon-note-cache:/cache lemon-note
# 数据目录 /data（全部数据，务必挂卷并备份），缓存目录 /cache（可删，启动时自动重建），监听 :8686。
# 拉官方镜像慢时可以换基础镜像，例如 --build-arg NODE_IMAGE=docker.1panel.live/library/node:22-alpine。

ARG NODE_IMAGE=node:22-alpine
ARG GO_IMAGE=golang:1.26-alpine
ARG RUNTIME_IMAGE=gcr.io/distroless/static-debian12:nonroot

# ---------- 前端 ----------
FROM --platform=$BUILDPLATFORM ${NODE_IMAGE} AS console
WORKDIR /src/lemon-note-console
# 锁文件里的下载地址可能是国内镜像，统一换成当前配置的源（默认 npm 官方源）
ARG NPM_REGISTRY=https://registry.npmjs.org/
RUN npm config set registry "$NPM_REGISTRY" && npm config set replace-registry-host always
COPY lemon-note-console/package.json lemon-note-console/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci --no-audit --no-fund
COPY lemon-note-console/ ./
RUN npm run build

# ---------- 服务 ----------
FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS service
ARG TARGETOS TARGETARCH TARGETVARIANT
ARG VERSION=docker
ARG COMMIT=
ARG BUILD_DATE=
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=$GOPROXY GOTOOLCHAIN=local
WORKDIR /src/lemon-note-service
COPY lemon-note-service/go.mod lemon-note-service/go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY lemon-note-service/ ./
COPY --from=console /src/lemon-note-console/dist/ ./internal/web/dist/
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    GOARM="${TARGETVARIANT#v}" CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -o /out/lemon-note \
      -ldflags "-s -w -X github.com/LemonITCN/lemon-note/lemon-note-service/internal/version.Version=${VERSION} \
        -X github.com/LemonITCN/lemon-note/lemon-note-service/internal/version.Commit=${COMMIT} \
        -X github.com/LemonITCN/lemon-note/lemon-note-service/internal/version.Date=${BUILD_DATE}" \
      ./cmd/lemon-note
# distroless 里没有 shell，数据、缓存与引导配置目录在这里建好，交给非 root 用户（65532）
RUN mkdir -p /out/data /out/cache /out/config

# ---------- 运行 ----------
FROM ${RUNTIME_IMAGE}
COPY --from=service /out/lemon-note /usr/local/bin/lemon-note
COPY --from=service --chown=65532:65532 /out/data /data
COPY --from=service --chown=65532:65532 /out/cache /cache
COPY --from=service --chown=65532:65532 /out/config /config
# 引导配置文件在 /config/lemon-note/lemon-note.toml；容器里建议直接用环境变量配置（外部数据库用 LEMON_NOTE_DSN）
ENV LEMON_NOTE_DATA_DIR=/data \
    LEMON_NOTE_CACHE_DIR=/cache \
    LEMON_NOTE_LISTEN=:8686 \
    XDG_CONFIG_HOME=/config
USER 65532:65532
VOLUME ["/data", "/cache"]
EXPOSE 8686
ENTRYPOINT ["/usr/local/bin/lemon-note"]
CMD ["serve"]
