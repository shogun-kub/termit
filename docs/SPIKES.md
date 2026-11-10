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
`termit-uv doctor` while enabled.

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
  local directory cut the whole `termit-uv init` from 2 m 28 s to 0.7 s with the digest still verified.
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
text/html` without one) and caching headers for `/assets/`. Both landed in M1.5.

## S10 (M1.5) — What the first real node taught us

`termit-uv init` on `termit-uv1` runs the whole pipeline: the core, the vhost, the units, both services up,
and a `vless://` client reaching the internet through nginx. Four things only a live host showed:

1. **`xray -test` picks its parser by file extension.** Staging the generated config as
   `/etc/termit/uv/xray.json.new` made the core answer `Failed to get format of …xray.json.new`, and
   `-format=json` does not override it. The staged file is now `xray.json.new.json`; the live file keeps
   its name and the rename stays atomic.
2. **The core could not read its own config.** `xray.json` is `0640 root:termit`, but
   `/etc/termit/uv` was `0750 root:root` and `/etc/termit` was `0750 root:root`, so the unprivileged
   core could not traverse into it: `open /etc/termit/uv/xray.json: permission denied` and a restart
   loop (`start request repeated too quickly`). The config directory now belongs to group `termit`
   (`0750`), its parent is `0755`, and the `dirs` step *verifies* the group — the first fix was a
   silent no-op because the step reported itself satisfied and never ran.
3. **nginx 1.25+ needs an explicit `http2 on;`.** Without it the listener offers only `http/1.1` in
   ALPN, Xray's XHTTP client sends the h2 preface anyway, and nginx answers `400` on
   `PRI * HTTP/2.0` — the tunnel never starts. With the directive the client speaks h2 to nginx, nginx
   speaks HTTP/1.1 to Xray, and `https://example.com` through the tunnel answers `200`.
4. **Disabling a vhost by renaming it inside `sites-enabled` does not disable it.** Debian's include is
   `sites-enabled/*`, a glob, so `default.termit-disabled` is still loaded — and being
   `default_server` it kept answering the bare address with Debian's welcome page. The file is parked
   in `sites-available/` instead, where nothing includes it, and `termit-uv uninstall` moves it back.

Also observed: after `systemctl reload nginx` the old worker keeps serving in-flight requests, so a
`curl` issued in the same second can still see the previous vhost. That is nginx's graceful reload, not
a bug in the step.

The Xray API path was exercised by hand against a throwaway config: `xray api adu` with a full inbound
object (it validates the inbound, so `listen` and `port` have to be present) added a user, and
`xray api rmu -tag=proxy <email>` removed it. M1.5 restarts the core instead, because a restart costs
one connection and only happens when the generated config changes; the API path belongs with the daily
peer sync in M3, where the client set actually changes.

## S11 (M1.4) — Certificates on a host whose :80 belongs to nginx

Issuing for `termit-uv1.lan` against Pebble took one run once the challenge had somewhere to land. The
flow that shipped: the ACME client writes `/.well-known/acme-challenge/<token>` into the webroot nginx
already serves, nginx answers, the client deletes the token. No second listener on `:80`, so the
promise that Termit never takes a public port from another service stays true.

What the lab showed:

- Pebble issues a 90-day certificate; `fullchain.pem` is `0644`, `privkey.pem` and `account.key` are
  `0600`.
- The vhost flips to TLS **in the same `termit-uv init`**: the certificate step notices that the web step's
  output no longer matches and runs it again, rather than leaving the node serving plain HTTP until
  the next run.
- Tokens do not accumulate: after a successful order the webroot holds no files.

Two things worth remembering:

1. **`--force` was silently a no-op.** The engine's `Force` bypasses a step's `Check`, but the step
   called `acme.Ensure`, which decides from the clock — so the certificate was left alone and the
   serial never changed. `Force` has to reach the step: `stepEnv.forced` is now set around `Run`, and
   the certificate step calls `acme.Issue` when it is true. The lesson is general: "run it anyway" is
   not the same instruction as "your effect is missing", and a step whose `Run` is not idempotent
   must be able to tell the two apart.
2. **A renewal failure must not cost the node its certificate.** Drilled by pointing the ACME
   directory at a dead port: the run fails, `termit-uv status` says `last run: failed` /
   `certificate failed`, and the old certificate keeps serving (`200`). Nothing is written until the
   CA returns the chain, and `writeCertificate` writes the key before the certificate so a reader never
   sees one without the other.
## S12 (M1.6) — A whole lifecycle on one box

Fresh host → install → reboot → uninstall → install again, all on `termit-uv1`. The node survived the
reboot on its own and the second install produced a different identity and certificate, which is the
point of keeping generated state out of the config.

Three defects only a full cycle exposes:

1. **Uninstall left `/var/www/termit` behind.** It walked the manifest backwards in one pass, so a
   directory could be attempted before the file inside it had been removed; the decoy step also never
   recorded `assets/` at all, only the files in it. Both are fixed: the manifest walk is three passes
   (files, user, directories deepest-first) and the decoy records its subdirectories.
2. **Uninstall tried to delete directories that belong to the distribution** — `/etc/systemd/system`
   and `/etc/nginx/sites-available` had been recorded as Termit\'s. The steps now record a parent only
   when it is ours.
3. **The parking fix from S10 was never in the code.** The audit after uninstall showed
   `enabled: default.termit-disabled` — the file was still inside `sites-enabled`, still loaded by the
   glob, still `default_server` for the bare address. Fixed for real this time, and the restore after
   uninstall is now exercised by the acceptance run.

Two smaller observations:

- `systemctl reload nginx` is graceful: a request issued in the same second can still be answered by
  the old worker, so a check run immediately after a reload may see the previous vhost. Not a defect —
  poll or wait a second before concluding anything.
- The stock Debian vhost answers `200` again after uninstall, which is the behaviour an operator
  expects: Termit removes what it created and puts back what it moved.
## S13 (M2.1) — SQLite through modernc, and three things it refuses

The choice is `modernc.org/sqlite`: pure Go, so `CGO_ENABLED=0` and cross-compilation still work. The
module graph grows (a handful of indirect dependencies) but nothing needs a C toolchain on the target.

What building the store taught us:

1. **`check` is a reserved word** in SQLite, and the table in `DESCRIPTION.md` §14 is called `check`.
   It has to be quoted (`"check"`) in every statement; renaming it would put the schema out of step
   with the document.
2. **A comment can break naive statement splitting.** The schema is split on `;`, and the header
   comment contained one — so half a sentence was executed as SQL and the migration failed with
   "near …: syntax error". Comment lines are stripped before splitting now. The same trap waits for
   anyone who adds a `;` inside a string literal.
3. **`VACUUM INTO` is the only correct way to copy a live database.** It writes a consistent snapshot
   without stopping the server, and it refuses to overwrite an existing file, so the backup path is
   removed first.

Two smaller notes: the driver reports constraint violations through its own error type, so
"that domain is taken" is decided by the SQLite error code rather than by matching an error string; and
a database from a newer build is refused outright — an older binary writing a newer schema is how a
registry gets corrupted.

File modes are part of the contract here: the database is `0600` (it holds hashes and node metadata,
not public data) and the master key is `0600` with a writer that refuses to replace an existing one.
## S14 (M2.2) — credentials are digests, not password hashes

The first version of `internal/mn/secret` stored Argon2id hashes with a per-value salt. That is the
right thing for a password and the wrong thing here, for a reason that only shows up when the server
has to *use* the stored value: an agent presents `Authorization: Bearer <agent_key>`, and the server
must find the node by that key. A salted hash cannot be looked up — the salt is different for every
row — so every request would have to verify against every node in the registry.

The choice is now SHA-256 with a `sha256:` prefix:

- **Lookup works**: the digest of a presented credential is the index.
- **Nothing is lost by not salting**: the values are 32 random bytes (`secret.Token`), 160 bits for an
  invite code. There is no dictionary to defend against, and a salt defends against exactly that.
- **The prefix is not decoration**: it makes the stored format self-describing, so a future change
  (say, adding a keyed MAC with the master key) can be detected per row instead of guessed.

Argon2id stays out of the module for now. If a *human-chosen* secret ever lands in this database, it
needs the slow hash and a different lookup path; the prefix is what will make that migration possible.

Two related details from the same milestone:

- Enrollment must probe with a credential the node already accepts, and at enrollment only the self
  credential exists. The order in `DESCRIPTION.md` §7.3 reads as if the probe credential came first;
  it cannot. MN issues `probe_uuid` in the enrollment response and the node starts accepting it at its
  first sync.
- The invite is consumed in the same transaction that inserts the node. A refusal (bad URL, DNS
  mismatch, failed probe) must not burn an invite, and a node must not exist without the invite that
  admitted it.
## S15 (M2.2) — two defects that both looked like a broken node

Enrolling the first real node took four attempts, and every failure had the same shape: the probe
said the node did not work. Neither defect was the node's.

**Xray does not read `SSL_CERT_FILE`, and did not read the host bundle either.** With a lab CA
(Pebble) the worker reported `x509: certificate signed by unknown authority` no matter what: the root
was appended to `/etc/ssl/certs/ca-certificates.crt` (150 certificates on disk), `SSL_CERT_FILE` was
exported into the worker's environment, and `update-ca-certificates --fresh` claimed to install it.
Python, against the same host and the same file, verified the chain fine. The lesson is not "find the
right env var": it is that a prober should not depend on the host's trust store at all. The answer is
the pin the design already asked for — MN reads the leaf on enrollment, checks hostname, chain and
expiry, and passes `pinnedPeerCertSha256` to the worker. That is stronger than CA trust (a valid
certificate from the wrong authority no longer passes), and it makes the prober independent of the
machine it runs on.

**The probe spoke cleartext HTTP into port 443.** `httpStatusThroughTunnel` wrote `GET / HTTP/1.1` to
whatever port the probe URL named, including 443, where the target's web server answered `400 Bad
Request`. The node had carried the request perfectly; the answer was garbage. The tunnel now wraps the
connection in TLS when the port is 443. Worth remembering: "got HTTP 400" from a probe is evidence
about the *probe* until proven otherwise.

A third, smaller finding: `openssl verify -CAfile root.pem fullchain.pem` failing does not mean the
root is wrong. It does not use the second certificate in the file as an intermediate; `-untrusted`
does. One more way to misread a certificate problem as a trust problem.