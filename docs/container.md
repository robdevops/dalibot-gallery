# The container image

The quick start is in the [README](../README.md#quick-start-container). These are the details.

The image is built with Go 1.27 on `golang:1.27-alpine` and runs on
`alpine:3` (Alpine 3.24 with libvips 8.18.2 when this was written). Alpine
packages libvips' heavier loaders (PDF, ImageMagick, HEIF, JPEG XL)
separately and the image installs none of them, so it is about 125 MB,
against more than 300 MB on Debian or Ubuntu. Both images can be replaced
with the build arguments `GO_IMAGE` and `BASE_IMAGE`, but the build and
runtime stages must stay on the same Alpine release (the binary links musl
and libvips dynamically) and the package names in the Dockerfile are Alpine's.

**What has and has not been tested.** The image was built and run in the
development sandbox only (Docker 29, x86-64):

- Built from this Dockerfile, unchanged, but with `GO_IMAGE` and `BASE_IMAGE`
  set to copies of `golang:1.27-alpine` and `alpine:3` that add the sandbox
  proxy's certificate, because the sandbox has no direct internet access.
  The default build arguments (the plain images from Docker Hub) have not
  been built end to end.
- The Go tests were run inside the build stage (Go 1.27.4, musl, libvips
  8.18.2), except the integration test, which needs moto and ran on the
  host only (Ubuntu 24.04, libvips 8.15.1).
- Run with the `docker run` flags from the README's quick start against the dev stack (a local
  DynamoDB stand-in and a fake Telegram): healthy, uid 10001, read-only root,
  no capabilities, thumbnails and originals served, 15 MiB of the 512 MiB
  in use after warming 240 thumbnails, clean exit on `docker stop`.
- `deploy/compose.yaml` was brought up and its client-address handling
  checked through the published port with an earlier Ubuntu-based image;
  it has not been run again with the Alpine image. The nginx, Caddy and
  Traefik examples were each run in front of that earlier container before
  `GALLERY_TRUSTED_PROXIES` existed; they have not been run again since,
  and the Traefik address in `deploy/traefik-labels.yaml` is a placeholder
  you must change.
- Not tested: real AWS, the real Telegram API, certificate issuance, any
  other machine or architecture, the unit under a running systemd.

`/healthz` answers 503 until the first read of the table has succeeded, so
the container is "starting" or "unhealthy" while AWS is unreachable or the
credentials are wrong; the log says why. Traefik does not route to a
container that is not healthy.
