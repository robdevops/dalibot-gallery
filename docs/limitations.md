# Limitations and out of scope

- **Images disappear with the bot's TTL.** When DynamoDB expires a record
  (90 days after its last write by default), its tiles and cached files go too.
- **Read-only.** No uploads, likes, comments, search, or writing to the table.
- **One shared token.** No users or roles, and no per-session sign-out.
  Many addresses failing together can block sign-ins for a while; see
  [Security model](security.md).
- **DynamoDB only.** The bot's self-hosted SQLite mode is not supported (the
  listing sits behind an interface so a reader could be added).
- **JPEG originals only**, which is what Telegram serves for photos. Anything
  else shows the placeholder. Photo-variation albums are not stored by the
  bot, so they cannot appear.
- **WebP thumbnails only**; no AVIF or JPEG XL.
- "Newest" means most recently drawn: adding a style to an old message brings
  its tiles back to the top. The date is derived from the TTL, so it is only
  right if `GALLERY_STORE_DAYS` matches the bot.
- **Older pictures can be imported.** The bot cannot read a chat's history,
  but pictures it made before it kept records can be forwarded to it in a
  private chat (see the bot's README). The bot stores each with a `created`
  date, and the gallery shows it at that date instead of at the top. An
  imported picture belongs to the private chat it was forwarded in, expires
  like any other record, is labelled with the bot's default command whatever
  model made it, and appears twice if the bot also has an ordinary
  record of the same picture under a different Telegram file id.
- With `GALLERY_AUTH=off`, image responses are cacheable as `public`; see [Reverse proxy](../README.md#reverse-proxy).
- Responses that Go's HTTP server writes itself for malformed requests carry
  no security headers; see [Security model](security.md).
- Rebuild the container image often to pick up security updates to Alpine,
  libvips and its codecs.
- The whole table is scanned on every refresh and the listing is held in
  memory; this suits a table bounded by a TTL, not an unbounded one.
