# Reverse proxy rules

The gallery speaks plain HTTP and expects a proxy in front that terminates
TLS. Example configurations for nginx, Caddy and Traefik are in `deploy/`,
and the [README](../README.md#reverse-proxy) lists them.

Whatever proxy you use:

- **Do not remove, replace or duplicate the gallery's security headers.** The
  examples add only `Strict-Transport-Security`.
- **Do not cache at the proxy when viewing needs the token.** Images are
  then sent with `Cache-Control: private, max-age=31536000, immutable`: the
  browser keeps them for a year, and a shared cache that follows the
  standard does not store them. (The plan specified `public`; `private` was
  chosen because the images are behind the login. Browser caching is the
  same.) In a public gallery (`GALLERY_PUBLIC=1`) they are sent as `public`
  and a shared cache may store them: anyone may see them anyway. If your
  proxy restricts who can reach a public gallery, a cache must sit behind
  that restriction, not in front of it. A shared cache also goes on
  serving an image it already holds after an admin has [hidden](admin.md)
  it, until you purge it. Pages and the JSON listing are
  `no-store` in every mode; do not override that, because an admin's page
  and listing differ from a visitor's.
- **The proxy grants no access.** The gallery decides who may view and who
  is an admin from the session cookie or the Basic password alone. Do not
  have the proxy add an `Authorization` header for visitors; for a gallery
  without a password use `GALLERY_PUBLIC=1` (see the
  [README](../README.md#a-public-gallery-behind-a-proxy)). If the proxy has
  its own Basic auth, it forwards the visitor's `Authorization` header to the
  gallery, which, when an admin token is set, reads the password as a guess
  at the tokens. The same password on every request counts only once a
  minute, so visitors are not blocked and the admin can still sign in at
  `/login`, but each visitor uses up one of the five attempts a minute and a
  `basic auth failed` line is logged. Better: stop the proxy from forwarding
  the header (nginx: `proxy_set_header Authorization "";`, Caddy:
  `header_up -Authorization` inside `reverse_proxy`, Traefik: a `headers`
  middleware with `customRequestHeaders.Authorization=` set empty). The
  gallery's own Basic sign-in for scripts then works only past the proxy.
- **Set `X-Forwarded-For`, and tell the gallery which proxies to believe.**
  The limiter on token guesses counts per client address. The gallery uses
  the address of the TCP peer, unless that peer is listed in
  `GALLERY_TRUSTED_PROXIES`; then it reads `X-Forwarded-For` from the right
  and takes the first address that is not itself a trusted proxy. That is
  the only thing the setting does: a trusted proxy is believed about the
  client's address, and is given no access of its own. nginx's
  `$proxy_add_x_forwarded_for`, Caddy and Traefik all append the client
  address by default. Set the variable to the address your proxy connects
  from, as the gallery sees it:
  - Proxy and gallery on the same host, no container: nothing to do. The
    default trusts loopback.
  - `deploy/compose.yaml` with a proxy on the host: already set. The
    container sees the proxy's connections as coming from the gateway of
    the Compose network, which the file pins to `172.30.80.1`.
  - `docker run` with a proxy on the host: the gateway of the Docker network
    in use (`docker network inspect bridge`, usually `172.17.0.1`).
  - Traefik in Docker: Traefik's address on the shared network; see the
    comments in `deploy/traefik-labels.yaml`.

  List only the proxies themselves. Anything that can connect to the gallery
  from a trusted address can pick its own client address and so get around
  the per-client limit (the overall limit still applies) and write false
  addresses to the log. If the proxy is not listed, nothing breaks, but all
  visitors count as one client and share 5 wrong tokens per minute.
- Nothing else is needed: no WebSockets, no path rewriting, and the only
  request bodies are the login form and the empty sign-out.
