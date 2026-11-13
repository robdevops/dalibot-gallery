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
  that restriction, not in front of it. Pages and the JSON listing are
  `no-store` in every mode; do not override that, because an admin's page
  and listing differ from a visitor's.
- **The proxy grants no access.** The gallery decides who may view and who
  is an admin from the session cookie or the Basic password alone. Do not
  have the proxy add an `Authorization` header for visitors; for a gallery
  without a password use `GALLERY_PUBLIC=1` (see the
  [README](../README.md#a-public-gallery-behind-a-proxy)). If the proxy has
  its own Basic auth, that header reaches the gallery too, which reads the
  password as a token guess: either stop the proxy from forwarding it
  (nginx: `proxy_set_header Authorization "";`) or expect those requests to
  be counted by the limiter.
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
