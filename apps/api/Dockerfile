# syntax=docker/dockerfile:1.7
#
# smart-erp Go services. One Dockerfile, one binary per image, selected by CMD:
#   docker buildx build --platform linux/amd64,linux/arm64 \
#     --build-arg CMD=api -t ghcr.io/jaichahal/smart-erp/api:<tag> apps/api
# CMD is one of: api, worker, scheduler, migrate, pushsink (cmd/<CMD>).
#
# Multi-arch: the builder runs on the build host's platform and cross-compiles
# for TARGETARCH (Go needs no emulation), so CI builds amd64 + arm64 in one go.
# Images are pinned by tag here; CI pins by digest when it publishes:
#   golang:1.27-alpine@sha256:...   gcr.io/distroless/static-debian12:nonroot@sha256:...

ARG GO_IMAGE=golang:1.27-alpine
ARG RUNTIME_IMAGE=gcr.io/distroless/static-debian12:nonroot

# ---------------------------------------------------------------------------
# Builder
# ---------------------------------------------------------------------------
FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS builder

ARG CMD
ARG TARGETOS=linux
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown

RUN test -n "${CMD}" || (echo "build arg CMD is required (api|worker|scheduler|migrate|pushsink)" && exit 1)

WORKDIR /src

# Module download is cached separately from the source tree.
COPY go.mod go.sum* ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

ENV CGO_ENABLED=0
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
      -o /out/app ./cmd/${CMD}

# ---------------------------------------------------------------------------
# Runtime: static distroless, non-root (uid 65532), no shell.
# ---------------------------------------------------------------------------
FROM ${RUNTIME_IMAGE}

ARG CMD
ARG VERSION=dev
LABEL org.opencontainers.image.title="smart-erp ${CMD}" \
      org.opencontainers.image.source="https://github.com/jaichahal/smart-erp" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.licenses="proprietary"

COPY --from=builder /out/app /app

USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/app"]
