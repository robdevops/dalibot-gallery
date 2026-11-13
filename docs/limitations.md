# Limitations and out of scope

- **Images disappear with the bot's TTL.** When a record expires (90 days
  after its last write by default), its tiles and cached files go too.
- **Read-only.** No uploads, likes, comments, search, or writing to the
  bot's table or database file. The one thing an admin can change is the
  gallery's own list of hidden images; see [Hiding images](admin.md).
- **One shared token.** No users or roles, and no per-session sign-out.
  Many addresses failing together can block sign-ins for a while; see
  [Security model](security.md).
- **One source at a time.** A gallery reads either the DynamoDB table or one
  SQLite file, not both and not several.
- **SQLite: same machine, local disk.** The bot's database file must be on
  the gallery's machine; a network file system is not supported. A database
  in WAL mode is refused, and after the bot was killed in the middle of a
  write the gallery keeps its last listing until the bot has opened the
  database again. Choosing an earlier style in Telegram moves that record to
  the top of the gallery, which it does not do with DynamoDB. See
  [SQLite](sqlite.md).
- **JPEG originals only**, which is what Telegram serves for photos. Anything
  else shows the placeholder. Photo-variation albums appear only once the bot
  records them: bot versions before that change did not store them.
- **WebP thumbnails only**; no AVIF or JPEG XL.
- "Newest" means most recently drawn: adding a style to an old message brings
  its tiles back to the top. With DynamoDB the date is derived from the TTL,
  so it is only right if `GALLERY_STORE_DAYS` matches the bot.
- **Older pictures can be imported.** The bot cannot read a chat's history,
  but pictures it made before it kept records can be forwarded to it in a
  private chat (see the bot's README). The bot stores each with a `created`
  date, and the gallery shows it at that date instead of at the top. An
  imported picture belongs to the private chat it was forwarded in, expires
  like any other record, is labelled with the bot's default command whatever
  model made it, and appears twice if the bot also has an ordinary
  record of the same picture under a different Telegram file id.
- With `GALLERY_PUBLIC=1`, image responses are cacheable as `public`; see [Reverse proxy](reverse-proxy.md).
- There is one view token and one admin token, no accounts. Signing in as
  admin replaces a view session in the same browser, and signing out drops
  whichever session there is.
- The "Admin" and "Sign out" links are at the foot of the page, below the
  last image, so in a long gallery they are a long scroll away. `/login` can
  be opened directly.
- **Hiding only hides.** A hidden image stays in the bot's records and in
  Telegram; delete it with the bot's `#delete`. Pages already open in other
  browsers keep its tile until they are reloaded (its image no longer
  loads), and a shared cache in front of a public gallery may keep serving
  a copy it already holds. The list is read at start-up only, so edit
  `hidden.json` by hand only while the gallery is stopped. Hiding works one
  image at a time. See [Hiding images](admin.md).
- Responses that Go's HTTP server writes itself for malformed requests carry
  no security headers; see [Security model](security.md).
- Rebuild the container image often to pick up security updates to Alpine,
  libvips and its codecs.
- The whole table (or every row of the SQLite database) is read on every
  refresh and the listing is held in memory; this suits a store bounded by a
  TTL, not an unbounded one.
