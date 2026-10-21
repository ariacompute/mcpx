# mcpx — MCP gateway for arbitrary HTTP services
#
# Stage 1 builds a static Go binary; stage 2 is the runtime image. There is no
# database and no writable state: the config file is the only thing mounted,
# and TLS is terminated by a fronting proxy, so the container speaks plain HTTP
# and compose publishes it on the host loopback.
#
# ---------------------------------------------------------------------------
# Build & run
# ---------------------------------------------------------------------------
# The build fetches one Go module (gopkg.in/yaml.v3), so it needs outbound
# network. Two equivalent flows:
#
#   1) One-shot compose build + run (recommended):
#        export MCPX_TOKEN=$(openssl rand -hex 24)
#        docker compose up -d --build
#
#   2) Manual image build, then compose up (reuses the image):
#        docker build \
#          --network=host \
#          --build-arg BUILD_PROXY=http://127.0.0.1:7897 \
#          -t mcpx:dev .
#        docker compose up -d
#
# `make docker-build` runs flow 2 and forwards the git build stamp.
#
# Build-time args (all optional):
#   * BUILD_PROXY  HTTP(S) proxy for `go mod download`. Use --network=host so
#                  the container's 127.0.0.1 reaches a host-side proxy; on the
#                  bridge network the Dockerfile retries with the proxy cleared.
#   * GOPROXY      Go module proxy (defaults to the goproxy.cn mirror).
#   * ALPINE_MIRROR
#                  Mirror root used by the runtime stage's apk add. Swap it when
#                  the Alpine CDN is unreachable, e.g. https://mirrors.aliyun.com.
#   * VERSION / COMMIT / DATE
#                  Build stamp reported by /healthz and `mcpx version`. `.git` is
#                  excluded by .dockerignore, so the values must be passed in;
#                  `make docker-build` and compose derive them from the checkout.
# ---------------------------------------------------------------------------

# ---------- build ----------
FROM golang:1.27-alpine AS build

ARG BUILD_PROXY=
ARG GOPROXY=https://goproxy.cn,direct

# .git is excluded from the context, so the stamp has to be passed in.
ARG VERSION=dev
ARG COMMIT=unknown
ARG DATE=unknown

ENV GOTOOLCHAIN=local \
    CGO_ENABLED=0 \
    GOPROXY=${GOPROXY} \
    GOFLAGS=-mod=mod \
    HTTP_PROXY=${BUILD_PROXY} \
    HTTPS_PROXY=${BUILD_PROXY} \
    http_proxy=${BUILD_PROXY} \
    https_proxy=${BUILD_PROXY}

WORKDIR /src

COPY go.mod go.sum ./
# The BUILD_PROXY usually points at 127.0.0.1 on the host, which does not
# resolve on the bridge network compose uses, so retry without it and fail with
# an actionable message instead of a dial timeout.
RUN go mod download || \
    env -u HTTP_PROXY -u HTTPS_PROXY -u http_proxy -u https_proxy go mod download || \
    { echo "go mod download failed: pass --build-arg BUILD_PROXY=<proxy> with --network=host, or --build-arg GOPROXY=<mirror>"; exit 1; }

COPY cmd ./cmd
COPY internal ./internal

RUN mkdir -p /out && \
    go build -trimpath \
    -ldflags="-s -w \
      -X mcpx/internal/version.Version=${VERSION} \
      -X mcpx/internal/version.Commit=${COMMIT} \
      -X mcpx/internal/version.Date=${DATE}" \
    -o /out/mcpx ./cmd/mcpx

# ---------- runtime ----------
FROM alpine:3.21

ARG BUILD_PROXY=
ARG ALPINE_MIRROR=https://dl-cdn.alpinelinux.org

# wget backs the healthcheck, ca-certificates and tzdata back outbound HTTPS and
# log timestamps. The proxy is passed on the command line, never as ENV: an ENV
# here would survive into the running container and proxy the gateway's own
# upstream calls.
RUN sed -i "s|https://dl-cdn.alpinelinux.org|${ALPINE_MIRROR}|g" /etc/apk/repositories && \
    ( http_proxy="${BUILD_PROXY}" https_proxy="${BUILD_PROXY}" \
        timeout 180 apk add --no-cache ca-certificates tzdata wget || \
      timeout 180 apk add --no-cache ca-certificates tzdata wget || \
      { echo "apk add failed: retry with --build-arg ALPINE_MIRROR=https://mirrors.aliyun.com (or https://mirrors.tuna.tsinghua.edu.cn)"; exit 1; } ) && \
    addgroup -g 10001 mcpx && \
    adduser -D -u 10001 -G mcpx mcpx && \
    mkdir -p /etc/mcpx

WORKDIR /app
COPY --from=build /out/mcpx /app/mcpx
# Container-ready example: listens on 0.0.0.0 and points at the demo upstream
# from docker-compose.yml. Compose mounts over it at the same path.
COPY deploy/mcpx.docker.yaml /etc/mcpx/mcpx.yaml

# The config path is the only required input; MCPX_TOKEN must be present when
# the mounted config uses server.auth.scheme=bearer.
ENV MCPX_CONFIG=/etc/mcpx/mcpx.yaml \
    TZ=UTC

USER mcpx
EXPOSE 8848

HEALTHCHECK --interval=15s --timeout=5s --start-period=5s --retries=5 \
    CMD wget -qO- http://127.0.0.1:8848/healthz >/dev/null || exit 1

ENTRYPOINT ["/app/mcpx"]
CMD ["serve"]
