# Development and tests

Requirements: Go 1.27, libvips with headers, and for the DynamoDB
integration tests the [moto](https://github.com/getmoto/moto) server
(`pip install 'moto[server]'`; the tests look for `/opt/moto/bin/moto_server`
or `$MOTO_SERVER` and skip if it is missing). The SQLite tests need nothing
more: SQLite is compiled into the test binaries. Nothing in the tests or the
dev stack contacts AWS or Telegram.

```sh
go vet ./...
staticcheck ./...
go test -race ./...
go test ./internal/imaging -run '^$' -fuzz FuzzValidate -fuzztime 30s   # the JPEG header validator
```

- Unit tests cover decoding the bot's item shapes and SQLite rows, ordering and cursors, the
  Telegram client against a fake server (allowlist, redaction, 429, failure
  cache, size cap), the imaging pipeline, the cache and the HTTP server,
  including the access rules (view and admin sessions, both tokens under
  the limiter, public mode and what it must not reveal), and hiding images:
  the list on disk (atomic writes, a corrupt or unreadable file, concurrent
  changes), the listing with hidden images for both sources, and the admin
  routes for every kind of visitor, including cross-origin requests.
- `cmd/gallery/integration_test.go` builds the real binary and runs it
  against moto and a fake Telegram API with about 150 seeded items in 3 chats.
  `cmd/gallery/sqlite_integration_test.go` does the same with
  `GALLERY_SOURCE=sqlite`: the same records in a SQLite file, no AWS setting
  that works, the file locked, missing, and absent at start-up, and a check
  that the file and its directory are unchanged by the gallery. Both end by
  hiding and restoring images as an admin, across restarts of the binary, a
  wiped cache and a corrupt list (`cmd/gallery/hide_integration_test.go`).
- `testdata/telegram/` holds 8 real images exactly as Telegram served them
  (1024×1024 progressive JPEG, 142–348 KB). The imaging tests, the benchmark
  and the fake Telegram server all use them. `testdata/dynamodb/items.json`
  holds items in the bot's shapes.
- `testdata/sqlite/dalibot.db` is a database written by the bot's own
  `SqliteStore` (ordinary records, several styles, an imported picture, a
  variations album, an album note, a deleted record, a carried-over message,
  an expired row). `testdata/sqlite/make_fixture.py` makes it again from a
  checkout of the bot; the comment at its top has the command. It imports
  only the bot's `lib/store.py` and sends nothing anywhere.

**Dev stack.** One command runs moto, the seed, a fake Telegram API and the
real gallery:

```sh
scripts/dev-stack.sh                 # http://127.0.0.1:8080, control server on :8081
scripts/dev-stack.sh -port 9000 -auth=false          # public: GALLERY_PUBLIC=1, no login to view
scripts/dev-stack.sh -admin-token dev-admin-token-0123456789   # also an admin (GALLERY_ADMIN_TOKEN)
scripts/dev-stack.sh -admin-token dev-admin-token-0123456789 -state /tmp/gallery-state   # keep hidden images between runs
scripts/dev-stack.sh -source sqlite  # the same records in a SQLite file (GALLERY_SOURCE=sqlite); no moto
scripts/dev-stack.sh -h              # every option
```

When ready it prints one line of JSON with the URL, the source and the token
(`dev-gallery-token-0123456789`); with `-auth=false` it has `"public":true`
and no token, and with `-admin-token` it also has `"admin_token"`, which
signs in as an admin at the same `/login`. The cache and the list of hidden
images (`GALLERY_STATE_DIR`) are in a temporary directory removed on exit,
unless `-cache` or `-state` names another. The control server is for tests:
`curl -X POST 'http://127.0.0.1:8081/add?n=3'` writes three new records (they
appear after the next refresh, 5 s in the dev stack), and
`curl http://127.0.0.1:8081/state` reports counts and any Telegram request
other than the two allowed ones (`"forbidden"` must be `[]`).

**End to end.** `e2e/` holds Playwright (Chromium) tests. They start two dev
stacks themselves (one that needs the token, one public, both with an admin
token), on ports 18480 to 18483 unless `E2E_PORT` says otherwise, and need
Playwright with its Chromium installed: `cd e2e && npm test`.

With `-source sqlite` the database is a new file in the stack's temporary
directory, written the way the bot writes it; the dev stack never opens an
existing database. The browser tests run against the DynamoDB stack only.

**Container.** To run the image against the dev stack, start the stack, take
`AWS_ENDPOINT_URL_DYNAMODB`, `AWS_ENDPOINT_URL_DYNAMODB_STREAMS` and `TELEGRAM_API_BASE` from its gallery
process's environment, and run the image with `--network host`, those
variables, the dev token (as `GALLERY_TOKEN`) and bot token, `AWS_ACCESS_KEY_ID=testing`,
`AWS_SECRET_ACCESS_KEY=testing` and a free `GALLERY_LISTEN` port (to try
hiding, add `GALLERY_ADMIN_TOKEN` and `-v <volume>:/var/lib/dalibot-gallery`). With
`-source sqlite`, take `GALLERY_SQLITE_PATH` instead of the AWS values,
mount its directory read-only into the container and set
`GALLERY_SOURCE=sqlite` and the path as the container sees it.

`CONTRACTS.md` documents the interfaces between the packages and the HTTP API.
