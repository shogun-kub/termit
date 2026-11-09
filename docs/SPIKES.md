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
