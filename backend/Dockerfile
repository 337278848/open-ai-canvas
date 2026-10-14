# syntax=docker/dockerfile:1.7

FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS payment-package-build

ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=$GOPROXY
# 部分镜像源（goproxy.cn / 阿里云 goproxy 等）会主动断开 Go 客户端的 HTTP/2 连接，
# 表现为 “http2: server sent GOAWAY”，导致依赖下载随机失败且重试不稳定。
ENV GODEBUG=http2client=0

RUN apk add --no-cache nodejs unzip zip

WORKDIR /src
# 插件只下载实际依赖，不为四个小程序预取整个业务后端的模块图。
COPY backend ./backend
COPY plugin-packages ./plugin-packages
ARG TARGETOS
ARG TARGETARCH
# 模块与编译缓存都挂到 BuildKit 缓存上：首次构建下载后，后续构建直接复用，
# 既避开镜像源抖动，也不再把几 GB 依赖写进普通镜像层。
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build set -eu; \
    test -n "$TARGETOS"; \
    test -n "$TARGETARCH"; \
    PAYMENT_PLUGIN_GOOS="$TARGETOS" \
    PAYMENT_PLUGIN_GOARCH="$TARGETARCH" \
    PAYMENT_PLUGIN_CGO_ENABLED=0 \
      ./plugin-packages/build-packages.sh --payments-only; \
    sh ./plugin-packages/verify-payment-packages.sh "$TARGETOS" "$TARGETARCH"

FROM alpine:3.22 AS payment-package-smoke

ARG TARGETOS
ARG TARGETARCH
RUN apk add --no-cache unzip
COPY --from=payment-package-build /src/plugin-packages /app/plugin-packages
RUN PAYMENT_PLUGIN_SMOKE_TEST=1 \
    sh /app/plugin-packages/verify-payment-packages.sh "$TARGETOS" "$TARGETARCH"

FROM golang:1.25-alpine AS backend-build

ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=$GOPROXY
# 与 payment-package-build 相同：禁用 HTTP/2，避免镜像源 GOAWAY 造成随机失败。
ENV GODEBUG=http2client=0

RUN apk add --no-cache build-base

WORKDIR /src/backend
COPY backend/go.mod backend/go.sum ./
# go.mod 含本地 replace（github.com/casdoor/go-sms-sender => ./third_party/go-sms-sender）。
# go mod download 解析依赖时就需要读到该目录的 go.mod，而这行执行时完整源码尚未复制，
# 缺了它会报 "reading third_party/go-sms-sender/go.mod: no such file or directory"。
# 后面 COPY backend ./ 会覆盖为完整源码，这里只为让依赖解析通过。
COPY backend/third_party ./third_party
# 模块缓存在 BuildKit 缓存里，不进镜像层：重复构建不再重新下载整个依赖图。
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY backend ./
COPY VERSION /src/VERSION
ARG BUILD_VERSION
ARG BUILD_COMMIT=unknown
ARG BUILD_TIME=unknown
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build set -eu; \
    version="${BUILD_VERSION:-$(tr -d '\r\n' </src/VERSION)}"; \
    ldflags="-s -w -X yingce/backend/internal/buildinfo.Version=${version} -X yingce/backend/internal/buildinfo.Commit=${BUILD_COMMIT} -X yingce/backend/internal/buildinfo.BuildTime=${BUILD_TIME}"; \
    CGO_ENABLED=1 GOOS=linux go build -trimpath -ldflags="$ldflags" -o /out/yingce-backend ./cmd/server; \
    CGO_ENABLED=1 GOOS=linux go build -trimpath -ldflags="$ldflags" -o /out/migrate-schema ./cmd/migrate-schema; \
    CGO_ENABLED=1 GOOS=linux go build -trimpath -ldflags="$ldflags" -o /out/migrate-sqlite-postgres ./cmd/migrate-sqlite-postgres

FROM node:22.19-alpine AS pi-runtime-deps

WORKDIR /app/backend/agent-runtime/pi
COPY backend/agent-runtime/pi/package.json backend/agent-runtime/pi/package-lock.json ./
RUN npm ci --omit=dev --ignore-scripts

FROM node:22.19-alpine

RUN apk add --no-cache ca-certificates tzdata wget \
    && addgroup -S app \
    && adduser -S app -G app

WORKDIR /app
ENV GIN_MODE=release
ENV CANVAS_BACKEND_ADDR=:8080
ENV CANVAS_BACKEND_DATA_DIR=/data

RUN mkdir -p /data && chown -R app:app /data
COPY --from=backend-build /out/yingce-backend /usr/local/bin/yingce-backend
COPY --from=backend-build /out/migrate-schema /usr/local/bin/migrate-schema
COPY --from=backend-build /out/migrate-sqlite-postgres /usr/local/bin/migrate-sqlite-postgres
COPY --from=payment-package-smoke /app/plugin-packages /app/plugin-packages
COPY --from=pi-runtime-deps /app/backend/agent-runtime/pi/node_modules /app/backend/agent-runtime/pi/node_modules
COPY backend/agent-runtime/pi/package.json backend/agent-runtime/pi/package-lock.json /app/backend/agent-runtime/pi/
COPY backend/agent-runtime/pi/agent-runtime.mjs /app/backend/agent-runtime/pi/agent-runtime.mjs

USER app
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 CMD wget -qO- http://127.0.0.1:8080/api/health/ready >/dev/null || exit 1
CMD ["yingce-backend"]
