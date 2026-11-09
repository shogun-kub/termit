# Termit — implementation plan

Companion to [`DESCRIPTION.md`](DESCRIPTION.md). This document sequences the work, defines what
"done" means for each milestone, and records the risks we are buying down.

Estimates are ideal engineering days for one developer familiar with Go and Linux networking. They
exist to order the work and expose the critical path, not to promise dates.

---

## 0. Decisions already locked

| Decision | Value | Impact |
|---|---|---|
| Language | Go for both binaries, one module, CGO off | static binaries, pure-Go SQLite |
| Credentials | per-pair UUIDs derived by MN from a master key | `internal/cred` is a contract |
| Registration | invite codes + admin approval by default (`open` opt-in) | enrollment flow, invites table |
| MN deployment | single binary + SQLite + systemd + built-in ACME | no Postgres/Docker requirement |
| MN admin | unix-socket API + `mn` CLI, no web UI in v1 | no second auth surface |
| Subscription | base64 `vless://` list at `/sub/{token}`, sing-box JSON at `/sub/{token}/json` | two renderers |
| Licence | AGPL-3.0, public repository | no GPL/AGPL dependencies |
| Target | Debian 13, amd64 + arm64 | packaging, checks |
| Acceptance | three Debian machines over SSH (1 MN, 2 UV) + LAN DNS | lab suite is the release gate |

Still open (see §9): the exact XHTTP mode/tuning, whether WARP is enabled by default on fresh
installs, and the lab coordinates. Resolved since the first draft: module path and repository URL,
and the decoy inspiration source.

## 1. Critical path and ordering

```
M0  spikes ──► M1  UV standalone ──► M2  MN core ──► M3  full loop ──► M4  hardening/release
     (topology,      (install, cert,      (store, enroll,     (state machine,      (WARP, metrics,
      ACME, probe)    decoy, xray)         probe, subs)        sync, A/B/C)         docs, release)
```

The plan deliberately builds a **standalone, useful UV first**. A UV that installs, serves a decoy,
renews a certificate and survives a reboot is testable without MN, and every MN feature then has a
stable counterpart to talk to. Doing MN first would mean testing it against a mock node that does not
resemble the real thing.

Two things run in parallel from day one and are cheap:

- the **lab harness** (`test/lab`), needed by every later milestone;
- the **CI skeleton** (lint, unit tests, cross-build), which costs an hour and prevents drift.

---

## M0 — Spikes and scaffolding (3–5 d)

Purpose: remove the technical unknowns that would otherwise force a redesign in M1/M3. Every spike
ends with a written decision in `docs/SPIKES.md` and, where relevant, code that later milestones
reuse.

### M0.1 Toolchain and repository skeleton

- Install Go (and optionally Docker) on the development host; verify `go build ./...` on an empty
  module.
- `git init`, `.gitignore` (`references/`, `dist/`, `.dev/`, `*.pem`, `*.key`, `*.db`, `*.log`,
  `.env`), `LICENSE` (replace the SPDX placeholder with the canonical AGPL-3.0 text), `README.md`,
  `go.mod`.
- Directory skeleton from `AGENTS.md` §3 with `doc.go` files, `cmd/uv/main.go` and `cmd/mn/main.go`
  that only print `--help` and `version`.
- `.github/workflows/ci.yaml`: `gofmt -l`, `go vet`, `golangci-lint`, `go test ./...`,
  cross-build matrix. It must be green on the skeleton.
- `Makefile` with `build`, `test`, `lint`, `lab` targets (convenience only; plain `go` commands stay
  documented).

**Done when**: a fresh clone builds on Windows and Linux, CI is green, and `references/` is ignored.

### M0.2 Spike: the 443 topology — **done**, see `docs/SPIKES.md`

Measured on `termit-uv1` with Xray 26.11.9. Outcome:

- A VLESS+XHTTP+TLS inbound answers unknown paths with `404`; `fallbacks` is never consulted.
- A VLESS/RAW/TLS terminator does fall back to the decoy for browsers, but its path matching is
  **exact**, and XHTTP appends a per-request identifier to the path, so XHTTP traffic can never match.
- The XHTTP inbound itself works (`mode: packet-up`, plaintext) when reached directly.
- Xray 26 removed `allowInsecure`; verification is by `pinnedPeerCertSha256` (hex) or real CA roots.

Decision: the 443 front is **nginx** (TLS termination, decoy, prefix proxy to Xray on loopback).
xray no longer terminates TLS. Recorded in `DESCRIPTION.md` §5.1/§5.2/§13.1.

Left for M1: re-check whether XHTTP `mode: auto` works through nginx's HTTP/1.1 upstream
(`packet-up` is the known-good fallback).

### M0.3 Spike: ACME in Go with HTTP-01 — **done**, see `docs/SPIKES.md` S6

- Chose `golang.org/x/crypto/acme` over `lego` and `autocert` (first-party, small, and we own the
  challenge handler and the PEM files anyway).
- Pebble runs on the MN container with `httpPort: 80`; a cross-compiled test binary on the node issued
  a real certificate in 2.0 s, chain of 2, correct SAN, key `0600`, and reused it on a second call.
- Pitfall recorded: use `CreateCertFromOrder`, not the deprecated `CreateOrderCert`.

### M0.4 Spike: the probe worker — **done**, see `docs/SPIKES.md` S7

- One xray-core worker, one SOCKS inbound per node, `xray run -test` gate before start.
- Healthy: 72–131 ms end-to-end. Two nodes in one worker: 135 ms total.
- Node down and wrong pin both surface as `tunnel_closed` (5–6 ms); TLS validity needs the separate
  decoy/certificate checker, which is what M2.3 builds.
- Worker failures are errors from `Probe`, never node results — the `unknown` invariant holds.

### M0.5 Lab harness bootstrap

- SSH client wrapper (`golang.org/x/crypto/ssh`) with host-key pinning from an env var, command
  runner, file upload, and log capture.
- `test/lab` package with `TestLabSmoke`: connect to all three machines, assert Debian version, free
  ports, DNS resolution, and NTP offset; print a report.
- Provisioning helpers: copy a binary, install a systemd unit, tail a journal, wait for a condition.

**Done when**: `go test -tags=lab ./test/lab -run Smoke` passes against the three machines, and a
failed run prints enough to debug without logging in by hand.

---

## M1 — UV standalone (8–12 d)

Purpose: a fully working node with no MN involved. This is the first shippable artifact: a user can
install it, get a valid certificate, see a decoy site, and connect with a manually built
`vless://` URI.

### M1.1 CLI, config, state — **done**

- `uv init`, `doctor`, `status`, `uninstall` and `version` are implemented. The rest of the command
  set is declared and exits `3` ("not implemented in this build"), so a caller script can tell "not
  written yet" from "failed".
- Configuration: strict YAML (`KnownFields`), `schema_version: 1`, defaults for every field, and one
  message listing *all* validation problems. It holds only what the owner decides — domain, ACME
  account, pinned core, decoy template, paths — never a generated value.
- State (`state.json`, `0600`): identity, decoy seed and template, installed core version and digest,
  certificate expiry, and the manifest of every path, user and unit Termit created. Schema-versioned.
- Journal (`install-journal.json`, `0600`, last 20 runs): one record per step, so `uv status` can say
  where a run stopped and an unfinished run reads as "interrupted".
- Re-running `uv init` is a no-op: every step reports `present` and `state.json` stays
  byte-identical (verified on the lab, same sha256 before and after).

### M1.2 Install engine — **done**

- Ordered steps, each with `Check` ("is my effect already present?") and `Run`: `preflight` → `user`
  → `dirs` → `packages` → `xray-binary` → `identity`. A failing step stops the run and is journaled;
  the next run resumes at the first unsatisfied step. `--only`/`--skip`/`--dry-run` exist for
  debugging and are covered by tests.
- Preflight: root, Debian ≥ 13 (12 warns), systemd, architecture, clock, ownership of :80/:443, DNS,
  ACME directory reachability, `/dev/net/tun` when WARP is enabled, free space (measured on the
  nearest existing ancestor, so it works before the first install), memory, apt.
- Core: a pinned release table with a per-architecture SHA-256 that is **verified before anything is
  extracted**; the archive is cached by digest, a mismatch is refused rather than installed, and the
  installed binary is checked by running `xray version` and hashing it into the state.
  `xray.download_base` accepts an HTTP(S) URL, a `file://` URL or an absolute directory — a mirror or
  an air-gapped copy — and `xray.sha256.<arch>` overrides the pin for a re-packed release.
- `uv uninstall [--purge]` walks the manifest backwards and removes only what is in it. Without
  `--purge` it keeps `/etc/termit/uv` and `/var/lib/termit/uv`, so a reinstall keeps the identity;
  packages are never removed, and no other nginx site is touched. `--purge` also removes the two
  trees, their now-empty `termit` parents and the service user.

Measured on `termit-uv1` (Debian 13, 1 vCPU / 1 GiB), 2026-11-09:

| Run | Result |
|---|---|
| fresh `uv init` | 2 m 28 s total, 2 m 21 s of it the 35 MiB core download through a mirror |
| second `uv init` | 0.4 s, every step `present`, `state.json` byte-identical |
| `uv uninstall --purge` | no files, no user, no units left; nginx and its default site untouched |

The certificate, decoy, generated Xray/nginx configuration, units and self-test belong to M1.4–M1.5;
until they land `uv init` ends by naming them instead of pretending the node proxies traffic.

### M1.3 Decoy site — **done**

- `internal/uv/decoy` generates the site deterministically from `(domain, seed, template)`; the seed is
  generated once at install and kept in the state file, so the site survives reinstalls, reboots and
  an owner who edits the configuration.
- Two archetypes: the **branded enterprise portal** (dark or light preset, invented product, badge,
  infrastructure-register subtitle, non-standard field labels, checkbox, full-width CTA, fictional
  footer company and legal links) and the **product sign-in screen** (light, brand row, SSO divider,
  legal footer). Both are original markup and CSS, written here.
- Randomised per install: product name, badge, subtitle, credential labels, field ids, CTA, footer
  company, link set, accent palette, card width, corner radius, label placement (stacked or split),
  footer arrangement, SSO presence, submit path, cookie name, error code and message.
- The generator also produces `404.html`, `robots.txt` (disallowing a seed-chosen set of internal-
  looking paths) and a 32×32 PNG favicon drawn from the same seed, so the tree is complete and
  self-contained.
- Anti-fingerprint rules, each enforced by a test over many seeds: no `http(s)://`, `@import`,
  `preconnect`, `dns-prefetch`, `integrity` or `crossorigin` anywhere in the output; no
  generator-shaped identifiers (`f_<hex>`, `csrf_<hex>`); the submit path varies per deployment and
  is the one the form actually posts to; layout varies with the seed; the favicon varies with the seed.
- `internal/uv/web` renders the nginx fragment the page needs, from the same `decoy.Site`: `error_page
  404`, and a `location` for the submit path that answers `POST` with the site's own `401` JSON, its
  `Set-Cookie`, `Cache-Control: no-store` and `X-Request-Id $request_id`, and `405` for anything else.
  A golden test pins both the page and the fragment so they cannot drift apart.
- `uv renew-site [--template T] [--seed S] [--dry-run]` regenerates the tree and the fragment,
  `nginx -t`s and reloads nginx **only when Termit already owns the vhost** (it does not until M1.5),
  and never touches the identity or the certificate.
- Tests: determinism (same seed ⇒ byte-identical tree), seed and template variation, spec validation,
  the anti-fingerprint rules above, `robots.txt`, favicon validity, stale-file removal, and golden
  pages for both archetypes.

Two decisions taken while building it, both reported in `SPIKES.md` S9:

- **nginx answers the form; there is no agent-side HTTP server.** The plan above had an idea for a
  unix-socket server with PROXY protocol and an artificial delay. `AGENTS.md` §5 forbids the agent
  from listening on the public ports and nginx already terminates TLS, so the endpoint became a
  `return 401` location — which is also indistinguishable from an application to a scanner. The lost
  delay is the one cost, and it is not worth a second daemon on a 1 GiB box.
- **The product archetype is brand-free.** A faithful re-creation of a real vendor's sign-in screen
  means shipping its trademark in an AGPL repository and giving every deployment the same logo. The
  shipped template is an original screen with an invented product name, and its links point at local
  paths rather than the real vendor.

Measured on `termit-uv1` (nginx 1.26.3, the node's own site tree):

| Request | Answer |
|---|---|
| `GET /` | `200 text/html`, 5.6 KB, no URL of any kind in the page |
| `POST /v1/session/open` (the form's action) | `401`, `application/json`, `Set-Cookie: sess=…; HttpOnly`, `X-Request-Id` |
| `GET` the same path | `405` |
| `GET /does-not-exist` | `404` with the generated `404.html` |
| `/robots.txt`, `/favicon.png`, `/assets/boot.js` | `200` `text/plain`, `image/png` 32×32, `application/javascript` |

The site tree moved to `/var/www/termit/{site,acme}`: nginx runs as `www-data` and could not traverse
`/var/lib/termit` (0750), so the decoy returned `403`. State and certificates stay in `/var/lib/termit/uv`.
### M1.4 Certificate lifecycle — **done**

Built:

| Piece | Where | Notes |
|---|---|---|
| Challenge publication | `internal/uv/acme` | `Config.ChallengeDir` writes the token into the webroot nginx serves; `ChallengeHandler` stays for tests and for a host where Termit owns `:80` |
| Step | `internal/uv/install/certificate.go` | issues when absent or inside the renewal window, records `NotAfter` in the state, then rewrites the vhost and reloads nginx |
| Schedule | `internal/uv/service` | `termit-uv-renew.timer` (`OnCalendar=daily`, `RandomizedDelaySec=1h`, `Persistent=true`) runs `termit-uv-renew.service` → `<binary> renew-cert --config …` |
| Drill | `uv renew-cert [--force]` | without `--force` it is the same check `uv init` makes; with it the certificate is replaced regardless |
| Reporting | `uv status` | `certificate: expires … (in N days)`, and a failed run shows up as `last run: failed` with the failing step |

Measured on `termit-uv1` against Pebble:

| Check | Result |
|---|---|
| `uv init` with `--acme-ca …/dir` | `certificate ok`; `fullchain.pem` `0644`, `privkey.pem` `0600`, `account.key` `0600` |
| Issued certificate | `issuer=CN=Pebble Intermediate CA …`, 90 days |
| Vhost after issuance | flipped from plain HTTP to TLS in the same run (`web` ran again inside the certificate step) |
| `GET https://…/` | `200` over HTTP/2; `:80` → `301` |
| `vless://` through the issued certificate | `200` |
| `uv renew-cert` (not due) | `certificate present, valid until …`, exit 0 |
| `uv renew-cert --force` | new serial, node kept serving, challenge files removed from the webroot |
| Renewal against a dead CA | fails loudly, `last run: failed` + `certificate failed` in `uv status`, **the certificate and the site stay untouched**, and the next run succeeds once the CA is back |
| `uv init` afterwards | 1 ok, 11 present, nothing pending |

One finding, recorded in `SPIKES.md` S11: `--force` initially changed nothing. The engine's `Force`
skips the step's `Check`, but the step itself called `acme.Ensure`, which renews only inside the
window — so the flag had no effect and the serial stayed the same. The step now reads
`stepEnv.forced` and calls `acme.Issue` in that case. A forced run must mean *do it again*, not
*skip the check*.

Deliberately different from the plan: the timer, not the agent, owns the renewal clock. There is no
agent loop yet (M3), and a daily oneshot is easier to inspect, survives a reboot via `Persistent` and
needs no backoff logic of its own — a failure is retried tomorrow and visible in `uv status` today.

### M1.5 Config generation, units, hot API — **done**

Built:

| Piece | Where | Notes |
|---|---|---|
| Typed Xray config + generator | `internal/uv/xray` | VLESS inbound on `127.0.0.1:8443`, `security: none`, `network: xhttp`, `mode: packet-up`; loopback management API on `10085`; golden file, deterministic bytes |
| Profile and client URI | `internal/proto/profile.go` | `BuildVLESSUrl`/`ParseVLESSUrl`, round-trip test, refuses `allowInsecure` and any transport that is not `xhttp`+`tls` |
| nginx vhost | `internal/uv/web` | two shapes from one generator: plain HTTP before a certificate exists, TLS afterwards (`http2 on;`, HSTS, `/assets/` cache, ACME location on `:80`, `location /<secret>/` → Xray) |
| Units | `internal/uv/service` | `termit-xray.service` runs as `termit`, `ProtectSystem=strict`, empty capability set, `SystemCallFilter=@system-service`, `MemoryDenyWriteExecute`; nginx gets a small drop-in |
| Install steps | `internal/uv/install` | `xray-config` → `web` → `units` → `start`, each converging and journaled |
| Owner's devices | `uv device list|add|remove` | local users in the state file, never shared with the mesh |

Measured on `termit-uv1` (1 vCPU / 1 GB, Debian 13):

| Check | Result |
|---|---|
| `uv init` on an installed node | 1 ok (`preflight`), 9 present, `not yet installed: certificate` |
| `systemctl is-active termit-xray nginx` | active, active |
| Loopback listeners | `127.0.0.1:8443` (XHTTP), `127.0.0.1:10085` (API), `:80` (nginx) — Xray has no public port |
| `GET /` | `200`, the generated page, `5665 B` |
| `POST <form path>` | `401` + `Set-Cookie` + `X-Request-Id`, no app server behind it |
| `GET <form path>` / `GET /nope` | `405` / generated `404` |
| `GET /.well-known/acme-challenge/<token>` | `200` with the token written into the webroot |
| TLS vhost with a temporary self-signed pair | `200` over HTTP/2, `:80` → `301` to `https`, POST → `401` |
| `vless://` client through nginx (`https://example.com`, `https://example.org`) | `200`, `200` — client → nginx over h2, nginx → Xray over HTTP/1.1 |
| `uv device add --label phone` | config rewritten, core restarted, `xray api inbounduser` shows `local-<id>@termit`; `remove` drops it again |

Three findings, all recorded in `SPIKES.md` S10:

1. `xray -test` picks its parser by file extension, so the staged config must end in `.json`;
2. the core runs as `termit` and could not read `/etc/termit/uv/xray.json` until the directory's group
   became `termit` and `/etc/termit` became traversable — and the *check* had to verify the group,
   otherwise the fix never ran;
3. nginx 1.25+ needs an explicit `http2 on;`: without it a client offering h2 gets its preface refused
   with `400`, and XHTTP does offer h2.

Deliberately different from the plan:

- **No hot add/remove through the Xray API in this milestone.** The `adu`/`rmu` path was exercised by
  hand and works, but a restart costs one connection and only happens when the generated config
  changes, which is rare until the mesh sync exists. The hot path lands with M3 where the peer set
  changes daily; doing it now would mean writing client-diff code that only sync can use.
- **The distribution's default vhost is parked, not deleted.** It is renamed to
  `sites-available/default.termit-disabled` (anything inside `sites-enabled` is loaded, since Debian's
  include is a glob) and `uv uninstall` puts it back. Without this a request to the bare address
  answered Debian's welcome page — a louder fingerprint than the decoy is quiet.
- **No `termit-uv.service` yet**: the agent loop it would run is M3. Only the core and nginx are
  supervised so far.


### M1.6 Standalone acceptance — **done**

Run on `termit-uv1` as a full cycle: clean host → install → verify → reboot → verify → uninstall →
audit → install again.

| Step | Result |
|---|---|
| Fresh `uv init` (Pebble, new empty host) | 11 ok, 1 present (`packages`); the certificate is issued in the same run |
| `uv doctor` | 16 ok, 1 warn (the host sees its own name as `127.0.1.1`), 1 skip (no `/dev/net/tun`), exit 0 |
| Decoy, certificate verified against the lab root | `200` over HTTP/2 |
| `POST` the form | `401` with `Set-Cookie` and `X-Request-Id` |
| `uv uri` → a client built from that URI | `https://example.com` through the tunnel: `200` |
| `systemctl reboot` | all three units `active` + `enabled`, no stale listeners, site `200`, certificate serial unchanged, tunnel `200`, `uv doctor` green |
| `uv uninstall --purge` | `/etc/termit`, `/var/lib/termit`, `/usr/lib/termit`, `/var/www/termit` gone; units gone; user gone; `8443`/`10085` closed; the stock vhost back in `sites-enabled` and answering `200`; nginx and `ca-certificates` still installed |
| Install again afterwards | 11 ok — a second node from scratch on the same host |

What the acceptance found, and what changed because of it:

1. **Uninstall removed directories in the same pass as the files they held**, so `site/assets` was
   attempted before `boot.js` inside it was gone, and `/var/www/termit` survived with an empty
   `assets/` in it. The walk is now three passes — files, then the service user, then directories
   deepest-first — and the decoy step records the subdirectories it creates instead of only the files.
2. **Uninstall tried to remove directories the distribution owns**: `/etc/systemd/system` and
   `/etc/nginx/sites-available` were recorded as ours. The units and web steps no longer record a
   parent that is not Termit's.
3. **The `sites-enabled` parking fix from S10 had never reached the code.** `disableStockSite` still
   renamed the file in place, and since Debian\'s include is a glob nginx kept loading it. It only
   surfaced because the audit after uninstall showed `enabled: default.termit-disabled` instead of
   `default`. It now parks the file in `sites-available/` and the restore works.
4. **`uv doctor` had no node half at all**, so "doctor green" would have meant "the host could be
   installed", not "the node works". It now also checks the state, the three units, the certificate
   window (fail when expired, warn under two weeks), the decoy against its seed, and that the core
   accepts its generated config.

Also added for the acceptance and for ordinary use: `uv uri`, which prints the `vless://` URI for the
node\'s own device. Without it a standalone node had no way to hand its owner a working client.

---

## M2 — MN core (8–10 d)

Purpose: a management server that accepts nodes, verifies them, and serves subscriptions. Still no
daily loop: sync exists but the UV drives it manually (`uv sync`).

### M2.1 Store and bootstrap

- SQLite schema and migrations (modernc.org/sqlite), the tables from `DESCRIPTION.md` §14.
- `mn init`: config, DB, master key (printed once, `0600` file, env override supported), admin token,
  initial invite code, ACME bootstrap.
- `mn serve`: TLS server, `/healthz`, `/metrics` on loopback, graceful shutdown.
- `mn backup` / `mn restore` with integrity checks and a schema-version guard.

### M2.2 Enrollment

- `POST /api/v1/enroll`: invite validation (hash, uses, expiry), URL parsing and validation, domain
  syntax, duplicate detection, rate limiting, DNS consistency check, decoy + VLESS probe, credential
  issuance (`agent_key`, `sub_token`, `probe_uuid`), state `active` or `pending`.
- Admin CLI: `mn invite create|list|revoke`, `mn uv list|show|approve|reject`.
- Problem responses with stable codes; nothing sensitive echoed back.

### M2.3 Prober

- `probe.Prober` interface; xray-core worker implementation from M0.4; cert/decoy checker; DNS
  checker with two resolvers (`system`, DoH) and `extra_roots` for the lab.
- Result model: `ok | failed | unknown` plus `error_class` (`timeout`, `tls_invalid`, `http_error`,
  `tunnel_closed`, `dns_mismatch`, `worker_unavailable`, …).
- Per-node concurrency limits, global worker pool, circuit breaker: if the worker is unhealthy, all
  results become `unknown` and the scheduler is told not to act (invariant 2).

### M2.4 Subscription and credentials

- `internal/cred`: derivation + vectors + property tests (order independence, epoch sensitivity).
- `GET /sub/{token}`: base64 list; `GET /sub/{token}/json`: sing-box profile; ETag; cache headers;
  stable ordering; degraded markers for `unavailable` peers.
- Token rotation (`mn sub rotate`), revocation, per-token rate limit.
- Golden tests for both renderers with a fixed peer set.

### M2.5 Sync v0

- `POST /api/v1/agent/sync` returning the complete peer set with pairwise credentials, the
  subscription URL, policy hints, and `next_sync_after`.
- `uv sync` applies it with the hot API + reconcile logic and reports what changed.
- Acceptance: two lab UVs enroll; each subscription contains both nodes; a `curl` through any
  subscription URI works from a third machine; pulling a node's plug makes its probe fail.

---

## M3 — Full lifecycle (8–12 d)

Purpose: the three scenarios from the concept, end to end, verified in the lab.

### M3.1 Scheduler and state machine (MN)

- Queue keyed by `next_check_at` with jitter; worker pool; per-node serialization.
- Retry policy inside a cycle (3 × 10 min, configurable); cycle outcome aggregation (`ok`, `failed`,
  `unknown`).
- Pure transition table + side effects (peer invalidation, subscription ETag bump, audit event).
- `mn check now`, `mn policy set`, `mn uv block|unblock|rm` (admin override, audited).
- Retention job (checks 90 d, audit 1 y).

### M3.2 Agent loop (UV)

- Long-running scheduler: daily jittered cycle, never before `registered_at + 2 h`, honoring
  `next_sync_after`; exponential backoff when MN is unreachable (traffic off the box keeps working).
- Local self-check included in the sync report (ports, xray process, cert expiry, WARP state) so MN
  can distinguish local breakage from path breakage.
- Reconcile: compute desired vs actual client set, apply via hot API, rewrite the config file,
  verify with a canary connection, report the applied state and `xray -test` result.
- `uv unblock`, `uv migrate --relocate`, `uv status --json`.

### M3.3 Scenario A acceptance

Enroll two UVs → wait for one cycle (or `mn check now`) → both nodes present in each other's Xray
config and subscription → kill node 2 → cycle fails after retries → `unavailable`, still in node 1's
config → second failed cycle → `blocked`, removed from node 1's config at its next sync → restart
node 2 → stays blocked until an explicit unblock → `uv unblock` → `active`, peers re-add it.

### M3.4 Scenario B acceptance

From a rebuilt machine (new `self_uuid`, same agent key), `uv init --relocate` followed by
`uv unblock --vless-url <new>` (Scenario B accepts a new URL, which is what a rebuild produces) →
MN verifies DNS/decoy/VLESS → `active`; peers converge; the subscription contains the new URL. A
blocked node that merely comes back healthy stays blocked until this explicit request.

### M3.5 Scenario C acceptance

Rebuild UV2 on a different machine/domain, keep the agent key → `uv init --relocate --domain
uv2b.lab.internal` → MN verifies source IP == new DNS, probes the new node, swaps host/path →
peers keep the same pair credentials and only change the endpoint in their subscription → old node's
`self_uuid` no longer accepted anywhere.

### M3.6 Failure-drill suite

Scripted, in the lab: MN down for a day (nodes keep working, converge after recovery), prober killed
(`unknown`, no node blocked), clock skew, expired certificate, DNS flipped to a wrong address, disk
full on MN, SQLite lock contention. Each drill asserts the invariant it protects.

---

## M4 — Hardening and release (6–8 d)

### M4.1 WARP

- `warp-cli` proxy-mode install and supervision; health check via `cdn-cgi/trace`; fail-open and
  fail-closed modes; `uv warp status|mode`.
- Refusal path with a clear reason when unsupported (no `/dev/net/tun`, unsupported arch, missing
  virtualization).
- Optional MN probe that reports `warp=on`.
- The lab cannot exercise WARP (LXC, no `/dev/net/tun`); accepted by the maintainer on 2026-11-09.
  The implementation follows the reference's `warp-cli` proxy mode, and real verification moves to the
  release checklist on a host with TUN.

### M4.2 Operations surface

- `docs/OPERATIONS.md` (install, upgrade, rotate, recover, backup, uninstall, troubleshooting),
  `docs/PROTOCOL.md` (API contract with examples), `docs/THREAT_MODEL.md`.
- `mn status` dashboard text, Prometheus metrics, structured logs with redaction tests.
- Bootstrap installer script (`deploy/install/uv.sh`) with checksum (and optional minisign)
  verification, plus an upgrade path that preserves state.

### M4.3 Release

- `v0.1.0` tag; `goreleaser` (or a small script) producing linux/amd64 + linux/arm64 archives,
  `SHA256SUMS`, and a systemd unit bundle.
- CI: lint, unit, race, govulncheck, cross-build, Compose integration; lab suite documented as a
  manual gate.
- Final manual VPS checklist on real Debian 13 with a real domain and Let's Encrypt.

---

## 2. Cross-cutting workstreams

These are not separate milestones; they must be updated in the milestone that touches them.

| Workstream | Rule |
|---|---|
| Docs | The change that alters behaviour updates the doc in the same commit. |
| Tests | Every milestone lands its unit tests; the lab scenario for a milestone lands with it. |
| Security | Redaction tests, `0600` checks, and an adversarial review pass at the end of M2 and M4. |
| Observability | New state transitions and probes log once, structurally, with the identifiers needed to debug. |

## 3. Suggested order of the first ten commits

1. `chore: scaffold module, layout, CI, licence, gitignore`
2. `feat(proto): profile v1 constants, VLESS URL build/parse with round-trip tests`
3. `feat(cred): pairwise derivation with test vectors`
4. `feat(uv/state): atomic JSON persistence with schema versioning`
5. `spike(uv): topology validation harness` (throwaway, kept under `spike/`)
6. `feat(uv/xray): typed config model and deterministic generator + golden tests`
7. `feat(uv/decoy): deterministic generator + embedded server + one template`
8. `feat(uv/acme): HTTP-01 issuance and renewal against Pebble`
9. `feat(uv/install): ordered resumable install with preflight and uninstall manifest`
10. `feat(uv): init/doctor/status end to end on the lab box`

## 4. Risks and mitigations

| # | Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|---|
| R1 | VLESS+XHTTP+TLS cannot share `:443` with the fallback decoy as assumed | Medium | High | M0.2 spike decides; two fallback topologies are pre-designed behind one interface |
| R2 | `cloudflare-warp` unsupported on some hosts/arches | Medium | Medium | refuse with a reason; never silently degrade; document supported matrix |
| R3 | MN's vantage point differs from users' and blocks healthy nodes | Medium | High | `unknown` invariant, retries, configurable thresholds, admin override; multi-vantage later |
| R4 | Daily convergence means up to 24 h of stale peers | High (by design) | Low | documented; `mn check now` + admin CLI for urgent cases; sync interval configurable |
| R5 | Let's Encrypt path untested in the LAN lab | High | Medium | Pebble covers the ACME code path; real-domain checklist gates `v0.1.0` |
| R6 | MN compromise or DB theft exposes all pair credentials | Low | Critical | master key can live outside the DB; backups encrypted by the operator; threat model documented |
| R7 | Scope creep toward a panel/quota/billing product | High | Medium | non-goals list is explicit; v1 API stays minimal |
| R8 | Dependency churn (SQLite, ACME, xray-core pinning) | Medium | Medium | pin versions + checksums, `govulncheck`, small module graph |
| R9 | Development host has no Go/Docker and targets Linux | High | Low | M0.1 installs the toolchain; cross-compile; lab machines are the runtime |
| R10 | Decoy templates look fake | Medium | Medium | user supplies the inspiration source; generator is pluggable; `uv renew-site` iterates |

## 5. Explicitly deferred (post-v1)

- REALITY as a second profile; Hysteria2/TUIC; MTProto.
- Multi-vantage probing; federation between MNs; node reputation.
- Web admin UI; traffic accounting and quotas; Telegram bot.
- IPv6-only nodes; non-Debian distributions (an explicit portability milestone, not a side effect).
- Hot certificate reload without a restart (depends on Xray capabilities).

## 6. Verification matrix (what proves each claim)

| Claim | Proof |
|---|---|
| Direct traffic unaffected | Lab: SSH session open during install, sync, WARP toggle, and certificate renewal never drops |
| Decoy is convincing | Browser + `curl` see a valid chain, HTML, plausible headers, working POST endpoint; no external requests in a network capture |
| Proxy works | Real HTTP fetch through a subscription URI from a third machine |
| Blocking is correct | Full transition table tests + scenario A drill in the lab |
| No false blocking | Failure-drill suite: prober/MN failures produce `unknown`, state unchanged |
| Secrets are protected | `0600` assertions, redaction tests, `git grep` for key patterns in CI |
| Uninstall is clean | Manifest diff before/after on a fresh lab machine |
| Idempotency | Install twice, sync twice, relocate twice; byte-identical state and config |

## 7. Lab environment

The maintainer provides three Debian 13 containers on Proxmox, reachable over SSH as root:
`termit-mn` (2 vCPU / 2 GB), `termit-uv1` and `termit-uv2` (1 vCPU / 1 GB each, ~8.6 GB free disk).
LAN DNS resolves `termit-*.lan`; outbound 443 works. Coordinates, host keys and the key path are kept
in the gitignored `.spike-out/lab.md`, never in committed files.

Answered:

- Debian 13 on all three, x86_64, NTP synced, outbound internet available.
- Private addresses only, so Scenario C proves the DNS-consistency logic but not a public-IP path.
- `warp-cli` **cannot** run: these are unprivileged LXC containers without `/dev/net/tun`.
- **WARP acceptance is deferred to the release checklist** (maintainer decision, 2026-11-09). The
  implementation follows the reference's `warp-cli` proxy mode, which is known to work; the lab can
  only verify that WARP is detected as unsupported and refused with a clear reason.

Answered during M1: the lab ACME is **Pebble on the MN container** (`httpPort: 80`, directory
`https://termit-mn.lan:14000/dir`), which is what M0.3 and every lab install ran against. Pebble's
self-signed chain is why `acme.skip_tls_verify` exists.

## 8. Estimated effort

| Milestone | Ideal days | Cumulative |
|---|---|---|
| M0 spikes + scaffolding | 3–5 | 3–5 |
| M1 UV standalone | 8–12 | 11–17 |
| M2 MN core | 8–10 | 19–27 |
| M3 full lifecycle | 8–12 | 27–39 |
| M4 hardening + release | 6–8 | 33–47 |

The range is wide because M1 and M3 depend on how much the M0.2 topology spike and the Xray hot-API
path cooperate.

## 9. Open items to confirm before M1 starts

1. ~~Go module path / repository URL~~ **Resolved**: `git@github.com:shogun-kub/termit.git`, module
   path `github.com/shogun-kub/termit`. The `origin` remote is configured; nothing is pushed.
2. ~~Decoy-site inspiration source~~ **Resolved**: the `autoXRAY-multi` generator family plus three
   maintainer-provided pages, captured locally under `captures/decoy-samples/` (gitignored, no URLs
   recorded). See `DESCRIPTION.md` §5.3 and `IMPLEMENTATION_PLAN.md` M1.3.
3. Lab coordinates and the DNS suffix, plus the answers to §7.
4. Whether `uv` should enable WARP by default on fresh installs (`off` is the current default).
5. Whether the MN domain and the UV domains are expected to live in the same DNS zone (affects the
   DNS-consistency check and documentation).
