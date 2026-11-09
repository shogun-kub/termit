# Spike log

Recorded decisions from throwaway experiments. Each entry states the question, what was observed
(with the evidence that decides it), and what changed in the design as a result.

Environment for all entries: `termit-uv1` (Debian 13, 1 vCPU, 1 GB, unprivileged LXC), Xray 26.11.9,
self-signed certificate for `termit-uv1.lan`, Python `http.server` as a stand-in decoy, clients and
servers on the same host. Scripts are throwaway and live outside the repository.

---

## S1 (M0.2) — Can one VLESS+XHTTP+TLS inbound on :443 serve the decoy through `fallbacks`?

**No.**

An inbound configured as `network: xhttp`, `security: tls`, `settings.fallbacks: [{dest: 127.0.0.1:18080}]`
starts, presents the certificate, and answers requests itself:

```
transport/internet/splithttp: listening tcp for XHTTP on 0.0.0.0:443
transport/internet/splithttp: failed to validate path, request:/, config:/spike/
https-root-status: 404      # nothing reached the decoy
```

The XHTTP transport owns the HTTP request handling on that port. A request that is not an XHTTP
request is rejected with `404`; it is never relayed to the fallback destination. `fallbacks` on an
XHTTP inbound is therefore not a decoy mechanism.

Also observed: the XHTTP server normalises the configured path with a trailing slash
(`config:/spike/`), so a client requesting exactly `/spike` is rejected while `/spike/...` is
accepted.

## S2 (M0.2) — Can a VLESS/RAW/TLS terminator use `fallbacks` by path to relay XHTTP traffic?

**Partly: the decoy side works, the XHTTP side cannot.**

A `network: raw`, `security: tls` VLESS inbound with two fallbacks — `{path: "/spike/", dest:
127.0.0.1:18443}` and a default `{dest: 127.0.0.1:18080}` — behaves as intended for browsers:

```
decoy-h11: 200 ok=1     # https://domain/  -> decoy through the fallback
```

XHTTP clients, however, never match the path fallback. The client appends a per-request identifier
to the configured path, and the fallback matcher compares the request path **exactly**:

```
proxy/vless/inbound: realPath = /spike/9dfd40ca-d817-491e-a5b0-592035dd46f6
proxy/vless/inbound: failed to dial to 127.0.0.1:18080   # fell through to the default fallback
```

Since the suffix changes on every request (and a second variant `/spike/<id>/0` appears as well), no
finite set of exact-match fallback paths can cover XHTTP. Prefix matching is required, and Xray's
fallback does not do prefix matching.

## S3 (M0.2) — Does the XHTTP inbound itself work when reached directly, without TLS?

**Yes.**

A client speaking XHTTP with `mode: packet-up` directly to `127.0.0.1:18443` (`security: none`)
tunnels traffic correctly:

```
c1: inet=200            # http://example.com fetched through the tunnel
```

It also accepts the per-request path suffix (`/spike/<id>`, `/spike/<id>/0`) against a configured
path of `/spike/`. So the protocol is fine; only the routing layer in front of it was wrong.

`mode: auto` / `stream-one` are HTTP/2-shaped and do not work over a downgraded HTTP/1.1 hop;
`packet-up` does work over HTTP/1.1.

## S4 (M0.2) — Client-side certificate verification in Xray 26

`tlsSettings.allowInsecure` has been **removed**:

```
The feature "allowInsecure" has been removed and migrated to "pinnedPeerCertSha256"(pcs)
and "verifyPeerCertByName"(vcn).
```

- `pinnedPeerCertSha256` is a **hex** string (not base64, not an array).
- `verifyPeerCertByName` still validates against the system roots, so it does not help with a
  self-signed certificate; pinning is the way.

Consequence for the product: neither the generated client URIs nor the MN probe client may emit
`allowInsecure`. The probe pins the node's certificate fingerprint (or trusts the lab CA via
`extra_roots`), and the profile never depends on `insecure=1`.

## S5 (M0.2) — Decision

**The 443 front must be HTTP-aware.** The chosen shape:

```
:443/tcp  nginx   TLS termination, serves the decoy, proxies /<secret>/ to Xray
127.0.0.1:8443    xray  VLESS+XHTTP, plaintext, loopback only
:80/tcp   nginx   ACME HTTP-01 webroot + 301 to HTTPS
```

nginx gives prefix matching (`location /<secret>/`), HTTP/2 where clients want it, and a
battle-tested streaming proxy for the XHTTP hop — the same three-layer shape the reference uses,
except that we generate and own the nginx configuration instead of overwriting the distribution's
default vhost.

Consequences recorded in `DESCRIPTION.md`:

- §5.1: port/service ownership table now includes nginx; Xray moves to loopback and loses TLS.
- §5.2: request paths go through nginx.
- §13.1: memory budget includes nginx and no longer includes an in-agent decoy server.
- Profile `v1`: XHTTP `mode: packet-up` over an HTTP/1.1 hop from nginx, ALPN `h2,http/1.1` at the edge
  (to be re-verified once the real nginx config and the real client exist; `mode: auto` is retried
  then).

Remaining unknown for M1: whether `mode: auto` behaves under nginx's HTTP/1.1 upstream proxy (the
reference used `auto` in that position, but it is unverified here); `packet-up` is the fallback that
is known to work.

---

## S6 (M0.3) — Which ACME client, and does HTTP-01 issuance work?

**Chosen: `golang.org/x/crypto/acme`**, not `go-acme/lego` and not `x/crypto/acme/autocert`.

Reasons: it is first-party and small; we must own the HTTP-01 handler anyway (nginx proxies the
challenge prefix to the agent, or the agent serves it directly); and autocert wants to own the
`tls.Config`, while here the certificate is just PEM files that nginx reads. lego would add a large
dependency tree (DNS providers, jose, ...) for a flow we implement in ~150 lines.

Measured on `termit-uv1`, Pebble running on `termit-mn`:

* Pebble is configured with `httpPort: 80`, so it validates by fetching
  `http://termit-uv1.lan/.well-known/acme-challenge/<token>` from the MN container.
* The agent's handler served the token, Pebble issued through the intermediate, and the test wrote
  `fullchain.pem` (0644, 2 certificates) and `privkey.pem` (0600) in **2.0 s**.
* Leaf: `SAN=DNS:termit-uv1.lan`, issuer `CN=Pebble Intermediate CA …`, `notAfter` +90 days.
* A second `Ensure` reused the certificate without contacting the CA.

Pitfall worth remembering: the older `CreateOrderCert(ctx, finalizeURL, …)` passes an empty order URL
and fails with `Post "": unsupported protocol scheme ""` as soon as the CA answers with `processing`.
`CreateCertFromOrder(ctx, order, …)` passes both URLs and is the API to use.

Pebble's own listener certificate is self-signed, so the client needs `SkipTLSVerify` for the ACME
endpoint in the lab. That flag is `acme.skip_tls_verify` in the product config and must be reported by
`uv doctor` while enabled.

## S7 (M0.4) — Can one worker probe N nodes, and are failures distinguishable?

**Yes.** One `xray-core` process with one SOCKS inbound per target, routed to that target's
VLESS+XHTTP outbound, is enough; the config is validated with `xray run -test` before the process
starts.

Measured on `termit-mn` against `termit-uv1` (nginx → XHTTP → freedom → `http://example.com/`):

| Case | Result |
|---|---|
| healthy node | `ok=true`, 72–131 ms end-to-end, worker start ≈60 ms shared by all targets |
| two targets in one worker | 135 ms total: first `ok`, second (closed port) failed in 1 ms |
| node's xray stopped | `class=tunnel_closed`, `err=EOF`, 6 ms, `worker_unknown=false` |
| wrong certificate pin | `class=tunnel_closed`, `err=EOF`, 5 ms, `worker_unknown=false` |
| missing/broken xray binary | `Worker` construction fails; `Probe` returns an error, never a node failure |

Two consequences for M2:

1. **A failed tunnel and a failed pin look identical at the SOCKS layer.** TLS validity is therefore a
   separate check (`decoy + certificate`), not something the tunnel probe can report. The result model
   keeps `class` for the tunnel and the checker reports its own verdict.
2. The probe itself is a plain SOCKS5 CONNECT followed by a minimal `GET`; the client is ~120 lines of
   standard library, so the prober has no dependency beyond the xray binary.

Config pitfall: xray expects `routing` to be an object (`{"rules": [...]}`); a bare array is rejected
at config load with exit status 23.

## S8 (M1.2) — Is the pinned Xray archive verifiable, and does a mirror or a local copy work?

**Yes, and the digest is the only thing that needs trusting.** The lab's GitHub downloads returned
`504` intermittently during M0, so the installer had to be able to take the archive from somewhere
else without weakening the check.

- The release publishes one `<asset>.dgst` per archive. For `v26.11.9` the SHA2-256 values are
  `b651f7e1…5f82` (`Xray-linux-64.zip`) and `64ed6858…30be` (`Xray-linux-arm64-v8a.zip`); both are in
  the table in `internal/uv/install/xray.go`.
- The downloaded archive hashed to exactly the published `b651f7e1…5f82`, so a mirror that serves the
  same bytes is safe: the digest, not the source, is what the installer trusts.
- The archive is flat — `xray` (0755), `geoip.dat`, `geosite.dat`, `README.md`, `LICENSE` — so the
  installer extracts only `xray` and never unpacks into a directory tree.
- `download_base` accepts an HTTP(S) URL, a `file://` URL, or an absolute directory. On the node, a
  local directory cut the whole `uv init` from 2 m 28 s to 0.7 s with the digest still verified.
- A mismatching archive is refused *before* extraction, and a cached archive whose digest no longer
  matches is deleted and downloaded again rather than trusted.

Decision: keep the digest table in code, allow `xray.sha256.<arch>` for a re-packed or self-built
core, and treat the archive as the only artifact whose provenance matters.

## S9 (M1.3) — Can nginx serve the decoy and answer its sign-in form itself?

**Yes, and it removes a daemon.** The page is a generated file tree; the form posts to a path that a
generated nginx `location` answers with `return 401` and its own headers. Checked on `termit-uv1`
against a throwaway `nginx -c` configuration on `127.0.0.1:8080`, so nothing on `:80` or `:443` was
touched:

| Request | Answer |
|---|---|
| `GET /` | `200 text/html`, 5.6 KB, zero URLs of any kind in the document |
| `POST` the path from the form's `action` | `401`, `application/json`, `Set-Cookie: …; HttpOnly`, `X-Request-Id` |
| `GET` the same path | `405` |
| `GET /does-not-exist` | `404` with the generated `404.html` |
| `/robots.txt`, `/favicon.png`, `/assets/boot.js` | `200` with `text/plain`, `image/png` 32×32, `application/javascript` |

Two things only a live server showed:

1. **The first attempt answered `403`.** The tree was under `/var/lib/termit/uv/site`, which is `0750`,
   and nginx's worker is `www-data` — it cannot traverse that directory. The decoy is public content,
   so it moved to `/var/www/termit/site` (`0755`, files `0644`), and the ACME webroot to
   `/var/www/termit/acme` for the same reason: M1.4 needs nginx to read challenge files too.
2. `error_page 404 /404.html` works from the fragment, so a prober that guesses paths sees the site's
   own 404 instead of nginx's default page.

Decision: no agent-side HTTP server, which the plan's earlier idea had. The artificial delay it would
have added is not worth a second daemon on a 1 GiB box, and `return 401` is indistinguishable from an
application to a scanner; `X-Request-Id $request_id` supplies a unique id per answer without a
process. The product archetype ships brand-free: a real vendor's mark in an AGPL repository is
trademark use, and every deployment sharing one logo is itself a fingerprint.

Recorded for M1.5: the server block still needs `charset utf-8;` (nginx answered `Content-Type:
text/html` without one) and caching headers for `/assets/`.
