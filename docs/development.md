# Development and tests

Requirements: Go 1.27, libvips with headers, and for the integration tests
the [moto](https://github.com/getmoto/moto) server
(`pip install 'moto[server]'`; the tests look for `/opt/moto/bin/moto_server`
or `$MOTO_SERVER` and skip if it is missing). Nothing in the tests or the dev
stack contacts AWS or Telegram.

```sh
go vet ./...
staticcheck ./...
go test -race ./...
go test ./internal/imaging -run '^$' -fuzz FuzzValidate -fuzztime 30s   # the JPEG header validator
```

- Unit tests cover decoding the bot's item shapes, ordering and cursors, the
  Telegram client against a fake server (allowlist, redaction, 429, failure
  cache, size cap), the imaging pipeline, the cache and the HTTP server.
- `cmd/gallery/integration_test.go` builds the real binary and runs it
  against moto and a fake Telegram API with about 150 seeded items in 3 chats.
- `testdata/telegram/` holds 12 real images exactly as Telegram served them
  (1024×1024 progressive JPEG, 142–348 KB). The imaging tests, the benchmark
  and the fake Telegram server all use them. `testdata/dynamodb/items.json`
  holds items in the bot's shapes.

**Dev stack.** One command runs moto, the seed, a fake Telegram API and the
real gallery:

```sh
scripts/dev-stack.sh                 # http://127.0.0.1:8080, control server on :8081
scripts/dev-stack.sh -port 9000 -auth=false
scripts/dev-stack.sh -h              # every option
```

When ready it prints one line of JSON with the URL and the token
(`dev-gallery-token-0123456789`). The control server is for tests:
`curl -X POST 'http://127.0.0.1:8081/add?n=3'` writes three new records (they
appear after the next refresh, 5 s in the dev stack), and
`curl http://127.0.0.1:8081/state` reports counts and any Telegram request
other than the two allowed ones (`"forbidden"` must be `[]`).

**End to end.** `e2e/` holds Playwright (Chromium) tests. They start the dev
stack themselves, on port 18480 unless `E2E_PORT` says otherwise, and need
Playwright with its Chromium installed: `cd e2e && npm test`.

**Container.** To run the image against the dev stack, start the stack, take
`AWS_ENDPOINT_URL_DYNAMODB` and `TELEGRAM_API_BASE` from its gallery
process's environment, and run the image with `--network host`, those two
variables, the dev token and bot token, `AWS_ACCESS_KEY_ID=testing`,
`AWS_SECRET_ACCESS_KEY=testing` and a free `GALLERY_LISTEN` port.

`CONTRACTS.md` documents the interfaces between the packages and the HTTP API.
