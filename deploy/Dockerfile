# syntax=docker/dockerfile:1
#
# dalibot-gallery container image.
#
# Build from the repository root:
#
#   docker build -f deploy/Dockerfile -t dalibot-gallery .
#
# The binary links libvips through cgo and musl, so the build stage and the
# runtime stage must share one Alpine release: golang:1.27-alpine and alpine:3
# both follow the newest Alpine 3.x. Alpine packages libvips' heavier loaders
# (PDF, ImageMagick, HEIF, JPEG XL) separately; none of them is installed.
# SQLite (for GALLERY_SOURCE=sqlite) is compiled into the binary from the Go
# module's own copy of its source, so no SQLite package is needed in either stage.
# Rebuild regularly (--pull --no-cache) to pick up security updates to
# libvips, libwebp and libjpeg-turbo.

ARG GO_IMAGE=golang:1.27-alpine
ARG BASE_IMAGE=alpine:3

# ---- build: Go toolchain + C compiler + libvips headers ---------------------
FROM ${GO_IMAGE} AS build
RUN apk add --no-cache build-base pkgconf vips-dev
ENV GOTOOLCHAIN=local \
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
RUN apk add --no-cache ca-certificates vips \
 && addgroup -g 10001 -S gallery \
 && adduser -u 10001 -S -G gallery -H -h /nonexistent -s /sbin/nologin gallery \
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
