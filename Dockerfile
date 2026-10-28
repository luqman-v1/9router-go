# Stage 1: Build Frontend SPA
# Build on BUILDPLATFORM (native host architecture) to avoid emulating Bun under QEMU.
# Output in web/dist is static HTML/JS/CSS, architecture-agnostic, and shared across targets.
FROM --platform=$BUILDPLATFORM oven/bun:1-alpine AS web-builder
WORKDIR /app/web
COPY web/package.json web/bun.lock ./
RUN --mount=type=cache,target=/root/.bun/install/cache bun install --frozen-lockfile
COPY web/ ./
RUN bun run build

# Stage 2: Build Go Binary with Embedded Assets
# Build on BUILDPLATFORM using Go's native cross-compiler (CGO_ENABLED=0 GOOS/GOARCH)
# instead of running the Go toolchain inside slow QEMU software emulation.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
COPY --from=web-builder /app/web/dist ./web/dist

# Automatic Buildx multi-platform arguments
ARG TARGETOS
ARG TARGETARCH
# Central version — single source is VERSION file (and version.json for runtime)
ARG VERSION
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    VERSION=${VERSION:-$(cat VERSION 2>/dev/null || cat version.json | sed -n 's/.*"latestVersion": *"\([^"]*\)".*/\1/p')} && \
    echo "Building version $VERSION for ${TARGETOS:-linux}/${TARGETARCH:-amd64} on $BUILDPLATFORM" && \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -ldflags="-s -w -X '9router/proxy/internal/updater.CurrentVersion=${VERSION}'" -o 9router-go ./cmd/9router-go/

# Stage 3: Minimal runtime image for target platform
FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /app/9router-go /usr/local/bin/9router-go
EXPOSE 20130
ENTRYPOINT ["9router-go"]
