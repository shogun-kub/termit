# Termit — project description

Status: the design of record for the code in this repository. `0.6.0` is the current release and
`0.7.0` sits on `dev`, unreleased. Audience: the maintainer and any agent or engineer who implements a
slice of it.

Related documents:

- [`QUESTIONS.md`](QUESTIONS.md) — open decisions and the answers taken so far.
- [`IMPLEMENTATION_PLAN.md`](IMPLEMENTATION_PLAN.md) — milestones, tasks, acceptance criteria.
- [`../AGENTS.md`](../AGENTS.md) — rules for coding agents working in this repository.

---

## 1. One paragraph

Termit is a cooperative proxy mesh for people who own a small VPS. Each participant installs **UV**
on their own Debian box; UV turns it into a plain-looking VLESS over XHTTP+TLS endpoint with
a convincing decoy website, applies traffic rules, and can route its proxied traffic through
Cloudflare WARP. All participants are coordinated by **MN**, a small self-hosted management server:
it verifies that every node is really reachable and really presents a valid certificate, keeps the
membership list, hands out per-pair credentials, and serves each owner a single subscription that
contains every healthy node in the mesh. Nobody has to trust a central proxy provider, and no single
operator sees the traffic — but the group gains the resilience of many independent exits.

## 2. Goals

1. **One command turns a fresh VPS into a working, disguised proxy.** Non-interactive where possible,
   idempotent, reversible (`termit-uv uninstall`), and safe to re-run.
2. **Direct traffic is never harmed.** SSH, the decoy site, the HTTP→HTTPS redirect, the ACME
   challenge and anything else the owner runs on the box must keep working exactly as before. WARP
   and the blocking rules apply to *proxied* traffic only.
3. **A verified mesh, not a list of URLs.** MN proves that a node's VLESS endpoint actually works
   through the tunnel and that the decoy site really serves a valid certificate, and removes nodes
   that stop working.
4. **Bounded blast radius for credentials.** A leaked client config must not grant access to the
   whole mesh (see §6).
5. **Predictable failure.** Every automatic action (block a node, remove peers, restart a service)
   has an explicit rule, is auditable, and never depends on a check the actor could not perform.
6. **Debian stable first.** Debian 13 (trixie) amd64 and arm64 are the target. Other distributions
   are a later, explicit extension, not an accident of shell compatibility.

## 3. Non-goals (v1)

- TUIC, Shadowsocks, MTProto, Trojan. VLESS+XHTTP+TLS is the client-facing profile; Hysteria2 joined it as a
  second transport in `0.5.0` (§3.6.2 of the plan), which is a kind the mesh reports, not a second profile.
- REALITY. Dropped on 2026-11-22: in its honest form (`dest` = the node's own vhost) it is externally the
  TLS the node already serves, and the variant that is cheaper to deploy is the one active probing catches
  (§3.6.1 of the plan).
- Traffic accounting, quotas, expiry, billing, Telegram bots.
- A web admin UI on MN. v1 is an HTTP API plus the `termit-mn` CLI.
- Multi-tenant MN (several independent groups on one instance), federation between MNs, or
  peer-to-peer gossip between UVs.
- Hosting arbitrary user websites. The box serves one generated decoy site; the fallback target is
  configurable so that this can be extended later without redesign.

## 4. Vocabulary

| Term | Meaning |
|---|---|
| **UV** | *User VPS*. One participant's node, and the agent binary that manages it. |
| **MN** | *Management node*. The coordination server, and its binary. |
| **Self credential** | The UUID a UV generates for its own server; it is what the registered "basic VLESS URL" contains. |
| **Pair credential** | A UUID valid on exactly two servers, used by those two owners to reach each other. Derived by MN, never stored as plaintext on UV. |
| **Probe credential** | A UUID issued by MN at enrollment and accepted by the UV, so MN can test the node without borrowing the owner's credential. |
| **Basic VLESS URL** | The `vless://` URI the UV registers: its own domain, its self credential, and the profile parameters. MN parses it to learn how to build peers' URLs. |
| **Peer** | Another UV, as seen from one UV's perspective. |
| **Decoy site** | The generated fake website served to anyone who opens the domain in a browser. |
| **Agent key** | Bearer secret that authenticates a UV to MN. Distinct from every VLESS credential. |
| **Subscription token** | Read-only secret embedded in the subscription URL. Distinct from the agent key. |
| **Profile** | The protocol parameter set (`v1` = VLESS+XHTTP+TLS). Versioned so MN can refuse incompatible nodes. |

## 5. Architecture

```
            ┌───────────────────────── MN (public host, TLS) ─────────────────────────┐
            │  HTTP API  /sub/{token}  /metrics(loopback)  admin unix socket          │
            │  ┌───────────┐ ┌──────────────┐ ┌────────────┐ ┌───────────────────┐   │
            │  │ registry  │ │ health prober│ │ credentials│ │ subscription      │   │
            │  │ (SQLite)  │ │ (xray-core)  │ │ (HMAC)     │ │ renderer          │   │
            │  └───────────┘ └──────────────┘ └────────────┘ └───────────────────┘   │
            └───────────────▲───────────────────────────────┬────────────────────────┘
                            │ enroll / sync / unblock / migrate (agent key, TLS)     │ subscription
                            │                                                       ▼ (base64 URIs
   ┌────────────────────────┴───────────────┐                          or JSON profile)
   │ UV (Debian 13, own domain)             │                          phone / desktop clients
   │  nginx :80/:443 — TLS, decoy, ACME     │
   │        │  location /<secret>/          │
   │        └──► xray 127.0.0.1:8443 ── VLESS+XHTTP ──► routing ──► direct
   │                                         │                      └► block
   │                                         └──────────────────────└► WARP (in-core, or a sidecar)
   └────────────────────────────────────────┘
```

Two binaries, one repository:

- `termit-uv` — installer, agent and CLI on each node.
- `termit-mn` — management server and admin CLI.

### 5.1 Port and service ownership on a UV

| Port / socket | Owner | Purpose |
|---|---|---|
| `:80/tcp` | `nginx` | ACME HTTP-01 webroot and `301` to HTTPS |
| `:443/tcp` | `nginx` | TLS termination, the decoy site, and the proxy hop for the secret path |
| `127.0.0.1:8443/tcp` | `xray` | VLESS+XHTTP, plaintext, loopback only |
| `127.0.0.1:10085/tcp` | `xray` | management API (inbounds, users), loopback only |
| `127.0.0.1:40000/tcp` | `warp-cli` (only on `warp.transport: proxy`) | WARP SOCKS5 proxy |
| `127.0.0.1:40001/tcp` | `termit-usque-relay` (only on `warp.transport: usque`) | the hop the core dials, run by `termit-uv warp-relay`; it forwards each association from the control connection's own port |
| `127.0.0.1:40002/tcp` | `termit-usque` (only on `warp.transport: usque`) | the MASQUE client's own SOCKS5 listener, behind the relay |
| `:443/udp` | `hysteria` (only when the QUIC transport is enabled) | that transport; a browser finds it through the `Alt-Svc` the vhost advertises |

The front is nginx because the routing decision is a **prefix** match. Xray's `fallbacks` compare the
request path exactly, while an XHTTP client appends a per-request identifier to its configured path
(`/secret/<id>`, `/secret/<id>/0`), so no fallback rule can match it; an XHTTP inbound also answers
unknown paths with `404` instead of relaying them. Both behaviours were measured (see
[`SPIKES.md`](SPIKES.md) S1 and S2). nginx's `location /secret/` does the job in one line, terminates
TLS for browsers, and provides a battle-tested streaming proxy for the XHTTP hop.

Xray therefore holds no certificate and speaks no TLS. The agent generates both configurations,
serves nothing itself, and owns certificate issuance and renewal. The decoy is a static tree that
nginx serves; an owner who wants a real site can point the fallback location at it later.

The one exception to "one proxy process" is a transport the core cannot speak. It stays narrow: a pinned
binary with a checksum, its own unit, user and state directory, a management listener on loopback only,
a handshake whose shape is re-tested with every upgrade, and no policy of its own — its traffic enters
the core through a loopback `socks` inbound, so WARP, the blocklist and the direct rules stay the core's
decisions. It is the shape `0.5.0` shipped for the QUIC transport and `0.6.0` for the MASQUE sidecar
(plan §3.6.2).

> Design note: this is the same three-layer shape the references use. The difference is that we
> generate and own one nginx configuration file instead of overwriting the distribution's default
> vhost.

Two details of that file matter operationally:

- **`http2 on;` is mandatory on the TLS listener** (nginx 1.25+). Without it the listener offers only
  `http/1.1` in ALPN, Xray's XHTTP client sends the HTTP/2 preface anyway, and nginx answers `400` —
  the tunnel never starts. With it, a client speaks h2 to nginx and nginx speaks HTTP/1.1 to Xray.
- **The distribution's default vhost is parked** at `sites-available/default.termit-disabled` and put
  back by `termit-uv uninstall`. It cannot simply be renamed inside `sites-enabled`: Debian includes that
  directory with a glob, so the renamed file is still loaded, and as `default_server` it would keep
  answering the bare address with "Welcome to nginx" — a louder fingerprint than the decoy is quiet.

### 5.2 Request paths on the UV

1. `https://domain/<secret>/…` → nginx `location /<secret>/` → `http://127.0.0.1:8443` → Xray XHTTP
   → VLESS → routing → outbound.
2. `https://domain/anything-else` → nginx → generated decoy page (`200`, HTML, plausible headers, no
   external assets).
3. `https://domain/<auth-path>` (`POST`) → nginx returns a plausible `401` JSON with a random error
   code and a `Set-Cookie`, with the path chosen per install, so the portal answers like a real one
   instead of being inert.
4. `http://domain/*` → nginx `:80` → `301` to `https://`, except `/.well-known/acme-challenge/*`.
5. `ssh` and every other inbound service → untouched by Termit.

### 5.3 Decoy site

The decoy is the layer that makes an active prober see an ordinary corporate service instead of a
proxy. It is generated locally, deterministically, from `(domain, seed, template)`; nothing is
fetched at runtime and nothing on the page points at Termit.

Two archetypes, one generator interface:

- **Branded portal** (primary; follows the family of the reference generator). An invented product
  with a small badge, a title and subtitle written in infrastructure register, deliberately
  non-standard field labels ("Identity Principal", "Credential Key"), a checkbox, a full-width CTA,
  and a footer with a fictional company and legal links. Dark and light presets, one accent color,
  centered card, restrained motion. Randomised per install: product name, vocabulary, preset,
  footer company, link set.
- **Product sign-in screen** (optional, one high-fidelity template). A faithful re-creation of the
  *idiom* of a well-known product's sign-in screen — brand row, one card, an SSO divider, a legal
  footer — with an invented product name and local secondary links. Shipping a real vendor's mark in
  an AGPL repository would be trademark use and would give every deployment the same logo, so the
  template is an original screen with the same impression, not the same brand.

Hard rules, because each of these is a fingerprint a scanner can match:

- No external requests. No CDN, no Google Fonts, no analytics, no remote images. System font stack or
  a self-hosted subset only.
- No generator-shaped identifiers (`f_<hex>`, `b_<hex>`, `csrf_<hex>`), no single shared submit path
  across deployments, no identical layout everywhere. Ids and field names look hand-written; the
  submit path and the auth path vary by install.
- The submit endpoint answers `POST` with a plausible `401` JSON (error code, message, request id)
  and sets a cookie — an inert page is weaker than one that answers. nginx answers it from a
  generated fragment (`return 401` with the body, cookie and a per-request id), so no application
  server runs behind the page; the page, the endpoint and the fragment come from one data set.
- A `robots.txt` that disallows a few internal-looking paths, and a favicon generated from the same
  seed.
- Templates are original re-creations. Brand assets and CSS of a real product are never copied.

The tree is written to `/var/www/termit/site` with mode `0755`, because nginx runs as `www-data` and
cannot traverse the `0750` state directory; state, certificates and the generated nginx fragment stay
under `/var/lib/termit/uv` and `/etc/termit/uv`.

Local visual references (screenshots and serialized DOM of pages used for inspiration) live in
`captures/decoy-samples/`, which is gitignored: provenance stays out of the published repository.

## 6. Identity and credentials

The mesh is a complete graph. For `N` active nodes there are `N·(N−1)/2` pairs, and each pair has its
own credential.

### 6.1 What exists

| Secret | Generated by | Stored on MN | Stored on UV | Purpose |
|---|---|---|---|---|
| `master_key` (32 B) | `termit-mn init` / env | DB or `TERMIT_MN_MASTER` env | never | derives all pair credentials |
| `self_uuid` | UV at init | as part of the registered URL | `identity.json` | owner reaches own server |
| `probe_uuid` | MN at enrollment | DB | `identity.json` | MN health probe |
| `pair_uuid(A,B)` | derived | derivable | in `state.json` + Xray config | A↔B access |
| `agent_key` | MN at enrollment | SHA-256 digest | `identity.json` (0600) | authenticates agent API calls |
| `sub_token` | MN at enrollment | hash | not needed by UV | read-only subscription URL |
| local user UUIDs | UV owner | no | `state.json` (`local_users`) | family devices, managed with `termit-uv device` |

### 6.2 Pair credential derivation

```
pair_uuid(A,B,epoch) = UUIDv4From( HMAC-SHA256( master_key,
      "termit/pair/v1|" + epoch + "|" + min(uv_id_A,uv_id_B) + "|" + max(uv_id_A,uv_id_B) )[0:16] )
```

`UUIDv4From` sets the RFC 4122 version/variant bits. Properties:

- Deterministic: MN needs no table, and any node can be re-derived after a restore.
- Symmetric: the same UUID works in both directions, which is what the mesh needs.
- Pair-scoped: a leaked config exposes the two servers of that pair, not the mesh.
- Rotatable: bumping `credential_epoch` invalidates every pair credential at once (a break-glass
  action; every client config must be re-issued).

Test vectors for this function are part of the contract (`internal/cred/vectors_test.go`); MN and UV
must never disagree about it.

### 6.3 What each UV accepts as clients

```
clients(A) = { self_uuid(A), probe_uuid(A) }
           ∪ { pair_uuid(A,P) | P ∈ active peers }
           ∪ { local users }
```

`unavailable` peers stay in `clients(A)`; `blocked` and removed peers are dropped. Pair credentials of
disabled peers are not revoked on the peer's own server (it is off the network anyway) but they are
removed from every *other* node's config, which is what removes the access path.

### 6.4 What each subscription contains

For UV `A`:

```
self              vless://self_uuid(A)@A.domain:443?…params(A)…#A-label
                  hy2://user(self_uuid(A)):pass(self_uuid(A))@A.domain:443?sni=A.domain#A-label (QUIC)
peer P (active)   vless://pair_uuid(A,P)@P.domain:443?…params(P)…#P-label
                  hy2://user(pair_uuid(A,P)):pass(pair_uuid(A,P))@P.domain:443?sni=P.domain#P-label (QUIC)
```

The `hy2://` line exists only for a node that reported the QUIC transport (§8): the user name and the
password are a one-way derivation of the same credential the `vless://` line carries, so the server stores
nothing extra, and a node that stops serving the transport simply loses its second line at the next sync.
A subscription therefore has one to two links per node, never more, and the plain and JSON forms carry the
same set.

Peers are ordered by label then id so that the rendered subscription is stable (ETag-friendly).
`unavailable` peers are included but marked in the label suffix (e.g. `·degraded`) — clients keep
working, the owner sees the state. `blocked` peers are excluded.

`…params(…)` is the same fixed set for every entry, and lives in one place (`internal/proto/profile.go`)
so the URI builder and anything reading a URI back cannot drift: `type=xhttp`, `mode=packet-up`,
`security=tls`, `sni=<domain>`, the node's secret path, `fp=firefox`, and an `extra` object carrying
`xPaddingBytes`. There is no `alpn`: the transport is HTTP/1.1 on the wire because nginx terminates TLS
and forwards HTTP/1.1 to Xray, so an `h2` offer can only reach for a handshake the hop cannot carry.
`fp` is the uTLS ClientHello the client should send; the node never inspects it, but filters on the path
do, and `firefox` is the measured choice — a `chrome` hello (with an X25519MLKEM768 key share, ~1.8 KB)
is frozen by some consumer paths when several connections open to one host at once, while `firefox`
passed in the same measurements. `extra.xPaddingBytes` makes every request a different size, so a filter
cannot match a constant one; it costs a few hundred bytes per POST and the node ignores it.
`extra.xmux` reuses one connection for many proxied connections — 20 proxied connections took 1 TCP
connection with it and 5–8 without, through the nginx hop, and the connection count is what a lossy path
turns into failed handshakes; `hMaxReusableSecs` bounds how long one connection lives. Neither is in the
sing-box profile: sing-box validates strictly and would reject a field it does not know.

XHTTP is a transport, not a protocol every client implements: stock sing-box has no XHTTP, and its
`/json` subscription form therefore only serves builds that patched it in. See
[`OPERATIONS.md`](OPERATIONS.md) §8.1 for the client matrix and for the `unknown version: 72` failure
that a wrong client produces.

### 6.5 Trust boundaries

- MN knows every credential except each UV's `self_uuid` private use; it is the single most valuable
  target. It must never log tokens or credentials, and its admin surface is a unix socket.
- A UV owner can see pair credentials involving their own node only. They cannot mint credentials for
  pairs they are not part of.
- The subscription URL is a bearer capability for *one* owner's list. It must not be the agent key,
  and it must be revocable and rotatable.
- A hostile UV can still refuse to serve peers while answering the probe. This is a cooperative
  network, not a market; the mitigation is social (removal by the admin), not technical. The probe
  credential at least prevents "accept only my own UUID".

## 7. Enrollment, states and lifecycle

### 7.1 States

| State | In others' configs | Subscription | Meaning |
|---|---|---|---|
| `pending` | no | none (the URL answers `404`) | enrolled, waiting for invite validation / admin approval |
| `active` | yes | full | checks pass |
| `unavailable` | yes | full, marked degraded | checks failed, within grace |
| `blocked` | no | URL still valid; the node is absent from it and peers no longer accept it | checks kept failing |
| `removed` | no | none | admin removed; credentials destroyed |

### 7.2 Transitions

```
                 enroll                approve
   (none) ──────────────► pending ──────────────► active
                              │                     │  ▲
                              │ reject              │  │ check OK
                              ▼                     │  │
                          (removed)      check fails│  │
                                              ┌─────▼──┴─────┐
                                              │  unavailable │
                                              └──────┬───────┘
                                        next check    │
                                        still fails   ▼
                                                  ┌────────┐
                                    unblock OK    │ blocked│
                              active ◄────────────┴────────┘
```

Rules (all thresholds configurable on MN, defaults in parentheses):

- **Check cadence**: one cycle per UV per ~24 h, random minute, never earlier than 2 h after
  enrollment. MN may override `next_sync_after` in the sync response.
- **Convergence between cycles**: the agent wakes on the interval the server states in its sync policy
  (`policy.mesh_poll_seconds` on the wire, from `policy.mesh_poll` in the server's configuration;
  default fifteen minutes, `0` turns it off) and reads `{prefix}/healthz`, which carries a digest of
  the mesh's shape. When the digest differs from the one its last sync applied, the node reconciles
  immediately — so a join, a block or a removal reaches the rest of the mesh in one interval rather
  than at each node's next cycle. The poll carries no credential and no peer data, and with the
  interval at `0` the node makes no request at all between cycles.
- **Retries inside a cycle**: 3 attempts, 10 min apart (adjustable), before a cycle counts as failed.
  Attempts never count towards a block: one cycle is one check, however many attempts it took.
- **Active → unavailable**: first failed cycle. Stays in peers' configs.
- **Unavailable → active**: any successful cycle.
- **Unavailable → blocked**: when the consecutive failure streak reaches
  `policy.block_after_failed_cycles` (default 2, i.e. one grace check; ≈24–30 h at the daily cadence).
  A successful cycle resets the streak; a check that could not run neither advances nor resets it.
  Peers drop it at their next sync or at their next mesh poll (one poll interval, fifteen minutes by
  default).
- **Blocked → active**: only an explicit, successful unblock request (Scenario B) or an admin action.
  No automatic un-blocking, to avoid flapping.
- **MN never blocks on a check it could not perform.** A prober outage, MN-side DNS failure, or a
  missing probe binary yields `unknown`, never `failed`. This is an invariant, see §11.
- **The probe reads the certificate before it opens the tunnel.** It verifies the chain, the
  hostname and the validity window in its own process, then pins the tunnel to that leaf:
  - an **expired** certificate is a verdict (`cert_expired`): clients are rejected too;
  - a leaf the server has not pinned is **`unknown`** (`cert_changed`), because that is exactly what
    a renewal looks like before the node has reported it — it must never block a healthy node;
  - every sync carries the node's current leaf (`X-Territ-Cert-SHA256`), so the pin follows a
    renewal instead of freezing the certificate the node had at enrollment.

### 7.3 Scenario A — first registration and the steady loop

1. Owner runs the UV installer with `--domain`, `--mn`, and an invite code.
2. UV performs pre-flight (`termit-uv doctor`), issues the certificate, starts Xray and the decoy site,
   generates `self_uuid`, and self-tests locally.
3. `POST {prefix}/enroll` with `{invite_code, domain, basic_vless_url, agent_version, profile, capabilities}`.
4. MN validates the invite, parses the URL, resolves the domain (must equal the request source IP,
   dual-confirmed by two resolvers), then probes the new node:
   - the decoy site over TLS (chain + hostname + expiry ≥ 14 d, then `GET /` → `200` + HTML), and
   - VLESS end-to-end through the tunnel using the probe credential.
5. On success MN creates the UV record (`active` immediately for a valid invite; `pending` if manual
   approval is required), generates `agent_key` + `sub_token` + `probe_uuid`, and returns them once.
6. UV stores them `0600`, adds `probe_uuid` as a client, and performs its first sync.
7. Daily cycle, per UV, at its own random time: local self-check → `POST {prefix}/agent/sync`
   (agent key) with the check report, the transports it serves and the leaf certificate it serves
   (`X-Territ-Cert-SHA256`) → MN runs/refreshes its own checks if due → response carries the
   complete desired peer set, the subscription URL, the next due time, and the build the server runs
   (`server_version`), which the node keeps beside its last sync.
8. UV reconciles the running core with the desired set: the generated config is compared with the
   file **and** the core is asked which clients it actually serves, so an API update or a hand edit
   cannot hide behind a correct file. When the client set is the only difference, additions and
   removals go through the Xray API; anything else is one `xray -test`-validated restart. A canary
   connection through the local tunnel proves the result after either path, and any failure restores
   the previous file and restarts. `termit-uv sync --check` reports the same drift (live set included) and
   exits `4` without changing anything; `termit-uv status --json` exposes the schedule, the last self-check
   and the peer list.
9. MN's state machine reacts to the check results (§7.2) and notifies affected peers through their
   next sync.

### 7.4 Scenario B — unblock

1. On a rebuilt box (empty state, same owner): `termit-uv init --relocate --agent-key-file <0600 file>`
   installs everything, mints a new identity (self UUID, XHTTP path, certificate) and stores the saved
   agent key instead of enrolling. Minting an identity is refused when the state already has one: a
   working node is not something an init may rewrite. The server still has the old record and the old
   endpoint, and the node is still `blocked` (a rebuilt box that merely comes back healthy is not
   admitted by a check).
2. The owner runs `termit-uv unblock [--domain D] [--vless-url U]` with the agent key. Without `--vless-url`
   the box builds its own URI from the local identity — exactly what a rebuilt box must present — so a
   credential never appears on a command line or in the remote process list.
3. `POST {prefix}/agent/unblock` with `{domain?, basic_vless_url?}`.
4. MN applies the same DNS + decoy + VLESS checks as enrollment, against the supplied or stored
   domain/URL: DNS must point at the requesting box, the decoy must answer, and the tunnel must carry
   traffic for the claimed UUID at the claimed path.
5. On success: state → `active`, `failed_cycles = 0`, and the stored profile (self UUID, path) and
   certificate pin are replaced together. The node id and every pair credential are keyed by the
   record, so peers keep their credentials and only the endpoint in their subscription changes. On
   failure: `blocked` remains, the response says which check failed.
6. The command then runs a normal sync, so a successful unblock converges the node's own set
   immediately instead of waiting for the daily cycle. The normal daily loop resumes either way.

### 7.5 Scenario C — the server changed

The request must come from the **new** server, because the check is "the IP that sent this request is
the IP the domain resolves to".

1. On the new box: `termit-uv init --domain new.example.com --relocate --agent-key-file <key>` installs it
   under the new name with the old agent key (a new `self_uuid` and path; the key file is `0600`).
2. `termit-uv migrate --relocate` builds the endpoint from the local identity and posts
   `POST {prefix}/agent/relocate` with `{domain, basic_vless_url}`; the agent key is the bearer.
3. MN verifies: the agent key names the node; the URL names the requested domain; the domain is not
   another node's; DNS for the new domain answers with the requesting address (every resolver that
   answers must agree); decoy site + certificate; VLESS end-to-end with the **claimed** self credential.
   The claimed identity is used because a blocked node has no probe credential in its config until it
   syncs — the same reason an unblock verifies the same way.
4. On success MN replaces `domain`, the self UUID, the profile and the certificate pin together; the
   node becomes `active` if it was `blocked` or `unavailable` (the move proved the new address works),
   and keeps its state if it was active. `probe_uuid` and every pair credential are keyed by `uv_id`, so
   **peers do not change credentials, only the host and path in their subscription** — the subscription
   serves `pair_uuid(A,P)` for a peer and `self_uuid(A)` for the owner's own node (§6.4).
5. On failure the old record is untouched and the new box reports the failing check. The old server
   keeps working until the owner shuts it down.

A node whose own name does not resolve cannot run its canary (the core resolves the Host it sends), so
`termit-uv init` warns about it and the operator has a broken box until DNS exists. That is a real dependency,
not a lab artifact: the preflight calls it out as a warning, never as a failure.

### 7.6 Enrollment policy

`enrollment.mode` on MN, default `invite+approval`:

- `invite` — a valid invite code is enough; the node becomes `active` after checks.
- `invite+approval` — checks run, node is `pending` until `termit-mn uv approve`.
- `open` — no invite needed (trusted private groups only; documented as unsafe for public use).

The mode is read once at startup, so `termit-mn invite policy --mode …` edits `enrollment.mode` in place
(comments and the rest of the file preserved, the result loaded again before it replaces the file) and
then says what matters: the running server keeps the old rule until `systemctl restart termit-mn`.

Invite codes: random 32-byte codes, stored hashed, with `max_uses`, `expires_at`, `note`, and a
`created_by` audit trail.

## 8. MN ↔ UV protocol

HTTP/1.1 + JSON over TLS 1.3, `Authorization: Bearer <agent_key>` for agent endpoints.
`api_version` is part of every response, so a build can name the protocol it was answered in; no side
refuses an answer whose version it does not recognise. What keeps mixed builds usable is the field
discipline: a new *request* field is the breaking kind and arrives only with an `api_version` change, while
an unknown value inside a known field is dropped, not refused. Errors are RFC 9457-style problem objects
with a stable `code` (`invite_invalid`, `pending_approval`, `check_failed`, `dns_mismatch`,
`rate_limited`, …).

The compatibility floor is declared, never enforced. The operator may name the oldest node build the mesh
supports — `min_agent_version` in the server's configuration, a plain release such as `v0.7.0` — and every
sync answer then carries it. A node below the floor says so in `termit-uv status` and in the `doctor`
report, and keeps serving: a refusal keyed on the server's word can strand a box that works, and the node
is the side that can tell an old build from a broken one. The value is read at startup, like
`enrollment.mode`, so a change needs a restart.

| Method | Path | Auth | Purpose |
|---|---|---|---|
| `POST` | `{prefix}/enroll` | invite code | register a new UV |
| `POST` | `{prefix}/agent/sync` | agent key | report self-check, the leaf pin and what the node serves (`transports`), fetch desired state + subscription URL |
| `POST` | `{prefix}/agent/unblock` | agent key | Scenario B |
| `POST` | `{prefix}/agent/relocate` | agent key | Scenario C |
| `GET` | `/` and every other path | none | the masking site: a generated page, or its own 404 |
| `GET` | `/sub/{token}` | token | base64 `vless://` list, plus `hy2://` where a node serves the transport |
| `GET` | `/sub/{token}/json` | token | sing-box profile (same peers, richer routing) |
| `GET` | `{prefix}/healthz`, `/metrics` | none / loopback | liveness, Prometheus |

The sync body also carries `transports`: one `{kind, version}` per transport the node serves — `vless`
always, `hysteria2` when the operator enabled it. That report is what the subscription renders links from,
so the server keeps no second copy of a node's configuration; a kind or a version it does not know is
dropped rather than refused, which keeps a newer node usable against an older server.

`{prefix}` is `api_prefix`: the path the node API is served under, random on a fresh install so that a
server is not discoverable by probing the names everyone uses. It is not a secret — every node carries
it inside its `mn.url` — and `docs/MN-API.md` is the reference for the whole surface. Subscriptions stay
at `/sub/…` and metrics at `/metrics`: the first is a URL already written into clients' applications,
the second a scrape target, and both are guarded by something better than an obscure path.

Everything else is the masking site: the same archetype the nodes serve (`decoy.template`, generated
from the domain and a seed stored in the database), held in memory and answered at `/` and for every
path the API does not claim. A management server that answered `404 page not found` for everything was
a fingerprint in itself — not a website, but a machine.

Admin operations are **not** HTTP endpoints: `termit-mn` talks to `/run/termit/mn/admin.sock`. This keeps the
public attack surface to two read-only-ish endpoints and one authenticated agent API.

Sync response shape (abridged; the whole set is in `internal/proto/sync.go`):

```json
{
  "api_version": "1",
  "self": {"id": "uv_…", "domain": "ams.example.net", "state": "active", "probe_uuid": "…"},
  "peers": [
    {"id": "uv_…", "domain": "fra.example.net", "label": "fra-2", "uuid": "pair-derived-uuid",
     "path": "/k7Q…/", "mode": "packet-up", "state": "active"}
  ],
  "subscription_url": "https://mn.example.com/sub/<token>",
  "policy": {"check_interval_seconds": 86400, "sync_interval_seconds": 86400,
             "min_sync_delay_seconds": 300, "mesh_poll_seconds": 900},
  "next_sync_after": "2026-11-10T07:31:00Z",
  "mesh_generation": "9f3c1a2b4d5e7788",
  "server_version": "v0.6.0",
  "min_agent_version": "v0.7.0"
}
```

The response carries the **complete desired peer set**; there is no delta field, because a delta is
what breaks when one response is missed. UV reconciles idempotently and reports what it changed in its
own log.

## 9. Health checking on MN

Two independent checks per node, plus one DNS consistency check:

| Check | How | Failure means |
|---|---|---|
| **VLESS end-to-end** | fetch a fixed `204` URL through a real XHTTP+TLS tunnel to the node, using the probe credential, 10 s deadline | the proxy is not usable |
| **Decoy + certificate** | TLS handshake with SNI=domain, full chain validation against system roots, hostname match, `NotAfter − now ≥ 14 d`, ALPN `h2`; then `GET /` → `200`, `text/html`, body > 512 B | the disguise is broken or the cert is about to expire |
| **DNS consistency** | A/AAAA for the domain via the system resolver **and** an independent DoH resolver must agree, and must contain the requester's source IP (enrollment, unblock, relocate only) | the domain does not point at this box, or a CDN/attacker is in front |

The VLESS check needs a protocol client. MN runs **one** `xray-core` probe worker (pinned version,
shipped by the MN installer) with N loopback SOCKS inbounds routed to N VLESS outbounds, regenerated
when the node set changes and validated with `xray -test` before start. The prober interface is
narrow (`Probe(ctx, vlessURL) (Latency, error)`) so a native Go implementation can replace it later
without touching the state machine.

The node checks its own WARP: the local self-check fetches
`https://www.cloudflare.com/cdn-cgi/trace` through the node's own tunnel, and `termit-uv status` / `termit-uv doctor`
report the address Cloudflare saw or say that traffic left directly. The sync request carries the same
ok/class summary as every other self-check item; the detail stays on the node. The design listed an
MN-side WARP check as optional and it is not in this build: the node-side check answers the question
with the node's own eyes, and MN already sees the result.

## 10. Traffic rules on the UV

Applied in Xray routing, therefore to proxied traffic only. Nothing here modifies host routes,
iptables or nftables, and nothing affects SSH, the decoy, ACME, or other services on the box.

Two independent rule groups, each toggleable in `config.yaml`:

- **Protect the host**: block `geoip:private`, `geosite:private`, outbound ports
  `25,135,137-139,445`, and BitTorrent (protocol sniffing).
- **Shape the user experience**: block `geosite:category-ads-all`, `geosite:win-spy`, and an
  optional extra domain list from a local file.

Of that list, only BitTorrent is implemented so far, behind `security.bittorrent_block` (off by
default): the core sniffs the first bytes of each connection, a `protocol: ["bittorrent"]` rule sends
the handshake to the blackhole, and `routeOnly: true` with an empty `destOverride` keeps the client's
destination intact — with `routeOnly: false` a sniffed HTTP `Host` replaces the address the client
asked for. The rest of the two groups is a plan, not a behaviour: nothing else is blocked today.

Egress, from `config.yaml`:

- `warp.mode: off` — everything not blocked exits `direct` from the node's address.
- `warp.mode: all` — **the default** — proxied traffic exits via WARP; a domain is forwarded to the
  sidecar unresolved, so the lookup happens inside the tunnel and does not leak the node's resolver.
- `warp.mode: selected` — only the domains in `warp.selected` (each also covering its subdomains)
  use WARP; everything else is direct.
- **Transport** — `warp.transport` names where the tunnel lives. `wireguard` (a fresh configuration, and
  the default) is a WireGuard outbound inside the core: one rule, `tcp,udp`, so UDP goes through Cloudflare
  like everything else; `proxy` is warp-cli's loopback SOCKS5 listener, which carries TCP only; `usque` is
  the MASQUE client as a pinned sidecar whose loopback SOCKS5 listener carries both protocols, so the split
  below does not apply to it. A file written before the key existed reads as `proxy` and is not migrated
  silently.
- **UDP on the proxy transport** — never through WARP, because SOCKS5 has no UDP ASSOCIATE. The catch-all
  rule is split, TCP to the tunnel (the balancer when fail-open is on) and UDP to `direct`, so DNS and
  QUIC leave from the node's address instead of disappearing.
- **Failure policy** — `warp.fail_open`, default `true`. Fail-open is implemented *inside the core*,
  not by rewriting the configuration: an Xray `observatory` fetches the trace through the WARP
  outbound every 30 s, and a `balancer` over that outbound has `fallbackTag: direct`. While WARP
  answers, traffic uses it; when it stops answering, new connections go direct without a config
  reload or a restart, and `termit-uv status` / `termit-uv doctor` say the exit is direct. With
  `warp.fail_open: false` there is no balancer and the route names the WARP outbound directly, so
  traffic fails rather than exposing the node's address.

WARP is on by default. The reason is not privacy theatre: an application on a client's device often
asks the internet what address it is coming from, and a node whose address is the answer is a node
that can be probed and blocked. `termit-uv init --no-warp` (or `warp.mode: off`) opts out, and an existing
configuration written before modes existed keeps the choice its old `warp.enabled` switch recorded.

WARP has three transports and none of them touches host routing, which is exactly the "direct traffic
intact" guarantee; full-tunnel WARP is rejected by design. `wireguard` (the default) is a WireGuard outbound
inside the core, carrying TCP and UDP, and it needs no device: the install step registers the node with
Cloudflare's API over HTTPS and writes the identity to the state directory. `proxy` is `warp-cli` in
**proxy mode** — a SOCKS5 listener on loopback carrying TCP only — installed from Cloudflare's signed apt
repository, and it does need `/dev/net/tun`. `usque` is the MASQUE sidecar: a pinned release with a checked
digest, its own unit, user and state directory, and no routing of its own. The core does not dial it
directly: `termit-usque-relay.service` listens on `127.0.0.1:40001` and the sidecar on `40002`, because
usque keys a UDP association on the control connection's own address and the core's SOCKS client does not
reuse it. Through that pair both protocols reach WARP; it is the answer for a box whose WARP client cannot
carry UDP, and for the day Cloudflare's client speaks only MASQUE. `termit-uv warp status` reports the
mode, the transport's own state and where a real fetch through the tunnel came out; `termit-uv warp mode`
changes the mode. If the
chosen transport cannot work on the box, the step and the command refuse with that exact reason, the mode
stays recorded, and the node keeps working with direct egress; nothing is silently degraded.
`termit-uv uninstall` disconnects the tunnel and leaves the package installed.

## 11. Safety invariants

These are non-negotiable and must be enforced by tests:

1. **No host-routing changes.** No `ip route`, no `ip rule`, no NAT, no iptables. The one exception is
   a single nftables table (`inet termit_uv` on a node, `inet termit_mn` on the server), and it exists
   only because someone typed `termit-uv firewall on`: `init`, `sync` and the agent leave the table
   itself alone, `firewall off` deletes it and puts the host back exactly as it was, and an apply that
   is not confirmed within five minutes removes itself. Two things fill the table's sets and never
   create, extend or remove the table: the guard (bans) and the blocklist refresh, which is why both are
   harmless with no table applied. One thing puts the table back: the boot unit, and only from the
   record of an apply the operator confirmed (or one whose confirmation window is still open) — a table
   nobody ever confirmed does not come back after a reboot.
2. **No traffic loss on failing checks.** MN never blocks a node because of a prober/DNS/agent error;
   `unknown` is distinct from `failed` everywhere.
3. **Config must validate before it is applied.** Every Xray config is written atomically, checked
   with `xray -test`, and only then activated (hot API first, validated restart second). A config
   that fails the test is rolled back and reported.
4. **Idempotency.** Re-running any installation or sync step converges to the same state and never
   regenerates issued credentials.
5. **Secrets never logged.** Tokens, keys and UUIDs are redacted in logs, metrics and error strings;
   credential files are `0600`.
6. **Certificate renewal never silently fails.** A renewal failure is surfaced in `termit-uv status`, in the
   MN check results, and (optionally) on stderr of the timer.
7. **Direct traffic stays direct.** Any feature that would capture host traffic is out of scope by
   construction, not by configuration.
8. **One core, and at most one transport the core cannot speak.** A node runs exactly one `xray`. The only
   other long-running proxy process is a QUIC transport, present only when the operator enables it, and it
   brings its own pinned version and checksum, unit, user and state directory, a loopback-only management
   listener, a handshake fingerprint that is re-tested on every upgrade, and no routing of its own: its
   traffic reaches the core through a loopback inbound, so the core's routing stays the only place policy
   lives. The WARP egress is the second kind of exception, and only when `warp.transport` names a sidecar:
   `warp-cli` in proxy mode (Cloudflare's package) or `usque` (ours to pin and check); the default,
   `wireguard`, is an outbound inside the core and adds no process at all. A sidecar gets the same terms —
   its own unit, user and state, a loopback listener, no routing of its own — and the core reaches it
   through a SOCKS outbound, so nothing about it can capture host traffic.

## 12. Lessons taken from the references

Read-only references: `references/autoXRAY-multi` and `references/Blitz` (both GPL-3.0). Their
mechanics inform this design; their code is **not** copied, and the directories are excluded from
this repository (see `.gitignore`).

Adopted ideas:

- nginx owns `:443` and terminates TLS, serves the generated decoy, and proxies one secret prefix to
  Xray; a browser sees a real certificate and a real-looking site while the secret path is a tunnel.
  (Xray's own fallback cannot do prefix routing — see `SPIKES.md` S1/S2.)
- Certificates are files on disk that both the proxy and the web layer read; renewal is a deploy
  action, not a proxy feature.
- A WARP tunnel inside the core (WireGuard, userspace) is the integration that provably leaves direct
  traffic alone; the older SOCKS5 sidecar (`warp-cli mode proxy`, port 40000) is kept for networks that
  filter WireGuard, at the cost of UDP.
- A versioned, opinionated default block list (ads, trackers, private ranges, SMTP/SMB ports,
  BitTorrent) is worth shipping as a preset, not as a blank rule editor.
- A believable decoy needs randomized branding, a matching favicon, a `robots.txt`, and an endpoint
  that answers a POST with a plausible error.

Deliberate rejections (each one is a defect we would otherwise inherit):

| Reference behaviour | Why rejected | Termit behaviour |
|---|---|---|
| One UUID shared by all clients and all transports | one leak = whole server | pairwise credentials, per-transport profile versioning |
| Interactive-only installer, unpinned `curl \| bash` at HEAD (4×) | not automatable, supply-chain risk | flags + env, pinned versions with checksums |
| No idempotency: re-run regenerates UUID/keys/paths | re-running destroys every issued config | credentials generated once, `termit-uv init` converges |
| `systemctl restart xray` on every cert renewal | drops all sessions | renewal at a scheduled low point, hot config path, restart gated by `xray -test` |
| No health checks; "WARP up" = `ss` shows a port | dead tunnels black-hole silently | end-to-end VLESS probe, cert/decoy probe, WARP trace check, fail-open policy |
| `chmod 744` private key, shared SOCKS creds on every client page, Hysteria auth = REALITY shortId | credential hygiene | `0600`, per-node secrets, no shared SOCKS |
| Host firewall absent; blocking only in Xray | fine for proxied traffic, no host hardening | both: Xray rules for proxied traffic, plus an opt-in nftables table the operator applies by hand |
| Subscription token = proxy password in the URL path | logged by every proxy, cannot be revoked alone | separate revocable read-only token |
| Hardcoded ports/paths, Debian-only nginx layout | not portable, not testable | single config file, ports and paths configurable |
| Admin API and panel exposed on public HTTP | needless attack surface | admin over unix socket, token auth, rate limits |

## 13. Deployment and operations

### 13.1 Target hardware

The reference deployment is a cheap VPS, and that is the budget the implementation must fit. These
are acceptance requirements, not aspirations:

| Component | vCPU | RAM | Disk | Notes |
|---|---|---|---|---|
| UV | 1 | 1 GB | 10 GB | Debian 13, amd64 or arm64, often an unprivileged LXC container |
| MN | 2 | 2 GB | 10 GB | single binary + SQLite + probe worker |

Steady-state budget on a UV (1 GB): kernel and base system ~80 MB, `sshd`/`systemd`/`journald`
~50 MB, nginx ~10–20 MB, Xray with a handful of clients (XHTTP only, no TLS in the proxy) ~40–70 MB,
`termit-uv` agent ~20–30 MB, `warp-cli` ~40–70 MB when WARP is enabled. Total ~200–320 MB, leaving
≥700 MB of headroom on a 1 GB box. Anything that would allocate per-connection buffers without a
bound is a defect.

Budget on the MN (2 GB): the server itself ~40 MB, SQLite a few MB to tens of MB, plus the Xray probe
worker at ~60 MB base and roughly 1–2 MB per monitored node. The worker's node count is capped by
policy so that 200 nodes stay comfortably inside the budget; the cap is logged, never silently
exceeded.

Disk: Debian base ~700 MB, Xray ~50 MB, both Termit binaries ~40 MB, decoy site < 5 MB, certificates
< 1 MB, SQLite a few MB per hundred nodes per quarter. Logs are capped (journald `SystemMaxUse`,
default 200 MB) and check history is pruned. **Builds never run on the target**: binaries are
cross-compiled and downloaded from releases.

Two consequences of the target class:

- **1 vCPU** means no busy loops, no per-request process spawns, and modest concurrency. The probe
  worker runs only on MN, never on a UV.
- **Unprivileged LXC containers** (the common cheap-VPS shape) have no `/dev/net/tun`. The `proxy`
  transport therefore cannot run there; the default `wireguard` transport lives inside the core and
  needs no device, which is why it is the default. A node whose transport cannot run must say so
  plainly instead of failing at runtime: preflight and `termit-uv warp status` name the reason and the
  node keeps serving on the direct path.

### 13.2 UV install

```bash
# the newest published release; --version vX.Y.Z pins one
wget -q https://github.com/shogun-kub/termit/releases/latest/download/uv.sh
sudo sh uv.sh --mn https://mn.example.com/6rfjdqfn5nno --domain node.example.com --invite ABCD-EFGH
```

The bootstrap resolves a release (the newest published one, or the one `--version` names), verifies its
checksum, installs `termit-uv`, pinned `xray-core`, and the `cloudflare-warp` package only for the `proxy`
transport, then runs `termit-uv init`.
Non-interactive by design; `termit-uv doctor` for diagnosis, `termit-uv uninstall [--purge]` for removal. A host
that cannot run WARP is not a failed install: `termit-uv init` records the refusal, keeps the mode the owner
asked for, and leaves traffic on the direct path.

An installed node moves to another release with `termit-uv upgrade`. Without `--to` it installs the
version the management server reported at the last sync — `server_version`, which `termit-uv status`
prints as `server:` — so a node follows its server and never picks up `latest` or a `vX.Y.ZpN` build.
The binary is checked against the release's `SHA256SUMS`, written beside the running one, and swapped in
with the previous file kept as `termit-uv.prev`; the new binary then runs `init` against the same
configuration, because it is the one that knows what its own release needs. A converge that fails puts
the previous binary back. `--check` says what a run would do and downloads nothing, `--release-base`
points at a mirror, and the menu's Node group says so plainly when there is nothing to install.

The Xray release is pinned in the binary with a per-architecture SHA-256 taken from the release's
`.dgst` file. The archive is verified before anything is extracted, cached by digest, and a mismatch
is refused instead of installed. `xray.download_base` (or `--xray-download-base`) points at a mirror,
a `file://` URL or a local directory when GitHub is unreachable or the host is air-gapped;
`xray.sha256.<arch>` overrides the digest for a re-packed build.

Configuration is `/etc/termit/uv/config.yaml` — strict YAML, `schema_version: 1` — and holds decisions
only: domain, ACME account, pinned core, decoy template, paths. Everything generated once (node
identity, decoy seed, installed core digest, certificate expiry, and the manifest of created paths)
lives in `/var/lib/termit/uv/state.json`, mode `0600`. Re-running `termit-uv init` or editing the
configuration therefore never regenerates a credential, and `termit-uv uninstall` removes exactly what the
manifest lists. The same file carries the agent's own bookkeeping — `schedule` (last and next sync,
last result, failure count), `last_self_check`, the peer list and `credentials.enrolled_at` — because
the loop must survive a reboot without asking MN when it last ran. `credentials.subscription_url` is
refreshed from every sync answer, not just from enrollment: a node enrolled by an older build, or one
whose token was rotated on the server, catches up at the next cycle. New fields are additive and
`schema_version` only moves when an old file must be rewritten.

ACME is configured by directory URL (`acme.directory`, or `--acme-ca`), defaulting to Let's Encrypt
production. `--acme-skip-tls-verify` exists only so the lab can use a self-signed ACME endpoint, and
`termit-uv doctor` reports it as a warning while it is on. The same knobs exist on MN.

The HTTP-01 challenge is answered by nginx: the client writes the token into the webroot nginx serves
and removes it afterwards. Termit never binds a public port for the challenge, so the promise that it
takes no port from another service holds here too.

Certificates are issued during `termit-uv init` and renewed by `termit-uv-renew.timer` — daily, jittered by
up to an hour, `Persistent` so a missed run happens after a reboot. A renewal is due when the
certificate is inside `acme.renew_before_days` of expiry (default 30). `termit-uv renew-cert` runs the same
check on demand and `--force` replaces the certificate whatever the clock says, which is what a drill
or a recovered CA needs. Failure never costs the node its certificate: nothing is written until the
CA returns a chain, the old file keeps serving, and `termit-uv status` reports `last run: failed` with the
failing step.

`termit-uv uninstall` removes exactly what the manifest lists, plus the vhost and the units, and puts the
distribution's default vhost back where it found it. It never removes packages: nginx may serve other
sites on the host, and it stays running with the stock page once Termit's vhost is gone.

`termit-uv init` converges one step at a time: `preflight`, `user`, `dirs`, `packages`, `warp`,
`xray-binary`, `identity`, `decoy`, `xray-config`, `web`, `units`, `certificate`, `start`, `warp-egress`
(plus the QUIC transport's and the MASQUE sidecar's own steps, which exist only when the configuration
asks for them). A step whose effect is already present
does nothing, an interrupted run resumes, and `--only/--skip/--force/--dry-run` make the pipeline
inspectable. Every generated file is checked before it becomes live: `xray -test` for the core config
(with the previous file restored on failure) and `nginx -t` for the vhost (same rollback).

The vhost changes shape with the node. Before a certificate exists it serves the decoy and the ACME
challenge over plain HTTP; once `fullchain.pem` and `privkey.pem` are in the certificate directory it
becomes the TLS vhost with the redirect, HSTS and the proxy location. Both shapes come from the same
generator, so the switch happens on the next `termit-uv init` after issuance.

The owner's own devices are local users: `termit-uv device list`, `termit-uv device add --label phone`,
`termit-uv device remove phone`. They live in the state file, are written into the core config, and are never
sent to MN — a family phone is not a mesh participant. `termit-uv uri` prints the `vless://` URI for the
node's own device; it is the one place a credential is printed on purpose, to stdout only.

`termit-uv options` renders the owner's page of client links: this node's own device, the local users it
accepts, the mesh peers as the subscription describes them, and the two subscription forms, each with a
copy button. It is a static file in the decoy site's tree under a random 32-character path, so nginx
needs no new location for it; the path is a capability like the subscription token, generated once,
stored in the state file and replaced by `--rotate`. The owner does not have to remember it: `init`,
`sync`, the agent's daily cycle and `device add`/`device remove` mint the address on the first run and
re-render the page afterwards (the decoy step rewrites the whole tree). The mesh half is
the reason the refresh is not left to the owner: a page that lists peers the server dropped, or omits a
device that was just added, is worse than no page at all.

`termit-uv wizard` is a front end to `init`, not a second installer: it asks for the domain, the ACME
contact, the decoy archetype, the egress mode, whether to serve the QUIC transport, whether to harden
the host and the invite code, writes each answer to `wizard.yaml`
beside the configuration so an interrupted run resumes with those answers as defaults, and then runs the
same `init` and `enroll` with the same flags. A node that is already installed is reported rather than
re-installed: the wizard runs the pipeline in dry-run mode and exits when every step reports its effect
present.

Its first question is the language: `locale` in the configuration, Russian by default, English second, and
the menu, the wizard and the page read it. Two rules keep the setting from becoming a trap. The console
uses it only where the environment is UTF-8 — on a C or latin-1 locale the menu falls back to English and
the wizard says so — while the page uses it unconditionally, because a browser is not a terminal.
Everything else a node prints (commands, logs, errors, `--json`) stays English: it is read by scripts and
quoted in bug reports, and translating it would make the tool untranslatable in practice.

`termit-uv doctor` answers three different questions, one screen each. The host group ("could this machine
be a node") is the preflight checks; the node group ("is it working") covers the state, the three units,
the certificate window, the decoy against its seed, and whether the core accepts its generated config; the
tunnel group covers the WARP transport, which is neither the machine's fault nor the node's. Each group
ends with its own tally and the report ends with one verdict — problems found, ok with warnings, or ok —
so the reader does not scan twenty lines to answer "is anything wrong". A check that cannot be performed is
a skip, never a failure: an unavailable `systemctl` says nothing about a node.

The node's clock belongs to `termit-uv-agent`: it wakes at the server-given `next_sync_after` (a day out,
jittered per node), runs the local self-check, syncs, and retries a failed cycle on a 5 min → 1 h
backoff. `termit-uv agent --once` runs one cycle by hand, `termit-uv status --json` prints what the loop persists
(schedule, last result, self-check, peers), `termit-uv unblock` asks MN to re-verify a blocked node and
re-admit it, and `termit-uv migrate --relocate` asks it to move the record to the domain the box now serves;
both then sync so the peers converge immediately.

`termit-uv` runs as two systemd units: `termit-uv-agent` (the daily cycle, self-check and sync) and
`termit-uv-renew` (the certificate timer). The agent is hardened with `NoNewPrivileges`,
`ProtectSystem=strict`, `ReadWritePaths` limited to its config and state directories and the site tree it
re-renders after a sync, a private `/tmp`, and `ProtectHome` — except when the configured MN CA file lives
under `/root`, `/home` or `/run/user`, where `ProtectHome` would hide the very file the unit must read.
The generator records that trade-off in the unit itself instead of leaving an unreadable CA to fail at the
first sync. Xray runs under its own generated unit as the unprivileged `termit` user, with an empty
capability set, `ProtectSystem=strict`, `SystemCallFilter=@system-service` and `MemoryDenyWriteExecute`. It
reads one config file and owns nothing else.

### 13.3 MN install

```
termit-mn init --domain mn.example.com      # creates config, DB, master key, admin token and invite code
systemctl enable --now termit-mn
```

Data in `/var/lib/termit/mn/` (SQLite, certs, probe workspace), config in `/etc/termit/mn/`,
admin socket in `/run/termit/mn/`. `termit-mn backup` produces a consistent snapshot (DB + config + certs);
`termit-mn restore` validates the file. **The master key must be backed up with the database**: without it,
pair credentials cannot be re-derived.

`termit-mn update` moves an installed server to another release: `--check` (the default) follows the
`releases/latest` redirect and says whether this build is behind, `--apply` downloads the binary,
checks it against the release's `SHA256SUMS`, swaps it in with the previous file kept as
`termit-mn.prev`, restarts `termit-mn`, and puts the previous binary back if the restart fails. The
server has nothing above it to follow, so an operator decides when it moves; the check is deliberately
absent from `termit-mn status` — and the server has no `doctor` command to hide one in — because a
status call must not reach GitHub and "an update exists" is not a fact about the mesh.

### 13.4 Operator workflow

```
termit-mn invite create --note "berlin box" --uses 1
termit-mn invite policy                # the rule a new node meets: invite, invite+approval or open
termit-mn invite policy --mode invite  # edits enrollment.mode; the server keeps the old rule until it restarts
termit-mn uv list                      # state, last seen, agent version
termit-mn uv approve uv_…   |  termit-mn uv block uv_…  |  termit-mn uv unblock uv_…  |  termit-mn uv rm uv_…
termit-mn check now uv_…    |  termit-mn check now --all
termit-mn policy show       |  termit-mn policy set check_interval 12h   # or probe_retries 3, block_after_failed_cycles 4
termit-mn sub rotate uv_…   # the only way to hand out a lost subscription URL again: the old one stops
termit-mn update --check    |  termit-mn update --apply    # the server's own binary, from the newest release
termit-mn status            # state counts, per-node last/next check, effective interval
```

On a node the equivalent pair is `termit-uv status` (its `server:` line is the build the server
reported) and `termit-uv upgrade`, which installs exactly that build (§13.2).

## 14. Data model (MN, SQLite)

```
uv(id, label, domain, self_uuid, probe_uuid, profile_json, state, state_reason,
   created_at, approved_at, last_seen_at, last_request_ip, agent_version,
   agent_key_hash, agent_key_sealed, sub_token_hash, sub_token_sealed,
   failed_cycles, credential_epoch, invite_id, notes,
   next_check_at, last_check_at, cert_sha256, home_mn, home_term)
uv_transport(uv_id, kind)                 -- what a node reported it serves: vless, hysteria2
uv_event(id, uv_id, at, from_state, to_state, reason, actor)
check(id, uv_id, at, kind, ok, latency_ms, error_class, detail_json)
invite(id, code_hash, created_at, created_by, expires_at, max_uses, used_count, note, revoked_at)
admin(id, name, token_hash, created_at, last_used_at)
settings(key, value)
audit(id, at, actor, action, target, detail_json)
replication_event(mn_id, seq, at, kind, payload)   -- the mesh journal, one log per server
replication_cursor(mn_id, seq)                     -- how far one server applied another's log
mesh_report(uv_id, at, payload)                    -- a node's report taken while its home was away
```

`home_mn` is the server that owns the row; empty means "this one", which is every node of a single-server
mesh. Every write that changes a replicated field appends one journal entry in the same transaction, and a
row homed by another server is never announced here — its log is what a third server pulls. A write to a
row another server homes is refused, and the scheduler does not pick such a row: a replica copies its peers'
nodes and probes only its own.

`home_term` counts home roles: a handoff increments it, so a write that still claims an older term loses the
merge — a server coming back from a partition cannot undo a handoff it missed. Two servers that both took the
same row while the mesh was apart end at the same term with different homes, and the row says nothing about
which is right: the merge refuses that write, so the pull keeps failing until an operator removes one of the
two records. A term cannot settle a tie, and guessing one would be a split brain with a clean log.

A node may sync to a replica while its home is down. The replica answers with the last desired state it
holds plus `read_only`, and keeps the report in `mesh_report`; its puller posts that report to the home,
which applies it as if the node had synced there. The transport is direct, HTTPS between the servers, and
described in `MN-API.md` §3.5–§3.6.

Indexes: `uv(domain)` (unique), `uv(state)`, `uv(last_seen_at)`, `uv(next_check_at)`,
`uv_event(uv_id, at desc)`, `check(uv_id, at desc)`, `check(at)` for pruning, `invite(code_hash)`
(unique), `admin(token_hash)` (unique), `audit(at)`. The journal is keyed by `(mn_id, seq)`, which is also
its read order. Retention: checks 90 days, audit 1 year, `uv_event` forever (small); the journal is pruned
by a follow-up, once a peer's cursor proves it no longer needs the tail.

## 15. Security summary

- TLS 1.3 only between UV and MN; certificates from Let's Encrypt via built-in ACME on both sides.
- Agent keys are 256-bit random values, stored as SHA-256 digests and found by digest through an index,
  so there is no byte-by-byte comparison to observe and nothing to brute-force; per-key and per-IP rate
  limits bound the online guesses a leaked digest would invite. A slow KDF buys nothing here: the secret is
  already unguessable, and it would be paid on every agent request.
- Subscription tokens separate, revocable, rotatable, and never accepted on agent endpoints. Their
  rate limit (60 requests per 15 minutes, per token) is load control, not brute-force defence: the
  token is unguessable, and a client that refreshes at launch must not see `429`.
- Admin surface on a unix socket with file permissions; every mutation audited.
- No secret in URLs except the subscription token; no secret in logs; no secret in metrics.
- The decoy site is deterministic per install (seeded), self-contained (no external fonts/CDNs), and
  answers a POST plausibly. It is a disguise, not a security control — stated explicitly in docs.
- Threat model and known limitations are in [`THREAT_MODEL.md`](THREAT_MODEL.md): malicious node owner,
  stolen subscription token, MN compromise (master key ⇒ all pair creds), offline DB theft, traffic
  analysis of the XHTTP flow, and the fact that blocking is cooperative.

## 16. Testing strategy

1. **Unit**: credential derivation vectors, subscription rendering and ordering, state machine
   transitions (table-driven, including every `unknown` path), invite validation, policy parsing,
   decoy determinism, URL parse/build round-trips, config generator golden files.
2. **Lab acceptance environment** (primary integration target): three Debian machines reachable over
   SSH — one MN and two UVs — with a LAN DNS zone that resolves the UV domains to their LAN
   addresses. This exercises the real stack: real systemd units, real Xray, real TLS handshakes, real
   process restarts, real reboot survival.
   - ACME: Let's Encrypt cannot validate a LAN-only name, so both binaries accept an **ACME directory
     URL** (`acme.directory`, `acme.email`, `acme.skip_tls_verify` for the lab only). A local
     Pebble instance signs the lab certificates; MN's prober accepts an extra root
     (`prober.ca_file`) so that certificate checks remain real (chain, hostname, expiry) rather
     than disabled.
   - Scenario C works unchanged in the lab: MN sees the UV's LAN source address and the LAN resolver
     returns the same address, so the DNS-consistency check is genuinely tested.
   - The lab suite is a Go test binary (`go test -tags=lab ./test/lab`) driven by environment
     variables (`TERMIT_LAB_MN`, `TERMIT_LAB_UV1`, `TERMIT_LAB_UV2`, `TERMIT_LAB_SSH_KEY`,
     `TERMIT_LAB_DNS_SUFFIX`). It provisions, asserts and cleans up, and is safe to re-run.
   - Scenarios: enroll → sync → both peers visible in each other's Xray config → kill one node →
     `unavailable` → `blocked` → peers converge → unblock → `active` → relocate to a rebuilt machine.
3. **Docker Compose** (developer convenience, no lab hardware): the same scenarios with Pebble and a
   local DNS server, for fast iteration. It is planned, not built — `test/integration` is the directory
   it will occupy; the lab remains the acceptance gate.
4. **Manual VPS checklist** (release gate for v1.0): fresh Debian 13, real domain, real Let's
   Encrypt, browser opens the decoy, `curl` through the subscription works from a phone, SSH
   unaffected, reboot survives, renewal simulated with `--force`, uninstall leaves no listeners.
5. **CI**: `golangci-lint`, `go vet`, the unit suite with `-race`, the release tool's tests and
   `govulncheck` over the explicit package patterns (`./cmd/... ./internal/...`, never `./...`: the
   third-party reference trees sit inside this module), and cross-builds for `linux/amd64` and
   `linux/arm64`. The lab suite runs on demand from the maintainer's machine (it needs the lab
   credentials, which never enter CI).

## 17. Open questions

Tracked in [`QUESTIONS.md`](QUESTIONS.md), which keeps every decision taken and the few still open. The
XHTTP mode and tuning constants, the decoy template source and the module path were settled there and are
not open.
