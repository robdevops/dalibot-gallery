#!/bin/sh
# Runs the whole gallery locally, with no AWS account and no Telegram bot:
# a moto DynamoDB server seeded with about 150 items in the bot's real shapes
# (or, with -source sqlite, a SQLite database holding the same records as the
# bot's self-hosted mode stores them), a fake Telegram Bot API that serves the
# sample JPEGs in testdata/telegram, and the real gallery binary pointed at both.
#
# Usage (from anywhere):
#
#   scripts/dev-stack.sh                          gallery on http://127.0.0.1:8080
#   scripts/dev-stack.sh -port 9000               another port (control server on 9001)
#   scripts/dev-stack.sh -auth=false              public: no login to view (GALLERY_PUBLIC=1)
#   scripts/dev-stack.sh -admin-token dev-admin-token-0123456789
#                                                 also an admin, who signs in at /login
#                                                 and can hide images (docs/admin.md)
#   scripts/dev-stack.sh -state /tmp/gallery-state
#                                                 keep the list of hidden images between runs
#   scripts/dev-stack.sh -source sqlite           read a SQLite database instead of DynamoDB
#                                                 (GALLERY_SOURCE=sqlite); needs no moto
#   scripts/dev-stack.sh -telegram-delay 300ms    imitate a slow Telegram
#   scripts/dev-stack.sh -h                       every option
#
# When ready it prints one line of JSON on stdout (logs go to stderr), e.g.
#   {"control":"http://127.0.0.1:8081","ready":true,"records":146,"source":"dynamodb",
#    "tiles":227,"token":"dev-gallery-token-0123456789","url":"http://127.0.0.1:8080"}
# Open the url and sign in with the token. Stop with Ctrl-C. With -auth=false
# the line has "public":true and no token; with -admin-token it also has
# "admin_token".
#
# For tests, the control server adds images and reports what Telegram saw:
#   curl -X POST 'http://127.0.0.1:8081/add?n=3'   three new records; they show
#                                                  after the next refresh (-refresh, default 5s)
#   curl http://127.0.0.1:8081/state               counts, and "forbidden" Telegram calls (must be [])
#
# Needs: Go, libvips with headers, and (unless -source sqlite) moto's server at
# /opt/moto/bin/moto_server or $MOTO_SERVER (pip install 'moto[server]').
# Nothing here contacts AWS or Telegram, or opens an existing database file.
set -eu
cd "$(dirname "$0")/.."
# Build first and exec the result (not "go run"), so signals reach the stack itself.
bin="${TMPDIR:-/tmp}/dalibot-gallery-devstack-$(id -u)"
mkdir -p "$bin"
GOTOOLCHAIN="${GOTOOLCHAIN:-local}" go build -o "$bin/devstack" ./scripts/devstack
exec "$bin/devstack" "$@"
