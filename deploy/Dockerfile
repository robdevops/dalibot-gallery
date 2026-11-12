# syntax=docker/dockerfile:1
#
# dalibot-gallery container image.
#
# Build from the repository root:
#
#   docker build -f deploy/Dockerfile -t dalibot-gallery .
#
# The binary links libvips through cgo, so the build stage and the runtime
# stage must share one distribution release: both use BASE_IMAGE. The Go
# toolchain is copied out of the official golang image.
#
# Ubuntu 24.04 ships libvips 8.15.1, the version the test suite and BENCH.md
# were run against. Rebuild regularly (--pull --no-cache) to pick up security
# updates to libvips, libwebp and libjpeg-turbo.

ARG GO_IMAGE=golang:1.24
ARG BASE_IMAGE=ubuntu:24.04

FROM ${GO_IMAGE} AS go

# ---- build: Go toolchain + libvips headers ----------------------------------
FROM ${BASE_IMAGE} AS build
RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      ca-certificates gcc libc6-dev pkg-config libvips-dev \
 && rm -rf /var/lib/apt/lists/*
COPY --from=go /usr/local/go /usr/local/go
ENV PATH=/usr/local/go/bin:$PATH \
    GOTOOLCHAIN=local \
    CGO_ENABLED=1 \
    GOFLAGS=-mod=readonly
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY cmd ./cmd
COPY internal ./internal
COPY web ./web
RUN go build -trimpath -ldflags="-s -w" -o /out/gallery ./cmd/gallery \
 && /out/gallery -version

# ---- runtime: libvips shared library only, no compiler, no shell tools added -
FROM ${BASE_IMAGE}
RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      ca-certificates libvips42t64 \
 && rm -rf /var/lib/apt/lists/* \
 && groupadd --gid 10001 gallery \
 && useradd --uid 10001 --gid 10001 --no-create-home --home-dir /nonexistent \
      --shell /usr/sbin/nologin gallery \
 && install -d -o 10001 -g 10001 -m 0700 /var/cache/dalibot-gallery
COPY --from=build /out/gallery /usr/local/bin/gallery

# Inside the container the gallery listens on every interface; publish the port
# only to the reverse proxy (for example -p 127.0.0.1:8080:8080).
ENV GALLERY_LISTEN=0.0.0.0:8080 \
    GALLERY_CACHE_DIR=/var/cache/dalibot-gallery

USER 10001:10001
# The only path the gallery writes to. Everything else may be read-only.
VOLUME /var/cache/dalibot-gallery
EXPOSE 8080

# The binary checks its own /healthz, so the image needs no curl or wget.
HEALTHCHECK --interval=30s --timeout=6s --start-period=30s --retries=3 \
  CMD ["/usr/local/bin/gallery", "-healthcheck"]

ENTRYPOINT ["/usr/local/bin/gallery"]
