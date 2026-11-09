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

### M1.1 CLI, config, state

- Subcommands: `init`, `doctor`, `status`, `renew-site`, `renew-cert`, `warp`, `uninstall`, `version`.
- Strict YAML config (`schema_version: 1`), defaults, validation with actionable messages.
- Atomic state persistence, `0600`, schema-versioned.
- `uv init` is idempotent: on re-run it reads existing state and converges instead of regenerating.

### M1.2 Install engine

- Ordered, resumable steps with a journal (so a failed install can be retried): preflight → packages
  → users/dirs → xray binary (pinned, SHA-256 verified, **configurable download base**: GitHub release
  assets returned 504 from the lab network, so a mirror or a self-hosted copy must be selectable) →
  identity → certificate → decoy → xray+nginx config → units → start → self-test → summary.
- Preflight (`uv doctor`): root, Debian ≥ 13 (warn on 12), systemd, clock offset, free `:80`/`:443`,
  DNS A record equals the box's address, outbound TLS to the ACME directory, `/dev/net/tun` presence
  for WARP, disk and memory minimums.
- `uv uninstall --purge` removes what Termit created and nothing else; the manifest of created paths
  is part of the state.

### M1.3 Decoy site

- Generator interface (`Generate(spec) (fs.FS, error)`) plus deterministic content generation from
  `(domain, seed, template)`; the seed is persisted at install so the site never changes under the
  owner between restarts.
- Two archetypes (`DESCRIPTION.md` §5.3): a **branded enterprise portal** generator with randomised
  product name, vocabulary set, dark/light preset, accent color, footer company and link set — this
  is the family the reference generator uses, re-implemented in Go — and one **product clone**
  template, an original re-creation with the same impression, not copied assets.
- Anti-fingerprint requirements, each with a test: no external hosts in the output (fonts, scripts,
  images, preconnect hints), no generator-shaped ids (`f_<hex>`, `b_<hex>`, `csrf_<hex>`), no shared
  fixed submit path, layout variation across seeds, and a favicon derived from the same seed.
- Embedded HTTP server on a unix socket: static files, `robots.txt`, plausible headers and caching,
  PROXY-protocol client IP, and a submit endpoint whose path comes from the template. It answers
  `POST` with a random plausible `401` JSON (error code, message, request id), sets a cookie, and
  adds a short artificial delay.
- The tree is served by nginx (`root` + `index`), not by the agent: the generator writes files and the
  generated nginx config points at them. The auth endpoint is a `location` in that same config with a
  `return 401` body and cookie header, so the page behaves like a portal without an application
  server behind it.
- `uv renew-site [--template T] [--seed S]` regenerates the tree, rewrites the affected nginx
  location, validates with `nginx -t`, and reloads only nginx.
- Tests: determinism (same seed ⇒ byte-identical tree), no external URL in any output file,
  hand-written-looking ids, plausible content for every preset, and a render check against the
  captured samples in `captures/decoy-samples/` (manual/visual, not a pixel diff).

### M1.4 Certificate lifecycle

- Issue on init, renew on a schedule derived from `NotAfter` (e.g. at 2/3 of lifetime), retry with
  backoff, surface failures in `uv status`.
- Reload strategy: hot config path if Xray supports it, otherwise a restart at a jittered low-traffic
  moment, gated by `xray -test`.
- `uv renew-cert --force` for drills; it must work without touching the decoy.

### M1.5 Config generation, units, hot API

- Typed Xray config model, deterministic generation, golden files. Xray listens on `127.0.0.1:8443`
  with `security: none` and `network: xhttp`; it holds no certificate (see `SPIKES.md` S5).
- Generated nginx configuration: `:80` ACME webroot + redirect, `:443` TLS with the certificate,
  static decoy root, `location /<secret>/` proxying to `127.0.0.1:8443` (`proxy_http_version 1.1`,
  buffering off, long read/send timeouts, `client_max_body_size 0`), and the auth endpoint. Validated
  with `nginx -t` before reload, with rollback on failure.
- `xray -test` gate with rollback; hot add/remove of clients through the Xray API; validated restart
  as fallback.
- Profile constants centralized in `internal/proto/profile.go`; `BuildVLESSUrl`/`ParseVLESSUrl`
  round-trip tests. The generated URI must not contain `allowInsecure` (`SPIKES.md` S4).
- systemd units for `termit-uv` and Xray, hardened (`NoNewPrivileges`, `ProtectSystem=strict`,
  `PrivateTmp`, `RestrictAddressFamilies`, capability set), plus drop-in hardening for the stock nginx
  unit.
- Local user list (family devices), independent of the mesh.

### M1.6 Standalone acceptance

On a fresh lab UV machine: `uv init --domain uv1.lab.internal --acme-ca http://…/dir` →
`uv doctor` green → browser shows the decoy with the lab CA trusted → `curl` through a hand-built
`vless://` URI returns `204` → `systemctl reboot` → everything returns → `uv uninstall --purge`
leaves no listeners, no units, no files outside the manifest.

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

Still needed:

- Whether the ACME server for the lab is Pebble run on the MN container (preferred) or external.

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
