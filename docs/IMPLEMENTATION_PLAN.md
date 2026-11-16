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
| MN admin | unix-socket API + `termit-mn` CLI, no web UI in v1 | no second auth surface |
| Subscription | base64 `vless://` list at `/sub/{token}`, sing-box JSON at `/sub/{token}/json` | two renderers |
| Licence | AGPL-3.0, public repository | no GPL/AGPL dependencies |
| Target | Debian 13, amd64 + arm64 | packaging, checks |
| Acceptance | three Debian machines over SSH (1 MN, 2 UV) + LAN DNS | lab suite is the release gate |

Still open (see §9): the exact XHTTP mode/tuning and the lab coordinates. Resolved since the first draft: module path and repository URL,
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

### M0.5 Lab harness bootstrap — **done**

- SSH client wrapper with host-key pinning from an env var, command runner, file upload, and log
  capture. The transport is `plink`/`pscp`, not `golang.org/x/crypto/ssh`: the lab key is a PuTTY
  `.ppk`, OpenSSH cannot read it, and `puttygen` on the development host cannot convert it. A PPK
  parser would be more code than the suite is worth, and the guard that matters — pinning the machine
  — is the same either way.
- `test/lab` package (build tag `lab`) with `TestLabSmoke`: connect to all three machines, assert
  Debian 13, arch and memory, both units active on the UVs, the `termit-*` binaries under test, and
  `termit-mn uv list` through the socket.
- Provisioning helpers: `Machine.Run`, `Upload`, `WriteFile`, `Unit`, `WaitFor`, and `MNAdmin` (the
  admin token is passed in the remote environment and redacted from every captured output).
- `FromEnv` skips, not fails, when the environment is incomplete: the lab is a manual gate and a
  developer machine must still run the default `go test`.

**Done when**: `go test -tags=lab ./test/lab -run Smoke` passes against the three machines, and a
failed run prints enough to debug without logging in by hand. Both hold: the suite runs from the
Windows host with `TERMIT_LAB_PLINK`/`TERMIT_LAB_PSCP` pointing at PuTTY, and a failing scenario dumps
both units' journals and `termit-mn uv list` before restoring the lab.

---

## M1 — UV standalone (8–12 d)

Purpose: a fully working node with no MN involved. This is the first shippable artifact: a user can
install it, get a valid certificate, see a decoy site, and connect with a manually built
`vless://` URI.

### M1.1 CLI, config, state — **done**

- `termit-uv init`, `doctor`, `status`, `uninstall` and `version` are implemented. The rest of the command
  set was declared and exited `3` ("not implemented in this build") until it landed; every documented
  command is implemented now, so the reserved status is gone and the usage text no longer lists it.
- Configuration: strict YAML (`KnownFields`), `schema_version: 1`, defaults for every field, and one
  message listing *all* validation problems. It holds only what the owner decides — domain, ACME
  account, pinned core, decoy template, paths — never a generated value.
- State (`state.json`, `0600`): identity, decoy seed and template, installed core version and digest,
  certificate expiry, and the manifest of every path, user and unit Termit created. Schema-versioned.
- Journal (`install-journal.json`, `0600`, last 20 runs): one record per step, so `termit-uv status` can say
  where a run stopped and an unfinished run reads as "interrupted".
- Re-running `termit-uv init` is a no-op: every step reports `present` and `state.json` stays
  byte-identical (verified on the lab, same sha256 before and after).

### M1.2 Install engine — **done**

- Ordered steps, each with `Check` ("is my effect already present?") and `Run`: `preflight` → `user`
  → `dirs` → `packages` → `xray-binary` → `identity`. A failing step stops the run and is journaled;
  the next run resumes at the first unsatisfied step. `--only`/`--skip`/`--dry-run` exist for
  debugging and are covered by tests.
- Preflight: root, Debian ≥ 13 (12 warns), systemd, architecture, clock, ownership of :80/:443, DNS,
  ACME directory reachability, `/dev/net/tun` when WARP is wanted (it is by default; a missing device
  is a warning with the reason, not a failed install), free space (measured on the nearest existing
  ancestor, so it works before the first install), memory, apt.
- Core: a pinned release table with a per-architecture SHA-256 that is **verified before anything is
  extracted**; the archive is cached by digest, a mismatch is refused rather than installed, and the
  installed binary is checked by running `xray version` and hashing it into the state.
  `xray.download_base` accepts an HTTP(S) URL, a `file://` URL or an absolute directory — a mirror or
  an air-gapped copy — and `xray.sha256.<arch>` overrides the pin for a re-packed release.
- `termit-uv uninstall [--purge]` walks the manifest backwards and removes only what is in it. Without
  `--purge` it keeps `/etc/termit/uv` and `/var/lib/termit/uv`, so a reinstall keeps the identity;
  packages are never removed, and no other nginx site is touched. `--purge` also removes the two
  trees, their now-empty `termit` parents and the service user.

Measured on `termit-uv1` (Debian 13, 1 vCPU / 1 GiB), 2026-11-09:

| Run | Result |
|---|---|
| fresh `termit-uv init` | 2 m 28 s total, 2 m 21 s of it the 35 MiB core download through a mirror |
| second `termit-uv init` | 0.4 s, every step `present`, `state.json` byte-identical |
| `termit-uv uninstall --purge` | no files, no user, no units left; nginx and its default site untouched |

The certificate, decoy, generated Xray/nginx configuration, units and self-test belong to M1.4–M1.5;
until they land `termit-uv init` ends by naming them instead of pretending the node proxies traffic.

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
- `termit-uv renew-site [--template T] [--seed S] [--dry-run]` regenerates the tree and the fragment,
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
| Drill | `termit-uv renew-cert [--force]` | without `--force` it is the same check `termit-uv init` makes; with it the certificate is replaced regardless |
| Reporting | `termit-uv status` | `certificate: expires … (in N days)`, and a failed run shows up as `last run: failed` with the failing step |

Measured on `termit-uv1` against Pebble:

| Check | Result |
|---|---|
| `termit-uv init` with `--acme-ca …/dir` | `certificate ok`; `fullchain.pem` `0644`, `privkey.pem` `0600`, `account.key` `0600` |
| Issued certificate | `issuer=CN=Pebble Intermediate CA …`, 90 days |
| Vhost after issuance | flipped from plain HTTP to TLS in the same run (`web` ran again inside the certificate step) |
| `GET https://…/` | `200` over HTTP/2; `:80` → `301` |
| `vless://` through the issued certificate | `200` |
| `termit-uv renew-cert` (not due) | `certificate present, valid until …`, exit 0 |
| `termit-uv renew-cert --force` | new serial, node kept serving, challenge files removed from the webroot |
| Renewal against a dead CA | fails loudly, `last run: failed` + `certificate failed` in `termit-uv status`, **the certificate and the site stay untouched**, and the next run succeeds once the CA is back |
| `termit-uv init` afterwards | 1 ok, 11 present, nothing pending |

One finding, recorded in `SPIKES.md` S11: `--force` initially changed nothing. The engine's `Force`
skips the step's `Check`, but the step itself called `acme.Ensure`, which renews only inside the
window — so the flag had no effect and the serial stayed the same. The step now reads
`stepEnv.forced` and calls `acme.Issue` in that case. A forced run must mean *do it again*, not
*skip the check*.

Deliberately different from the plan: the timer, not the agent, owns the renewal clock. There is no
agent loop yet (M3), and a daily oneshot is easier to inspect, survives a reboot via `Persistent` and
needs no backoff logic of its own — a failure is retried tomorrow and visible in `termit-uv status` today.

### M1.5 Config generation, units, hot API — **done**

Built:

| Piece | Where | Notes |
|---|---|---|
| Typed Xray config + generator | `internal/uv/xray` | VLESS inbound on `127.0.0.1:8443`, `security: none`, `network: xhttp`, `mode: packet-up`; loopback management API on `10085`; golden file, deterministic bytes |
| Profile and client URI | `internal/proto/profile.go` | `BuildVLESSUrl`/`ParseVLESSUrl`, round-trip test, refuses `allowInsecure` and any transport that is not `xhttp`+`tls` |
| nginx vhost | `internal/uv/web` | two shapes from one generator: plain HTTP before a certificate exists, TLS afterwards (`http2 on;`, HSTS, `/assets/` cache, ACME location on `:80`, `location /<secret>/` → Xray) |
| Units | `internal/uv/service` | `termit-xray.service` runs as `termit`, `ProtectSystem=strict`, empty capability set, `SystemCallFilter=@system-service`, `MemoryDenyWriteExecute`; nginx gets a small drop-in |
| Install steps | `internal/uv/install` | `xray-config` → `web` → `units` → `start`, each converging and journaled |
| Owner's devices | `termit-uv device list|add|remove` | local users in the state file, never shared with the mesh |

Measured on `termit-uv1` (1 vCPU / 1 GB, Debian 13):

| Check | Result |
|---|---|
| `termit-uv init` on an installed node | 1 ok (`preflight`), 9 present, `not yet installed: certificate` |
| `systemctl is-active termit-xray nginx` | active, active |
| Loopback listeners | `127.0.0.1:8443` (XHTTP), `127.0.0.1:10085` (API), `:80` (nginx) — Xray has no public port |
| `GET /` | `200`, the generated page, `5665 B` |
| `POST <form path>` | `401` + `Set-Cookie` + `X-Request-Id`, no app server behind it |
| `GET <form path>` / `GET /nope` | `405` / generated `404` |
| `GET /.well-known/acme-challenge/<token>` | `200` with the token written into the webroot |
| TLS vhost with a temporary self-signed pair | `200` over HTTP/2, `:80` → `301` to `https`, POST → `401` |
| `vless://` client through nginx (`https://example.com`, `https://example.org`) | `200`, `200` — client → nginx over h2, nginx → Xray over HTTP/1.1 |
| `termit-uv device add --label phone` | config rewritten, core restarted, `xray api inbounduser` shows `local-<id>@termit`; `remove` drops it again |

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
  include is a glob) and `termit-uv uninstall` puts it back. Without this a request to the bare address
  answered Debian's welcome page — a louder fingerprint than the decoy is quiet.
- **No `termit-uv.service` yet**: the agent loop it would run is M3. Only the core and nginx are
  supervised so far.


### M1.6 Standalone acceptance — **done**

Run on `termit-uv1` as a full cycle: clean host → install → verify → reboot → verify → uninstall →
audit → install again.

| Step | Result |
|---|---|
| Fresh `termit-uv init` (Pebble, new empty host) | 11 ok, 1 present (`packages`); the certificate is issued in the same run |
| `termit-uv doctor` | 16 ok, 1 warn (the host sees its own name as `127.0.1.1`), 1 skip (no `/dev/net/tun`), exit 0 |
| Decoy, certificate verified against the lab root | `200` over HTTP/2 |
| `POST` the form | `401` with `Set-Cookie` and `X-Request-Id` |
| `termit-uv uri` → a client built from that URI | `https://example.com` through the tunnel: `200` |
| `systemctl reboot` | all three units `active` + `enabled`, no stale listeners, site `200`, certificate serial unchanged, tunnel `200`, `termit-uv doctor` green |
| `termit-uv uninstall --purge` | `/etc/termit`, `/var/lib/termit`, `/usr/lib/termit`, `/var/www/termit` gone; units gone; user gone; `8443`/`10085` closed; the stock vhost back in `sites-enabled` and answering `200`; nginx and `ca-certificates` still installed |
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
4. **`termit-uv doctor` had no node half at all**, so "doctor green" would have meant "the host could be
   installed", not "the node works". It now also checks the state, the three units, the certificate
   window (fail when expired, warn under two weeks), the decoy against its seed, and that the core
   accepts its generated config.

Also added for the acceptance and for ordinary use: `termit-uv uri`, which prints the `vless://` URI for the
node\'s own device. Without it a standalone node had no way to hand its owner a working client.

---

## M2 — MN core (8–10 d)

Purpose: a management server that accepts nodes, verifies them, and serves subscriptions. Still no
daily loop: sync exists but the UV drives it manually (`termit-uv sync`).

### M2.1 Store and bootstrap — **done**

Done:

| Piece | Where | Notes |
|---|---|---|
| Atomic writes | `internal/atomicfile` | moved out of the UV state package; both binaries write config a running service reads |
| Shared ACME | `internal/acme` | was `internal/uv/acme`; both sides issue certificates |
| Database | `internal/mn/store` | pure-Go SQLite, schema version in `PRAGMA user_version`, migration in one transaction, a newer schema refused, `VACUUM INTO` for backups, the seven tables from `DESCRIPTION.md` §14 |
| Credentials | `internal/mn/secret` | Argon2id with a per-value salt, readable invite codes, constant-time comparison |
| Configuration | `internal/mn/config` | strict YAML, one schema version, defaults, validation that collects every problem |
| `termit-mn init` | `cmd/mn/init.go` | config, master key (`0600`, `TERMIT_MN_MASTER` override, never replaced), database, first admin token, first invite; re-running keeps what exists |
| `termit-mn serve` | `cmd/mn/serve.go` | `/healthz`, loopback-only `/metrics`, graceful shutdown, TLS when a certificate exists |

Measured on `termit-mn`: `config.yaml` `0644`, `master.key` `0600`, `termit.db` `0600`, schema 1; a
second `termit-mn init` reported the existing key instead of replacing it; `/healthz` answered with the build
identity and the schema version; SIGTERM shut the server down inside the grace period.

Still to do in this milestone:

- ACME issuance for the server's own certificate (the challenge listener on `:80`), so `termit-mn serve` is
  TLS by default rather than by luck.
- `termit-mn backup` / `termit-mn restore` on top of `store.Backup`, with a schema-version guard on restore.

### M2.2 Enrollment — **done**

Done:

| Piece | Where | Notes |
|---|---|---|
| Wire types | `internal/proto/api.go` | enroll request/response, the problem body, stable codes |
| Enrollment | `internal/mn/enroll` | invite validated but not consumed → URL parsed → domain resolves to the request address → VLESS probe → invite and node written in **one transaction**; a refusal burns nothing |
| Resolvers | `internal/mn/dns` | system resolver plus an optional second one; a resolver that cannot answer is not evidence against a node |
| Prober wiring | `cmd/mn/prober.go` | xray-core worker; with no binary configured every check is `unknown`, which never blocks a node |
| HTTP | `internal/mn/server` | `/api/v1/enroll`, `/healthz`, loopback `/metrics`, per-address rate limit, RFC 9457 problems |
| Modes | `enrollment.mode` | `invite` → active; `invite+approval` → pending with a notice; `open` → active without an invite |

Two decisions worth recording:

- **Credentials are stored as SHA-256 digests, not Argon2id hashes** (see `SPIKES.md` S14). Every
  one of them is looked up by value on each request; a salted password hash cannot be looked up at
  all. The values are 160–256 bits of randomness, so there is nothing to brute-force.
- **Enrollment probes with the self credential**, because the probe credential does not exist yet at
  that point: MN issues it in the same response and the node starts accepting it at its first sync.

Also done since: the admin CLI (`termit-mn invite|uv`) over a `0600` unix socket; the certificate check with a
14-day minimum validity; and the certificate pin the probe uses instead of a CA.

Lab evidence (termit-mn + termit-uv1, Pebble as the CA):

| Step | Result |
|---|---|
| `termit-mn invite create --uses 2` | code issued, stored as a digest |
| `termit-mn invite list` | `inv_T5bh68a-FS  0/2  usable` |
| `POST /api/v1/enroll` from uv1 | `201`, node `uv_tsFkPtgUuYn6`, state `active` |
| credentials in the response | `agent_key`, `sub_token`, `probe_uuid` — all present |
| `termit-mn uv list` | node with its domain, state and source address `192.0.2.38` (documentation range) |
| `termit-mn invite list` afterwards | `1/2` — the invite was consumed exactly once |

Two dead ends found on the way, both recorded in `SPIKES.md` S15: xray ignores `SSL_CERT_FILE` and the
host trust bundle for its XHTTP TLS, so trusting a lab CA through the system store does not work; and
the prober was sending a cleartext GET into port 443, which the target answered with `400` — a probe
bug that looked exactly like a broken node.

Still to do in this milestone:

- The second node (termit-uv2 has no install yet).
- `SPIKES.md` S15 write-up of the two probe defects.

### M2.3 Prober — **done**

Done: the xray-core worker, the certificate checker with a 14-day minimum validity and a pinned leaf, the
two-resolver DNS check (system plus an optional second one, `prober.ca_file` for a private CA), the
result model with stable classes, and the circuit breaker: three consecutive worker failures open it for
five minutes, during which every target answers `worker_unavailable` without starting the core.

Decided: the decoy is proven by the TLS + tunnel checks rather than a separate HTML assertion — a node
that answers TLS and carries a VLESS connection is serving the web tier, and a byte-level HTML check
would break whenever the template changes. The scheduler that consumes the classes landed in M3.1.

- `probe.Prober` interface; xray-core worker implementation from M0.4; cert/decoy checker; DNS
  checker with two resolvers (`system`, DoH) and `extra_roots` for the lab.
- Result model: `ok | failed | unknown` plus `error_class` (`timeout`, `tls_invalid`, `http_error`,
  `tunnel_closed`, `dns_mismatch`, `worker_unavailable`, …).
- Per-node concurrency limits, global worker pool, circuit breaker: if the worker is unhealthy, all
  results become `unknown` and the scheduler is told not to act (invariant 2).

### M2.4 Subscription and credentials — **done**

Done:

| Piece | Where | Notes |
|---|---|---|
| Renderers | `internal/mn/sub` | one ordered peer set, two formats; ordering (label, then id) is part of the contract because the body is what the ETag validates |
| `GET /sub/{token}` | `internal/mn/server/subscription.go` | base64 list; a degraded peer stays in the list and says so; `304` on a matching `If-None-Match` |
| `GET /sub/{token}/json` | same | sing-box profile: one vless outbound per peer, a selector, a direct outbound, and rules that keep private ranges off the mesh |
| Tokens | `termit-mn sub rotate` | new token returned once, old one stops working at once, rotation is audited |
| Storage | `enroll` | the record holds the parsed profile (domain, credential, path); a node enrolled before this change must re-enroll |
| Tests | `internal/mn/sub` | ordering, stability under reordering, degraded marking, empty set, JSON shape, ETag changes |

Acceptance on the lab — two nodes, one subscription, traffic through each URI:

| Step | Result |
|---|---|
| `termit-uv init` on termit-uv2 | 12 steps ok, certificate issued by Pebble, identity `ac7af400` |
| enroll uv2 | `201`, node `uv_R055z4RzZHki`, state `active` |
| `termit-mn uv list` | two nodes, both `active` |
| `GET /sub/{token}` | two `vless://` lines, one per node |
| request through the uv1 URI | `HTTP 200` from `example.com` |
| request through the uv2 URI | `HTTP 200` from `example.com` |

The acceptance client reads nothing but the decoded subscription: it parses the URI, reads the node's
leaf for the pin, and speaks the profile the URI describes. The first attempt showed uv1 timing out and
the immediate retry passing twice — that was the script's own 1.5 s startup window, not the node.

The single-node evidence, for the record: the list for `termit-uv1.lan` decoded to

```
vless://39dadfb7-…@termit-uv1.lan:443?encryption=none&mode=packet-up&path=%2Fcc522787…%2F&security=tls&sni=termit-uv1.lan&type=xhttp#termit-uv1.lan
```

and the JSON format listed `termit-uv1.lan` (vless), `proxy` (selector) and `direct`. A wrong token
answered `404`; a conditional fetch answered `304`.

Still to do in this milestone:

- `internal/cred`: pair-credential derivation with vectors and property tests. It is not needed for the
  acceptance above — a subscription carries self credentials — but M2.5 and the mesh need it.
- Revocation as an explicit operation (today `sub rotate` supersedes a token rather than revoking it).

### M2.5 Sync v0 — **done**

Done:

| Piece | Where | Notes |
|---|---|---|
| Derivation | `internal/cred` | HMAC over master key, epoch and both ids; order independent; two vectors frozen in the tests |
| `POST /api/v1/agent/sync` | `internal/mn/server/sync.go` | bearer agent key; the complete peer set, never a delta; each peer carries the pair credential for that relationship; a node's own entry is left out |
| Policy hints | response | `check_interval`, `sync_interval`, `min_sync_delay`, `next_sync_after` |
| Tests | `internal/mn/server/sync_test.go` | pair credential vs self credential, degraded marking, byte-identical repeats, unknown key, removed node, unknown field |
| `termit-mn uv reissue` | admin socket | a lost agent key can be replaced; the old one stops at once |

Lab: uv1's sync returned uv2 with pair `9821a6ed-3746-47b3-a5d2-149af557bf2f`; uv2's sync returned the
same value for uv1 — the property the derivation exists for. A wrong key answered `401`.

Also done since:

- `termit-uv sync` (`internal/uv/agent`): fetch, generate, `xray -test`, install, restart, roll back on a
  refused restart. Client order is sorted, so an unchanged set touches nothing.
- `subscription_url` is no longer empty. The token is stored twice — a digest that answers lookups and
  an AES-GCM copy sealed under the master key — so MN can print the URL again without the database
  alone revealing it. Schema v2 with a migration map carries existing databases forward.

Lab evidence for this milestone:

| Step | Result |
|---|---|
| `termit-uv sync` on uv1 and uv2 | `applied: 3 client(s), 1 peer(s)`; second run `already in sync` |
| pair credential `9821a6ed-…` | the same on both sides; a client with it reached `example.com` through uv2, HTTP 200 |
| `sync` after rotating a token | `subscription_url` equals the URL the rotation printed |
| live schema | `PRAGMA user_version` = 2, `sub_token_sealed` present |

Two defects found and fixed by these runs: the applier wrote the core's config as `root:root`, which
stopped both nodes (a failed unit also hits systemd's start rate limiter, so a plain restart was not
enough); and `openssl verify` without `-untrusted` misreports a valid chain.

Acceptance item deliberately left to M3: "pulling a node's plug makes its probe fail" is the
scheduler's job (§7.2), not this milestone's.

### M2 follow-ups, done straight after

| Piece | Result |
|---|---|
| `termit-uv enroll` | registers the node, stores the credentials in the state file and takes the first sync; refuses a second enrollment without `--force` |
| Sealed agent key (schema v3) | `termit-mn uv agentkey` reads the key back without rotating it; the row holds a digest and an `aead:` blob |
| `termit-uv sync --check` | exit 0 in sync, 4 on drift, 1 when the server is unreachable; writes nothing |
| Retention | checks 90 days, audit 1 year, pruned at startup and daily in `termit-mn serve` |

Lab: reissue then agentkey returned the same key; `sync --check` said `in sync` (0), then `drift: 1 to
add` (4) after a peer credential was removed from the core config, then `0` again after `termit-uv sync`.

---

## M3 — Full lifecycle (8–12 d)

Purpose: the three scenarios from the concept, end to end, verified in the lab.

### M3.1 Scheduler and state machine (MN) — **done**

- Queue keyed by `next_check_at` with jitter; worker pool; per-node serialization.
- Retry policy inside a cycle (3 × 10 min, configurable); cycle outcome aggregation (`ok`, `failed`,
  `unknown`).
- Pure transition table + side effects (peer invalidation, subscription ETag bump, audit event).
- `termit-mn check now`, `termit-mn policy set`, `termit-mn uv block|unblock|rm` (admin override, audited).
- Retention job (checks 90 d, audit 1 y).

Done so far:

| Piece | State |
|---|---|
| Transition table, pure, full cross-product under test | done (`internal/mn/sched`) |
| `termit-mn uv block` / `unblock` / `rm` through the same table, audited | done |
| `termit-mn uv forget` — frees a removed node's domain | done |
| Retention (checks 90 d, audit 1 y, at start and daily) | done |
| Queue, cycle runner with in-cycle retries, aggregator, jitter, `termit-mn uv check now` | done |
| Certificate pin stored at enrollment (schema v5) — without it every scheduled check fails | done |
| `termit-mn uv show` prints the last probe | done |
| `termit-mn policy` show and set, per-node serialisation within a pass | done |
| `termit-mn check now` for the whole mesh; `termit-mn status` dashboard (counts, last/next check) | done |

The table is the contract M3.2 and the scenario milestones build on: `active → unavailable` on the
first failed cycle, `unavailable → blocked` on the second, `blocked → active` only through an explicit
unblock, and `unknown` never moves anything.

Lab: block, block again ("nothing changed", one event row), unblock, rm, approve-after-rm refused with
`conflict`; a sync attempt after removal answered `node_inactive`; `termit-uv forget` freed the domain and uv2
re-enrolled as a new node.

Scheduler on the lab: both nodes `check_ok` and active, an automatic pass ran on its own, recorded both
checks and scheduled the next ones a day out with a per-node spread. Getting there cost two defects: a
failing cycle takes thirty minutes when retries are on (so an operator watching the table sees
"unchanged" for half an hour — hence `check now`, one attempt, 0.2 s), and the scheduled probe verified
the node certificate against xray's embedded roots instead of the pin enrollment used, which failed
every check with `x509: certificate signed by unknown authority` — filed under `tunnel_closed`, because
that was the classifier's fallback for everything it did not recognise. Unknown errors now have their
own class and count as unknown, so our own misconfiguration can never block a node.

### M3.2 Agent loop (UV)

- Long-running scheduler: daily jittered cycle, never before `registered_at + 2 h`, honoring
  `next_sync_after`; exponential backoff when MN is unreachable (traffic off the box keeps working).
- Local self-check included in the sync report (ports, xray process, cert expiry, WARP state) so MN
  can distinguish local breakage from path breakage.
- Reconcile: compute desired vs actual client set, apply via hot API, rewrite the config file,
  verify with a canary connection, report the applied state and `xray -test` result.
- `termit-uv unblock`, `termit-uv migrate --relocate`, `termit-uv status --json`.

Done so far:

| Piece | State |
|---|---|
| Loop: state lock, daily jittered cycle from the server's schedule, 5 min → 1 h backoff, `agent` unit with `-v` | done (`internal/uv/agent`) |
| Self-check: `xray`, `proxy`, `api`, `web`, `certificate`, `warp`; `unknown` is reported, never a failure | done |
| Reconcile: desired vs file vs the core's own client answer, hot API first, validated restart fallback, canary, rollback | done |
| `termit-uv agent [--once]`, `termit-uv status --json` (schedule, self-check, peers, enrollment), `termit-uv unblock` | done |
| `termit-uv migrate --relocate` | deferred to M3.5: it belongs to Scenario C, where the endpoint actually changes |

The reconcile order is the contract: the file is compared with the desired set *and* the running core
is asked what it serves, because an API update or a hand edit can leave the core different from a
correct file. Only the client set may go through the API; a path, port or stream change is always a
validated restart, and the canary (`127.0.0.1:80` through the local tunnel, `Host` = the node domain)
runs after either path and before the result is reported. `termit-uv sync --check` reads the live set too and
exits 4 on drift.

Lab: the agent ran on both UVs, reported seven self-check items, wrote `next_sync_at` a day out with
per-node jitter, and `termit-mn uv show` printed the self-check. A device added to the state landed through
the hot API with the core's `MainPID` unchanged; the same held for removing a peer after `termit-mn uv
block`, and re-adding it after `termit-uv unblock` (the MN verified DNS and the decoy and moved the node back
to `active`). Two defects surfaced: the desired-set map is `id → email` and the reconciler initially
read it as `email → id`, so every hot verification failed and every change fell back to a restart;
and the agent unit's `ProtectHome=yes` hides `/root`, so a CA file there was unreadable and the unit
now drops `ProtectHome` when a configured readable path lives under a home directory.

### M3.3 Scenario A acceptance — **done**

Enroll two UVs → wait for one cycle (or `termit-mn check now`) → both nodes present in each other's Xray
config and subscription → kill node 2 → cycle fails after retries → `unavailable`, still in node 1's
config → second failed cycle → `blocked`, removed from node 1's config at its next sync → restart
node 2 → stays blocked until an explicit unblock → `termit-uv unblock` → `active`, peers re-add it.

Done so far:

| Piece | State |
|---|---|
| Scenario A as one lab test (`test/lab/scenario_a_test.go`), driven through the M0.5 harness | done |
| Subscription URL learned from every sync, not only from enrollment | done |

The test asserts the server's view, the core's **live** client set (not the file) and the subscription
body at each step, and it restores the lab on every exit path. The subscription check is the reason a
small product gap surfaced: a node enrolled before the field existed never learned its subscription
URL, because only enrollment stored it. Every sync now stores what the server sends, so `termit-uv status`
works on an old node too.

Lab, both runs identical: the two nodes listed each other's `peer-…@termit` credential and each
other's domain in the sing-box subscription; killing uv2's core produced `check_failed → unavailable`
while uv1 kept the credential; the next cycle produced `blocked`, and uv1's sync dropped the peer
through the hot API (`canary=true`); a restarted, healthy uv2 stayed `blocked` through two passing
checks, its sync was refused with `node_inactive` and its config hash was unchanged; `termit-uv unblock`
passed DNS/decoy/VLESS verification and moved it to `active`; uv1 re-added the peer by hot API. The
whole scenario takes ~18 s, which is what makes it usable as a regression gate.

One flake is recorded rather than hidden: the first `termit-uv unblock` immediately after a cold start of the
core can hit a single failed probe (`check_failed: EOF`) even though the previous check passed, because
a fresh listener answers before its handlers settle. The test now requires the node to pass two checks
in a row before unblocking; the product path is unchanged and the retry is not automatic.

The subscription check also found a real defect the manual transcripts had missed: the endpoint shared
the enrollment limiter (10 requests / 15 minutes) keyed per token, so the fourth fetch in one scenario
run answered `429`. A subscription token is unguessable and clients refresh on every launch, so the
subscription counter is load control, not brute-force defence: it is now a separate limiter at 60 per
15 minutes, with a boundary test (60 pass, 61 is refused).

### M3.4 Scenario B acceptance — **done**

From a rebuilt machine (new `self_uuid`, same agent key), `termit-uv init --relocate` followed by
`termit-uv unblock` (the box builds the new URL from its own identity) → MN verifies DNS/decoy/VLESS →
`active`; peers converge; the subscription contains the new endpoint. A blocked node that merely comes
back healthy stays blocked until this explicit request.

Done so far:

| Piece | State |
|---|---|
| `termit-uv init --relocate --agent-key-file` — fresh install, new identity, saved key, no enrollment | done |
| `termit-uv unblock` builds the node's own URI from the local identity when `--vless-url` is absent | done |
| Scenario B as a lab test (`test/lab/scenario_b_test.go`) | done |

The key file has to be `0600`; the check is part of the command, not a convention. Passing the URL on
the command line would have put a credential into the shell history and the remote process list, so the
default is to build it locally — this is also what makes a rebuilt box work without the operator
having to copy anything but the key.

Lab, repeatable: uv2 blocked, its agent key saved to a 0600 file and its config to a copy,
`termit-uv uninstall --purge`, restore the config, `termit-uv init --relocate --agent-key-file` → new identity
(different fingerprint), `enrolled: true`, server still `blocked`, still two nodes; `termit-uv unblock` →
`active` (DNS + decoy + VLESS against the new UUID/path), and its own sync restores the probe
credential and the peer. uv1's subscription then carries the rebuilt path and not the old one, while
the `peer-<id>` credential and the node id are unchanged. Whole scenario ~15 s.

The test also proves the negative: after the rebuild and before the unblock, the server has not been
touched (blocked, same node count). Cleanup removes the key file, restarts the units, unblocks and
syncs; if the rebuild died half-way it finishes the init from the saved key and config.

One lab-only obstacle was also a product wart: a rebuild restarts the agent several times, and the
fifth start inside five minutes tripped systemd's start limit, leaving the unit failed. `termit-uv init` now
clears the unit's failed state before restarting the agent (a re-runnable command must not break its
own service), and the agent's burst is ten instead of five — a real crash loop still stops after ten
attempts with `RestartSec=30s`.

### M3.5 Scenario C acceptance — **done**

The box moves to a different domain, keeps its agent key, and the server's record follows it. The
command is split the same way as Scenario B: the install is `termit-uv init --relocate` with the new domain,
and the network action is `termit-uv migrate --relocate`, which posts the endpoint the box now serves.

Done so far:

| Piece | State |
|---|---|
| `POST /api/v1/agent/relocate` — verify the claimed endpoint from the requesting address, then swap domain, self UUID, profile and pin together | done |
| `termit-uv migrate --relocate` — builds the local URL, refuses a box whose config names another domain | done |
| `EventRelocate` in the state table: an active node keeps its state, a blocked or unavailable one is re-admitted by a verified move | done |
| Subscriptions carry the **pair** credential for a peer, not its self credential | fixed |
| Scenario C as a lab test (`test/lab/scenario_c_test.go`) | done |

The subscription fix was found by this scenario and is the part that makes the promise true. The
subscription handler served `profile.uuid` for every node, i.e. each peer's **self** credential, while
DESCRIPTION §6.4 says a peer appears with `pair_uuid(A,P)` and §6.5 says an owner may see only the pairs
their node is part of. A leaked subscription therefore exposed the owner credential of every peer, and a
rebuild changed the UUID in every list. It now derives `cred.Pair(master, owner, peer, owner.epoch)` —
the same value the peer's inbound was given at sync — so a move changes the host and path and leaves the
credential alone.

Lab, repeatable: uv2 blocked, key and config saved, the new name taught to the server's and the node's
resolvers (the node's own core resolves the Host its canary sends, and in the lab nothing resolves that
name yet), `termit-uv uninstall --purge`, install on the new domain with `--relocate`, then
`termit-uv migrate --relocate` → `active`, record on the new domain, uv1's subscription on the new domain and
path with the **same** pair UUID, both peers still accepted, `probe@termit` back after the first sync.
The test also proves the negative: before the migrate the record still names the old domain and the
node count is unchanged. Cleanup puts the box back on the name it started with (reinstall from the saved
config, then migrate the record back) and removes the hosts entries the test added.

The same run also covers the mesh with three nodes (`test/lab/mesh_test.go`, skipped when
`TERMIT_LAB_UV3` is unset). That node is not needed by M3.6's single-node drills, but two nodes cannot
express the invariants §6.3–§6.5 state: each node must serve exactly its two peers, the three pair
credentials must be pairwise distinct and symmetric (`uuid(A→P) == uuid(P→A)`), a blocked node leaves
the surviving pair intact while its own read-only list keeps both peers, and a move of one node leaves
the pair between the other two untouched. With `TERMIT_LAB_UV3` set, Scenario C adds that last check
against the node that stayed.

Two lab lessons: a relocate verification cannot use the stored probe credential (a blocked node has no
probe user until it syncs, so the checks run against the claimed self credential, exactly like unblock);
and the canary sends the node's own domain as the HTTP Host, which the core resolves itself, so a node
that cannot resolve its own name fails every sync — the preflight warns about it, and the test fixes it
with a hosts entry.

### M3.6 Failure-drill suite — **done**

Six scripted drills in `test/lab/drill_test.go`, each breaking one thing, asserting the invariant that
breakage protects, and putting the lab back:

| Drill | What it does | What it proves |
|---|---|---|
| MN unavailable | stops the server, restarts it | the nodes keep serving: their units stay active, the certificate is still served, and one sync converges them — without an operator |
| Prober broken | moves the prober binary away | two checks are `unknown` with the node still active: our broken tool is not evidence against the node |
| DNS flipped | points the name at a black hole | a genuine negative walks unavailable → blocked, the surviving pair's credential is untouched, and the restored name plus an explicit unblock is the only way back |
| Certificate replaced | serves an expired leaf signed by the lab CA | the failure is visible from outside (`cert_expired`, unavailable → blocked) while the node's own diagnosis still sees its services and decoy as healthy |
| SQLite lock | holds `BEGIN EXCLUSIVE` for eight seconds | the server answers instead of hanging or dying, and the registry is intact once the lock is gone |
| Disk full | fills the filesystem with `fallocate` + `dd` | a write reports the failure, reads keep working, and freeing the space recovers the server with no repair step |

Two defects the drills found, both now fixed:

1. **An expired certificate passed the check.** The tunnel is pinned to a leaf, and this build of xray
   does not check the pinned leaf's validity window, so a node whose certificate had expired was
   reported healthy while every client was being rejected. The probe now reads the certificate in its
   own process first (chain, hostname, validity) and only then opens the tunnel; expiry is a verdict
   (`cert_expired`), and a chain or hostname failure stays `unknown` because it can also mean the
   checker trusts the wrong root.
2. **A certificate renewal looked like a dead node.** The pin is taken at enrollment, so a renewed
   leaf failed the pin check, the tunnel closed, and the node was blocked after two cycles — for
   doing the right thing. Now the leaf a node serves is a mismatch the prober reports as
   `cert_changed` (unknown, never a verdict), and every sync carries the leaf's fingerprint
   (`X-Territ-Cert-SHA256`), so the pin follows a renewal within one cycle. Choosing a header rather
   than a body field keeps an older server from rejecting the body.

Lab limits, recorded rather than papered over: the day-long MN outage is the same code path as the
short one and is not replayed at length; clock skew cannot be exercised in an unprivileged LXC (no
`CAP_SYS_TIME`), so `TestDrillClockSkew` skips with that reason instead of passing on a check it did
not perform.

---

## M4 — Hardening and release (6–8 d)

### M4.1 WARP — done

Maintainer decision on 2026-11-10: `warp-cli` in proxy mode as designed (not userspace WireGuard),
and **WARP on by default**, opt-out. The reason is in the design: a client-side application that asks
"what address am I coming from?" must not be told the node's own.

Delivered:

| Piece | Where | Verified by |
|---|---|---|
| `warp.mode: off\|all\|selected` (default `all`), `warp.fail_open` (default `true`), `warp.selected`; the old `warp.enabled` migrates and is never written again | `internal/uv/config` | unit tests (default, migration both ways, selected normalisation, invalid modes) |
| WARP outbound (SOCKS5 to `127.0.0.1:40000`), catch-all/domain rule, loopback stays direct, `observatory` + `balancer` with `fallbackTag: direct` for fail-open, no balancer for fail-closed | `internal/uv/xray` | unit tests for all four shapes; `xray -test` on the lab accepts the generated file |
| `warp-cli` package install from Cloudflare's signed apt repo, registration, proxy mode, connect, wait; `status`/`trace` through a hand-written SOCKS5 client | `internal/uv/warp` | unit tests with a fake runner and a fake SOCKS5 server (name forwarded unresolved, refusal reported) |
| Install step between `packages` and `xray`; refuses an unsupported host with a reason and still reports a converged node | `internal/uv/install` | unit tests; lab: `termit-uv init`-time refusal path |
| `termit-uv warp status\|mode`, `termit-uv init --warp/--no-warp/--warp-selected`, `termit-uv status` line, `termit-uv doctor` check, uninstall disconnects the tunnel | `cmd/uv`, `internal/uv/install` | unit tests; lab |
| Self-check reports off / unavailable-with-reason / healthy / failing (fail-open says "traffic exits directly") | `internal/uv/agent` | unit tests for all five outcomes |

The lab cannot run WARP itself (unprivileged LXC, no `/dev/net/tun`), so `test/lab/warp_test.go`
proves what surrounds the tunnel, end to end: the migration to a mode, the refusal with its reason,
the generated route accepted by the node's own core, **fail-open with a dead sidecar** (a real Xray
client fetches `https://1.1.1.1/cdn-cgi/trace` through the node and gets an answer with `warp=off`
while nothing listens on port 40000), the server still seeing the node as healthy, status and doctor
telling the truth, and the switch back removing the route without interrupting traffic. Registration
and the connected tunnel remain on the release checklist for a host with TUN.

Two defects the lab found and the fix:

- `termit-uv warp mode` restarted the core twice (the `xray-config` step already restarts it after `xray
  -test`), which tripped systemd's start limit; the command now leaves the restart to the step.
- The running agent holds the configuration it read at start, so its next sync would have regenerated
  the old route; the command now restarts `termit-uv-agent` after the change.

### M4.2 Operations surface — done

Delivered:

| Piece | Where | Verified by |
|---|---|---|
| Operator documents: install/upgrade/backup/recover/uninstall/troubleshooting, the wire contract, the threat model | `docs/OPERATIONS.md`, `docs/PROTOCOL.md`, `docs/THREAT_MODEL.md` | written against the code; endpoint examples checked against a running server on the lab |
| A systemd unit for the management server (there was none) | `deploy/systemd/termit-mn.service` | started on the lab under systemd; `healthz`, `termit-mn status` and `/metrics` answer |
| Bootstrap installer with checksum verification and an upgrade path that keeps the node's identity | `deploy/install/uv.sh` | lab: a local release directory installs, a corrupted `SHA256SUMS` is refused before anything is written, a missing one is refused too, a re-run upgrades `termit-uv` and `termit-uv init` converges |
| Real Prometheus metrics: enrollments, syncs, check outcomes, a check-duration histogram, nodes by state, scheduler backlog, probe-worker health, server certificate expiry; loopback only, no node identifiers in any label | `internal/mn/metrics`, `internal/mn/server` | unit tests for buckets, labels, escaping, the 403 and the counters; scraped from the live lab server |
| The dashboard's server block: uptime, database size and schema, prober state with its reason, queue backlog and age, certificate expiry | `admin server.state`, `termit-mn status` | unit tests; live lab output |
| Per-node history in one series: the last ten probes and self-reports together | `admin uv.show` → `termit-mn uv show` | live lab output |
| A redaction test: enrolment and sync log identifiers, never the agent key, subscription token, probe UUID, invite code or node UUID | `internal/mn/server` | the test fails if any of them appears |
| In-place certificate renewal on the server: it re-checks every six hours, writes through the CA's chain and serves the new pair on the next handshake — no restart, and a failed attempt keeps the old certificate in service | `internal/mn/cert` (loader + loop), `termit-mn serve` | unit tests for the reload, the broken-pair case and the loop; lab: a certificate swapped under a running server was served immediately, swapped back, and the real chain still validated |

Also outstanding, and a maintainer decision rather than a defect fix: `DESCRIPTION.md` §8 shows a sync
body shape the server does not send, §15 says agent keys are hashed with Argon2id and compared in
constant time (they are SHA-256 digests looked up through an index, deliberately), and §13.4 lists
`termit-mn sub show`, which does not exist. `docs/PROTOCOL.md` is what the build does; the design document
should be reconciled with it before the first tag.

### M4.3 Release — done

| Task | Where | Verified |
|---|---|---|
| A release tool, `go run ./deploy/release --version v0.1.0`, that cross-compiles both binaries for linux/amd64 and linux/arm64 (`CGO_ENABLED=0 -trimpath`), stamps version/commit/date, copies `uv.sh` and `termit-mn.service` out of the repository, and writes `SHA256SUMS` | `deploy/release` | unit tests for the asset-name contract, the version pattern, the checksum format, digesting and both refusals; two builds with a fixed commit and date produce byte-identical `SHA256SUMS` |
| Asset names exactly as the installer expects, with `uv.sh` published beside them; no tarballs | `deploy/release` | lab: `sha256sum -c SHA256SUMS` on all six files, then an install from the release directory — `termit-uv v0.1.0 (commit …, api 1, profile v1)`, identity and certificate intact, services active; the server binary reports the same version after a restart |
| CI: `govulncheck` against our packages; a tag-triggered release job that builds, verifies the checksums and attaches the artifacts; a manual door on `release/<version>` that publishes the same artifacts as a `pre-v<version>-<run>` pre-release | `.github/workflows/ci.yaml`, `.github/workflows/release.yaml` | `govulncheck v1.9.0` locally: no vulnerabilities reachable from our code; the workflow runs the same commands the release tool documents |
| `v0.1.0` and `v0.2.1` tags | `git tag` | `v0.1.0` was published straight away. `v0.2.1` went through the pre-release door first, and was promoted to a full release on 2026-11-15, after the production pass: two nodes on different providers, real Let's Encrypt certificates, WARP egress on both, a phone client, and an in-place Debian 12 → 13 migration of one node (0.2.1 changelog). Flipping the flag is still the act that says the pass is done |

### Interactive front end — done

Both binaries carry a menu (`internal/termui`, `cmd/uv/menu.go`, `cmd/mn/menu.go`): a numbered screen
that asks what the flags would ask and then runs the same command line a script would run, so a menu
entry and its flags cannot drift apart. On a terminal the bare binary opens it; anywhere else it stays
usage text and exit 2. It is line-oriented on purpose — no raw terminal mode, no third-party TUI
library, and it can be driven by a script, which is also how it is tested. Colour appears only on a
terminal and obeys `NO_COLOR`/`TERM=dumb`; the screen is never cleared without colour, so a captured
session reads as sections rather than one overwritten page.

Deliberately absent: starting `termit-mn serve` from the menu (the unit owns the log), and any state of its
own (a dropped SSH session loses nothing). Interactive prompts stay out of the install path — the menu
builds flags, it does not replace them, which is why `uv.sh`, `termit-uv init` and `termit-mn init` remain scriptable.

### The admin credential, from a file — done

The admin token was environment-only, which is fine at a terminal and useless in a timer: systemd has no
shell to export from, so the token ended up pasted into a unit file or a playbook. `termit-mn` now reads
it from a file as well (`--token-file`, `TERMIT_MN_ADMIN_TOKEN_FILE`), and `termit-mn init
--admin-token-out FILE` writes a new token straight into one.

Two decisions worth keeping:

- **No default path.** A well-known location would authenticate silently from a file nobody remembers
  writing, and a stale token would present as a wrong password. The operator names the file or it is not
  read.
- **Refuse a file we cannot vouch for.** Mode must be `0600` or narrower and the owner must be the user
  running the command, because a group-readable token file is a leaked credential shaped like a tidy
  setup, and a file owned by somebody else is a way for them to choose the credential. The refusal names
  the `chmod` or `chown` that fixes it.

The node API then moved behind a configurable prefix (`api_prefix`): random on a fresh install, part of
every node's `mn.url`, hidden from a scanner probing `/api/v1`, and documented as a surface in
`docs/MN-API.md` — the first time the API has a reference of its own rather than a narrative section in
`PROTOCOL.md`. Subscriptions stayed at `/sub` and metrics at `/metrics`: a URL in a client's hands and a
scrape target are not paths an operator should have to keep secret for the server to be safe.

The server then got the masking site the nodes have had since M3: the same generator (`internal/decoy`,
moved out of `internal/uv` so both binaries can use it), generated at startup from the domain and a seed
kept in the database, and served from memory — the server is the listener, so there is no tree to write.
That closed the last asymmetry a scanner could read: `/api/v1` was hidden behind a random prefix, but the
host still answered every other path with Go's `404 page not found`.

Rotation followed in the next milestone (`termit-mn admin rotate`): the caller replaces its own
credential, the id moves with the token because it is derived from it, `last_used_at` is cleared because a
fresh token has never been presented, and the old digest stops resolving in the same write. A lost token
still has no online recovery — that is the price of storing only a digest — and the operations document
keeps the delete-the-row procedure for it.

### Documentation: a guide for participants — done

The reference documents assume an operator. A person who owns one node needs a different entry point, so
`docs/guide/` holds five pages — how Termit works (with diagrams), using a node, running the server, the
interactive menu, and what to do when something breaks — and `docs/guide/ru/` holds the same five in
Russian, side by side and cross-linked from every page, and `README.ru.md` is the same front page in
Russian. `AGENTS.md` records
that these are the only Russian content in the repository, and that a change to one side updates both.

Deferred, and stated as such rather than claimed: **the final manual VPS pass** on real Debian 13 with a
real domain and Let's Encrypt. The lab does everything else (ACME against Pebble, TLS, the mesh, an
upgrade from a release directory), but a production CA, a real DNS provider and a phone client are not
lab-shaped — so the publish workflow marks every release `prerelease: true` until someone runs that
pass. The Docker Compose harness remains a developer convenience (QUESTIONS Q9.4) and is not built.

---

## M5 — Security hardening (planned; decisions taken 2026-11-15)

Nothing here is implemented yet except M5.0 (§5.0) and M5.1–M5.6 (§5.9). The decisions below were made
by the operator on 2026-11-15; the milestones in §5.9 are the implementation plan, and each one ends in
something the lab can check.

### 5.1 Decisions

| Question | Decision |
|---|---|
| Backend | **Our own code on nftables**, inside `termit-uv` / `termit-mn`. No ufw, no fail2ban: the lab boxes have neither, `nftables 1.1.3` is already installed and its service enabled, and one backend means one place where bans live. |
| Firewall model | **Whitelist after a blocklist**: a public CIDR list is denied, then `22/tcp`, `80/tcp`, `443/tcp` are allowed for everyone else; with `default_deny` (on by default) the rest of the inbound traffic is dropped. |
| Who turns it on | **The operator, explicitly.** `init`, `sync`, the agent loop and the timers never touch firewall state; `DESCRIPTION.md` §11 keeps its promise and gains the one exception this needs, in the same commit. |
| Scan threshold | **Configurable, 5 attempts per 10 minutes by default**; a repeat scan within the ladder window → 24 hours. |
| Permanent block | **A separate persisted list** the operator owns (`blocked.txt`), plus the ban ladder for everything automatic. Nothing becomes permanent without the operator. |
| `ssh_from` | **Opt-in flag / config list**, not a default: a static address is the exception, not the rule. |
| Lockout safety | **Apply with a five-minute rollback timer**: `termit-uv firewall confirm` keeps the rules, anything else reverts them. |
| Persistence | **Bans live on disk with their TTL** and survive a reboot; the nftables sets are rebuilt from that file on start. |
| BitTorrent | **Opt-in Xray sniffer + routing rule**; off by default, a spike verifies the sniffer actually recognises it before we ship it. |
| Scope | **Nodes and the server, one mechanism**: same code, different allowed ports. |
| Defaults | **Everything off.** The wizard offers "harden this node" as a checkbox that is unchecked, and `init --harden` does the same without a wizard. |

### 5.0 Spike results (measured on `termit-uv2.lan`, nftables 1.1.3, Xray 26.11.9)

M5.0 asked two questions. Both are answered, and the second answer changed the config we planned to
ship. The throwaway rulesets and configs lived in the session scratch, not in the repository; the
conclusions are here.

**The scan detector is `meter`, and the form that looks like its modern replacement is not one.**
nftables 1.1.3 refuses that form outright when it carries a TTL, and accepts it without one but then
fires on the wrong packet:

| Form | `nft -c -f` | With one packet from a fresh source |
|---|---|---|
| `meter scan4m { ip saddr limit rate over 5/minute } add @scan4 { ip saddr timeout 15m }` | accepts | nothing added; the offender appears only once the rate is exceeded — **what we want** |
| `add @scan4 { ip saddr limit rate over 5/minute timeout 15m }` | `Error: syntax error, unexpected timeout` | — |
| `add @scan4 { ip saddr limit rate over 5/minute }` with the set's `timeout 15m` | accepts | **the element is added on the first packet**, so a drop rule keyed on it bans a single probe |
| `ip saddr limit rate over 5/minute add @scan4 { … }` | `Error: syntax error, unexpected limit` | — |

A single UDP packet to a closed port per data point, so the count is exact. The third form is not a
detector: `add` creates the element and the `limit` merely becomes part of the element it stores
(`nft list set` shows `192.168.88.135 limit rate over 5/minute burst 5 packets`), and the `add` never
consults it. The `meter` statement does — its set *is* the rate state, and the statements after it run
only when the rate is exceeded. §5.3's sketch stands as written, and the `log prefix` fallback it named
is not needed.

Three consequences for the implementation:

- The meter's own set (`scan4m`) is `flags dynamic` with **no timeout** and `size 65535`: one entry per
  source that ever scanned, and its rate state never expires. The memory is bounded (a few megabytes at
  worst) but the entries are not, so the guard flushes that set on the counting window — every
  `guard.scan.window`, ten minutes by default — in the same pass that reads `@scan4`. Flushing resets
  rate state only; an offender is detected again by scanning again.
- Rate state is per source and per rule, so IPv6 needs its own meter. Verified: one meter per family,
  both accepted inside one `inet` table.
- The element-level `timeout` and a `limit` expression cannot be combined, so the `@scan4` TTL comes
  from the statement (`add @scan4 { ip saddr timeout 15m }`) and the meter's set has none. The two
  sets differ on purpose.

**Rebuilding the table is one atomic batch.** A file that begins with `destroy table inet termit_uv` and
then declares the whole table applies cleanly whether the table is absent or present (both exit 0), and
the second apply *replaces* the contents instead of merging — applying the same ruleset twice does not
accumulate duplicate rules. Elements go with the table, bans included, so the guard re-applies
`bans.json` immediately after every apply. Teardown is `nft delete table`, which exits 1 when the table
is absent; `firewall off` treats that as success.

**Reading bans back.** `nft -j list set … banned4` returns two element shapes, and the parser must take
both: a bare string for an element with no timeout (the operator's permanent ban) and
`{"elem": {"val": "203.0.113.7", "timeout": 3600, "expires": 3599}}` for one with a TTL, where
`expires` is the seconds left. `nft delete element … { 1.2.3.4 }` removes one element; a set with a
default TTL also expires its elements on its own, which is the point of the design.

**The sniffer does classify BitTorrent — and `routeOnly: false` would have been a bug.** With
`"sniffing": {"enabled": true, "destOverride": […], "routeOnly": false}` and a routing rule
`{"protocol": ["bittorrent"], "outboundTag": "block"}`, a torrent handshake through a socks inbound went
to the blackhole (`app/dispatcher: taking detour [block]`) while a TLS ClientHello passed. The same run
also logged `sniffed domain: spike … taking detour [direct] for [tcp:spike:19000]`: with
`routeOnly: false` the sniffed HTTP `Host` **replaces the destination**, so an HTTP request through the
tunnel is dialled to the name in its own header and resolved by the node's DNS instead of going where
the client asked (`lookup spike on 192.168.90.1:53: no such host` — a real rewrite, not a logging
artefact). `routeOnly: true` keeps the destination and still classifies: HTTP and TLS pass, the
handshake is blocked. §5.7 now says `routeOnly: true`.

Also measured: classification does not depend on `destOverride` — with `"destOverride": []` and
`routeOnly: true` the handshake is still blocked and nothing else is sniffed. That is the smallest
configuration that does the job, and the one §5.7 shows: no HTTP/TLS/QUIC sniffing, so the only cost is
reading the first bytes of a connection.

### 5.2 What owns what

```
                 ┌───────────────────────── termit-uv / termit-mn ────────┐
 nftables table  │ firewall apply/off/confirm   bans list/unban/block    │  state files
 inet termit_*   │        │                            │                 │
                 │        └──────┬─────────────────────┘                 │
                 │               ▼                                       │
                 │         guard loop (unit) ── reads ── nginx access log│
                 │               │              and the ban sets        │
                 │               └── writes ── /var/lib/termit/*/bans.json
                 └───────────────────────────────────────────────────────┘
```

- **The firewall is the table.** One table per component (`inet termit_uv`, `inet termit_mn`), one
  input chain hooked at a negative priority so it runs before the distribution's rules. Everything
  Termit adds lives in that table and nowhere else: `termit-uv firewall off` deletes the table and the
  host is exactly as it was.
- **The guard is the ban engine.** A long-running unit (same shape as the agent) that reads the nginx
  access log, watches the ban sets, escalates repeats and writes the state file. It never parses
  free-form text it did not produce itself.
- **The blocklist is a file refresh.** A timer fetches one URL, validates it, and replaces
  `blocklist.txt` atomically; the last good copy stays in force when the fetch fails.

### 5.3 The table

Rendered from a typed model, like the Xray config, with a golden file per component and per option
combination. Sketch (v4 shown, v6 is the same with `ip6 saddr`):

```
table inet termit_uv {
    set blocked4  { type ipv4_addr; flags interval; auto-merge; }        # the downloaded list
    set banned4   { type ipv4_addr; flags timeout; }                      # dynamic, TTL per element
    set ssh4      { type ipv4_addr; flags interval; }                     # only with ssh_from
    set scan4     { type ipv4_addr; flags dynamic, timeout; timeout 15m; } # the guard reads these
    # scan4m is not declared here: the meter rule below creates it as its rate state.

    chain input {
        type filter hook input priority -5; policy accept;
        iif lo accept
        ct state established,related accept
        ct state invalid drop
        ip saddr @blocked4 drop
        ip saddr @banned4  drop

        # ICMPv6 is not optional for IPv6: without NDP and PMTU messages the family dies.
        icmpv6 type { nd-router-solicit, nd-router-advert, nd-neighbor-solicit,
                      nd-neighbor-advert, packet-too-big, destination-unreachable,
                      time-exceeded } accept

        tcp dport 22 accept            # or: tcp dport 22 ip saddr @ssh4 accept
        tcp dport { 80, 443 } accept

        # Everything else: count per source, remember who tried, drop when default_deny is on.
        # The meter's set is the rate state; the add writes the offender into scan4 with a TTL.
        meter scan4m { ip saddr limit rate over 5/minute } add @scan4 { ip saddr timeout 15m }
        drop
    }
}
```

Two details are deliberately not left to the implementation:

- **A scan is counted in the kernel, banned by the guard.** `meter … limit rate over N/minute` needs no
  userspace, and the guard sees the resulting element in `@scan4` and applies the ban with the ladder's
  TTL. That keeps one ban path for both signals and avoids a second, kernel-only ladder that the state
  file would not know about. The spike settled the syntax (§5.0): the `meter` statement is the form that
  gates, its set needs a name of its own (`scan4m`) because the meter creates it, and the guard flushes
  it on the counting window.
- **`default_deny` is configurable and on.** A node publishes exactly three ports; anything else
  inbound is a scan or a leftover service the operator forgot, and both are worth refusing. Off is for
  an operator who runs something else on the box and knows it.

### 5.4 Signals and the ban ladder

| Signal | Evidence | First ban | Escalation |
|---|---|---|---|
| Port scan | `@scan4` / `@scan6` elements (kernel meter, N attempts per window) | 1 hour | second within 24 h → 24 hours; then the offender is listed |
| Failed `443` request | nginx access log: 4xx on any path, counted per source in a sliding window | 5 minutes | doubling: 5 → 10 → 20 → 40 → 60 minutes cap |
| Operator | `termit-uv bans block <ip>` | permanent | stays until `unblock` |

- Every decision goes to `bans.json` (`ip`, `signal`, `until`, `count`, the last ban's length) and the
  guard re-applies the unexpired ones whenever the kernel lost them — a reboot does not clean a slate.
  The ladder's memory therefore lives in the file, not in a process: restarting the guard changes nothing.
  Only the 4xx window is in memory, so a restart asks for another window of failures, which is the safe
  direction.
- Repeated offenders are never blocked automatically: they appear in `termit-uv bans offenders` with the
  count and the last signal, and the operator decides. "Repeated" is not a hard-coded third time: a
  source is an offender when the ladder hands it no longer ban than it served last time, so the answer
  follows the configured ladder.
- **Never ban**: loopback, RFC1918/ULA sources, the management server's address (taken from `mn.url`),
  and the operator's own `ssh_from` list. A ban on those would be self-inflicted, and the guard checks
  before it writes anything — `bans block` refuses them too.

### 5.5 Commands

```
termit-uv firewall status|on|off|confirm|rollback    --dry-run, --ssh-from CIDR, --default-deny=false
termit-uv firewall refresh                          --url, --dry-run: the blocklist
termit-uv bans list|unban IP|block IP|unblock IP|offenders
termit-uv guard status|run --once                  (the unit runs `guard run`)
termit-mn  firewall … / bans … / guard …           the same code, the server's table and ports
```

- `on` renders the table, arms the rollback timer, applies it, and records what it applied in
  `firewall.json`; the timer's oneshot runs `firewall rollback`, which deletes the table unless
  `confirm` ran. The timer is armed **before** nft sees a rule: a firewall that cannot be undone is
  never installed.
- `off` deletes the table, disables and removes the timer units, and records that it did.
- `--dry-run` prints the rendered rules and, when running as root, also asks `nft -c` whether they
  parse; it changes nothing, which is also what the lab asserts.
- The commands take `--ssh-from` and `--default-deny` as one-off overrides of the configuration, and the
  record keeps what was actually applied so `status` never lies; the configuration file is not edited,
  because the owner edits it by hand.
- Still missing on purpose: the check against an install in flight. There is no install lock in the
  tree yet, and a check for a file nothing creates would be decoration — it lands with M5.6, where
  `init` starts taking one.

### 5.6 Configuration

What exists today (M5.1); the keys of the later milestones are added by them, so nothing is accepted
before something reads it:

```yaml
security:                       # everything here is off unless the operator says otherwise
  firewall:
    enabled: false              # the operator's intent; `firewall status` compares the host with it
    default_deny: true
    ssh_from: []                # CIDRs allowed to reach 22; empty means "from anywhere"
  guard:
    enabled: false              # also decides whether the table carries the scan meters
    scan:
      attempts: 5
      window: 1m                # nftables can only write "N per <unit>"; see below
      remember: 15m             # how long the kernel remembers a scanning source
      ban: 1h                   # the first rung
      repeat: 24h               # what a scan inside the ladder window earns
    path404:
      attempts: 10              # 4xx answers from one source inside the window
      window: 5m
      ban: 5m                   # doubling from here…
      max: 1h                   # …to here
    ladder_window: 24h          # how long a repeat still counts as one
```

Still to come, with the milestone that reads it: `bittorrent_block` (M5.4). The blocklist keys
(`blocklist_url`, `blocklist_refresh`, M5.3) are in place.

Three constraints the loader enforces:

- **`scan.window` is exactly `1s`, `1m`, `1h`, `1d` or `1w`.** nftables writes a rate as
  `N/<unit>` and nothing else, so the plan's original `5 attempts per 10m` has no spelling; a
  ten-minute window is refused at load rather than silently rendered as something else. The default is
  five attempts a minute, which is stricter than the original intent and still far above a single probe.
- **Thresholds are at least 2** and every duration is positive, so a single failure can never ban.
  `path404.window` must be shorter than `ladder_window`, or a 4xx run would always look like a first
  offence.
- **The ladder's memory is measured from the end of the previous ban, not from its start.** With a
  window as long as the repeat ban, the second reading would make a repeat impossible: the source is
  let back in exactly when the memory expires, and a scanner that never stops would never be recognised
  as one. `bans offenders` is what the third offence produces, not a hard-coded "third time".

CIDRs in `ssh_from` are parsed, normalised to their network and sorted. `termit-mn` gets the same block
with its own ports (`22`, `80`, `443`, plus `listen`).

### 5.7 BitTorrent

`security.bittorrent_block: true` adds to the generated Xray config, not to the firewall:

```json
"sniffing": { "enabled": true, "destOverride": [], "routeOnly": true },
"routing":  { "rules": [ { "type": "field", "protocol": [ "bittorrent" ], "outboundTag": "block" } ] }
```

`routeOnly` is `true` and `destOverride` is empty on purpose, and §5.0 measured why: with
`routeOnly: false` the sniffed HTTP `Host` replaces the request's destination, so the tunnel would dial
a name the node resolves instead of the address the client asked for; and BitTorrent is classified
whether or not a protocol is listed in `destOverride`, so listing `http`/`tls`/`quic` buys nothing but
extra parsing on every connection.

The spike also settled that this build does classify the handshake — a torrent handshake went to the
blackhole while a TLS ClientHello passed — so the feature is not a placebo and ships. A real torrent
client is still not lab-shaped, so that half of the check stays manual. Sniffing costs a little CPU on
the first bytes of each connection, which is why it is off by default and not implied by the firewall.

### 5.8 Blocklist refresh

- One `GET` of a pinned URL, no `git` dependency; `If-Modified-Since` and a size cap.
- Parsed as CIDRs (`iproute2`-style lines, `#` comments), normalised, deduplicated, sorted, and
  rendered deterministically so the file's digest means something.
- Replaced atomically; the old file stays until the new one parses. A refresh that finds nothing
  usable is a warning, never an empty list.
- `termit-uv firewall refresh` runs it once; the timer runs it daily with a randomised delay.

### 5.9 Milestones

**M5.0 — Spike (done).** On `termit-uv2.lan`: nftables 1.1.3 accepts the `meter … add @set { …
timeout … }` form and refuses the two shapes that look equivalent (element-level `timeout` with a
`limit`, and a `limit` matcher before `add`); the `add @set { … limit … }` form without a meter adds on
the first packet and is not a detector; `nft -c -f` accepts the whole table, both families, one meter
each. The pinned Xray 26.11.9 does classify a BitTorrent handshake, and `routeOnly` must be `true`. All
of it is in §5.0, including what changed because of it.

**M5.1 — Table and commands (done).** `internal/firewall` renders the table from a typed model and
applies it through `nft -f -`; `internal/uv/service` renders the rollback pair; `cmd/uv/firewall.go`
implements `status|on|off|confirm|rollback` with `--dry-run`, `--ssh-from` and `--default-deny`; the
`security:` block is in the node's configuration. Four golden files (node with and without
`default_deny`, node with `ssh_from`, server ports), all four of which nftables 1.1.3 accepts
(`nft -c -f`). `test/lab/firewall_test.go` is the scenario: dry run changes nothing, `on` keeps SSH and
443 alive and arms the timer, the oneshot removes an unconfirmed table, `confirm` stands the timer down,
and `off` returns `nft list ruleset` to its exact previous digest.

The lockout drill was run by hand on `termit-uv2.lan`, because it costs five minutes: with
`--ssh-from 203.0.113.0/24` a **new** SSH connection was refused, port 443 kept serving inside the
allowed path, and after 310 s the timer fired on its own, removed the table and SSH came back — `status`
then said `off since 14s ago` and the journal said why.

**M5.2 — Guard and bans (done).** `internal/firewall` gained the ban record, the ladder as a pure
function, the never-ban set and the set-element operations; `internal/guard` is the loop — the kernel's
scan set, the web server's log with a rotation-aware tail, the 4xx window, and a reconcile pass that
makes the kernel match `bans.json`. `termit-uv bans list|offenders|block|unban|unblock` and
`termit-uv guard status|run [--once]` exist; `init` installs and starts the guard unit when
`security.guard.enabled` is true, and uninstall stops it.

Three things the lab changed:

- **The unit needed `AF_NETLINK`.** With the agent's `RestrictAddressFamilies` copied over, every cycle
  failed with `Unable to initialize Netlink socket: Address family not supported by protocol` — nft
  talks to the kernel over netlink. The comment in `service/guard.go` says so, because it looks like a
  line that could be tightened.
- **`bans unban` clears the kernel's scan element too.** Otherwise the meter's own memory — minutes
  long — re-banned the source on the next cycle and the operator's decision looked broken.
- **The lab cannot produce a bannable scan.** Every address it has is RFC1918, so the never-ban rule
  correctly refuses them; `TestLabGuard` therefore proves both halves separately: a real scan from the
  server lands in the meter and is *not* banned, and a documentation-range address put into the scan
  set the way the meter would is banned for an hour, lifted, and then banned for a day on the next
  offence.

Tests: the ladder arithmetic and both caps, the window measured from the released ban, the never-ban
set, the log tail with rotation, the reconcile from the file after a "reboot", and the commands.

**Open question, resolved (2026-11-15): a boot unit.** The table lives in the kernel, so a reboot removes
it. The answer is `termit-uv-firewall.service`: a oneshot ordered before `network-pre.target` that runs
`termit-uv firewall restore`, which reads the record and decides — a confirmed apply comes back with no
timer (the operator already watched that exact table work), an apply still inside its five-minute window
comes back with the timer armed (the safety net still exists), and anything else — never confirmed, or
taken down by `off` — stays gone. The plugin, the server's copy in `deploy/systemd` and the lab check
(`TestLabHarden`: delete the table the way a reboot does, start the unit, the table returns without a
timer; `off` then the unit, nothing returns) are in §5.9.

**M5.3 — Blocklist refresh (done).** `internal/firewall/blocklist.go` parses, normalises, renders and
stores the list, fetches it over `http(s)` or `file` with `If-Modified-Since` and a 4 MiB cap, and
replaces the kernel's two sets in **one** `nft -f -` batch (`flush set` plus the elements), so the host is
never without a blocklist. `termit-uv firewall refresh` is the command (`--url` for a one-off,
`--dry-run`); `init` installs `termit-uv-blocklist.timer` when `security.firewall.blocklist_url` is set;
`firewall on` reloads the file, and `status` reports the count and the fetch age.

The decision that is not obvious: **a source that has not changed still reloads the kernel.** The file is
the record of truth — a reboot or a failed load empties the sets — so skipping the reload on a 304 would
leave a host with a list on disk and nothing in the kernel until the *next* change. The download is what
the conditional request saves; the load is cheap and idempotent.

Lab (`TestLabBlocklist`, 8.7 s): the timer unit is installed and enabled, its own oneshot (started by
hand, which is what systemd does daily) loads a `file://` list into the kernel, a truncated source is
refused with the sets untouched, an unreachable source is refused the same way, a network that leaves the
source leaves the kernel, and `off` then `on` restores the list from the file.

**M5.4 — BitTorrent (done).** `security.bittorrent_block` (off by default, `security:` top level, not the
firewall) reaches `xray.Spec.BlockBitTorrent` through both paths that generate the config — the install
step and the agent's reconciler — because a config regenerated without the flag would quietly stop
blocking. The generated document swaps the sniffer for `{enabled, destOverride: [], routeOnly: true}` and
puts `{protocol: ["bittorrent"], outboundTag: "block"}` first in the routing rules, before any egress
decision. `testdata/xray-bittorrent.json` pins it, and a second test states the two properties a reviewer
would otherwise "improve": the empty `destOverride` and `routeOnly: true` are the spike's conclusion, not
an oversight.

The golden was fed to the pinned core on the lab node: `xray -test` says `Configuration OK` (26.11.9).
A real torrent client is still not lab-shaped, so that half stays manual and `OPERATIONS.md` §6.1 says
what to watch: the handshake dies, an HTTPS request through the same client does not.

**M5.5 — MN parity (done).** The commands are one implementation, not two: `internal/harden` holds the
`security:` block both configurations embed, and `internal/hardencmd` holds the six firewall, five
bans and two guard commands. Each binary supplies a `Target` (table name, ports, units, paths, access
log, whether a unit directory exists) and the seams a test replaces; `cmd/uv`'s three command files
became one-liners, and `termit-mn` gained the same three.

Two decisions the server forced:

- **Ports 22, 80 and 443.** Port 80 has to stay open for *every* node's renewal, not only while this
  server's own ACME order is in flight, so it is not conditional. `firewall.ServerPorts` says so.
- **The server's units are files, not rendered ones.** The node writes its units because every path in
  them is configurable; the server has one layout, so `deploy/systemd` carries the guard, the blocklist
  pair and the rollback pair, and `firewall on` **refuses to run until the rollback timer is installed**.
  An apply with no way back is worse than a refused command, and the refusal is the first thing an
  operator sees. That is also why the blocklist interval on the server is the timer file rather than
  `security.firewall.blocklist_refresh`: the server's units are edited, not generated.

The server's guard counts scans only: the server writes its log to the journal, and its admin API is
rate-limited instead of banned. `guard status` says `log: none: this component keeps no access log`
rather than pretending a signal exists.

One bug fixed on the way: `bans block` built the never-ban set from `ssh_from` alone, so the
management server's address — which the guard refuses to ban itself — could be blocked by hand and cut
the node off from the mesh. Both now use the same set.

Lab (`TestLabMNFirewall`, 6.9 s): the units are installed from `deploy/systemd` the way the
documentation says, `on` applies `inet termit_mn` with `default deny` on 22/80/443, the admin socket
still answers, a node syncs through :443 while the table is up, `check --all` passes, `bans
block`/`unblock` work against the server's sets, `off` returns `nft list ruleset` to its previous
digest, and the ban record survives the table.

**M5.6 — Wiring (done).** `init --harden` sets `security.firewall.enabled` and `security.guard.enabled`
and stops there: the table is still `firewall on`, so no install can put a default-deny table on a host
while nobody is watching, and `init --harden=false` undoes it. The wizard asks the same question,
unchecked, and passes the same flag (new keys in both languages). `status` gained a `firewall:` line that
appears only when the operator has business with it — the configuration asks for a table, or one was
applied once — and it reads both the record and the kernel: `off (the configuration asks for it: run
`termit-uv firewall on`)` is what an operator sees between `init --harden` and `on`. `uninstall` removes
the table before the binaries, because a default-deny table with no command left to remove it is a host
nobody can repair without a console.

Lab (`TestLabHarden`, 7.0 s): `init --harden` installs and starts the guard's unit and stops, `status`
says what to run, `on` + `confirm` applies the table, a real `guard run --once` pass succeeds against it,
and the cleanup leaves the node as it was found.

**M5.7 — Docs and the §11 amendment (done).** Done: `DESCRIPTION.md` §11 invariant 1 (never touch
routing; the table exists only after `firewall on`; the guard and the blocklist refresh only fill sets; the
boot unit only puts back a confirmed apply), `OPERATIONS.md` §18 with the exact rules, the way back, the
blocklist, the server's copy and what survives a reboot, `THREAT_MODEL.md` §5.6/§5.7 and the residual-risk
line, `QUESTIONS.md` §0a, and the page a new operator needed:
[`guide/hardening.md`](guide/hardening.md) and its [Russian mirror](guide/ru/hardening.md), linked from
both overviews, both READMEs and the command tables of `using-uv` and `using-mn`.

**M5.8 — Rollout.** Lab first, all of it; then the production MN (`firewall on` with the rollback
timer, then `confirm` from a second session), then the two nodes one at a time, checking after each
step: SSH alive, 443 serving, a client request through the tunnel, the MN's prober green, `status`
clean. Any step that fails: `firewall off`, and the host is where it started. Reboot one node on purpose
before touching the second.

The lab already did it once, on `v0.3.0` (2026-11-15): a reboot of uv3 and of the MN, once with a
confirmed apply and once with one still inside its window. The confirmed half came back as designed and
armed no timer; the pending half failed with `read-only file system`, because arming the rollback timer
writes its units and `ProtectSystem=strict` did not allow it — that is what 0.3.1 fixes, and the lab
check now drives both record paths instead of only the confirmed one. A reboot on the production node is
still worth doing: the lab's boxes are containers, and a container's boot is not a host's.

The build to roll out is the pre-release `v0.3.2p4` (2026-11-16): the `0.3.1` fix, the owner's page
listing local devices and redrawing itself on every sync, the `device remove` argument fix, one link
carrying every client's fragmentation switch — Happ's `fragment=…` plus `sb.tls.fragment=1` and
`sb.tls.record_fragment=1` for `podkop-engine` and the patched sing-box builds — on the page, in
`termit-uv uri`, in the device links and in every line of the subscription list, and a route that sends
UDP `direct` instead of into a SOCKS proxy that cannot carry it (found on the production phones: every
name resolved through the node timed out). It is installed like a release — `sh uv.sh --version
v0.3.2p4 …` — and the `p<n>` counter numbers builds of the `0.3.2` branch, so a later build supersedes it
without renumbering the version itself.

Total: 5–7 days of work. The first half-day — the spike that decided two syntax questions which could
have re-shaped the rest — is spent and recorded in §5.0.

### 5.10 Explicitly out of scope

- **Rate limiting of proxied traffic.** The operator owns their node; a limiter adds latency to the one
  thing it exists for and is a timing fingerprint. The BitTorrent rule stays, because it catches an
  accident rather than abuse.
- **Any routing, NAT or `ip rule` change.** WARP stays a loopback proxy; nothing in this milestone sees
  a packet of host traffic other than inbound.
- **A GUI, a threat feed, or a second blocklist.** One list, one URL, one file, operator-editable.
- **Banning on the client's own protocol errors.** A wrong key answers `404`, which is a 4xx and
  therefore counts — that is intended and documented, with `bans unban` as the way out.
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
| R4 | Daily convergence means up to 24 h of stale peers | High (by design) | Low | documented; `termit-mn check now` + admin CLI for urgent cases; sync interval configurable |
| R5 | Let's Encrypt path untested in the LAN lab | High | Medium | Pebble covers the ACME code path; the real-domain pass gates promoting a release out of pre-release, and the workflow publishes pre-release until it is done |
| R6 | MN compromise or DB theft exposes all pair credentials | Low | Critical | master key can live outside the DB; backups encrypted by the operator; threat model documented |
| R7 | Scope creep toward a panel/quota/billing product | High | Medium | non-goals list is explicit; v1 API stays minimal |
| R8 | Dependency churn (SQLite, ACME, xray-core pinning) | Medium | Medium | pin versions + checksums, `govulncheck`, small module graph |
| R9 | Development host has no Go/Docker and targets Linux | High | Low | M0.1 installs the toolchain; cross-compile; lab machines are the runtime |
| R10 | Decoy templates look fake | Medium | Medium | user supplies the inspiration source; generator is pluggable; `termit-uv renew-site` iterates |

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
4. ~~Whether `termit-uv` should enable WARP by default on fresh installs~~ **Resolved** on 2026-11-10: on by
   default, opt-out with `--no-warp` or `warp.mode: off`. See M4.1.
5. Whether the MN domain and the UV domains are expected to live in the same DNS zone (affects the
   DNS-consistency check and documentation).

## 10. Backlog

Work that is understood, wanted and deliberately not in the current milestone. Each item names the rule
that keeps it out, so nobody re-discovers the reason from scratch.

### 10.1 WARP that carries UDP

Today `warp.mode` routes TCP through `warp-cli`'s SOCKS5 listener and sends UDP `direct`
(`docs/OPERATIONS.md` §6): proxy mode has no UDP ASSOCIATE, so a UDP flow handed to it never returns and
the first casualty is client DNS. The split keeps the nodes usable and gives up the address WARP exists to
hide, for UDP only. Four ways to close that gap were surveyed on 2026-11-16.

| Option | UDP | Host routing | Cost |
|---|---|---|---|
| **A. Xray `wireguard` outbound** with WARP credentials (`noKernelTun: true`) | yes | **none** — gVisor netstack inside the core | needs a WARP registration and a stored private key; one fewer service than today |
| B. `wgcf` + `wg-quick` (what `references/Blitz` does) | yes | `ip rule`, routes, `resolvconf` | contradicts §2.2.1 |
| C. `warp-cli` in TUN mode | yes | full-tunnel capture | contradicts §2.2.1, and `/dev/net/tun` is absent on the LXC nodes |
| D. `usque` (MASQUE, userspace SOCKS5 with TCP and UDP) | yes | none | a third-party service and its netstack, plus a licence to check; MASQUE is where Cloudflare is going |

**Recommended: A.** Xray has shipped a userspace WireGuard outbound since 1.6.5 and documents the WARP
setup end to end ([xtls guide](https://xtls.github.io/en/document/level-2/warp.html)); `noKernelTun` forces
the in-process gVisor stack, which is exactly the LXC case these nodes are
([outbound docs](https://xtls.github.io/config/outbounds/wireguard.html)). `wgcf generate --xray` prints the
credentials in the shape the outbound wants, including the three `reserved` bytes Cloudflare needs. Because
the tunnel lives in the core, the fail-open balancer keeps working unchanged and the node keeps one
long-running process instead of two. The registration is the only new secret (a WireGuard private key per
node, `0600` in the state directory) and it must never reach a log.

The risk to design around: Cloudflare is moving WARP to MASQUE, and WARP+ already reportedly does not work
over WireGuard ([wgcf#459](https://github.com/ViRb3/wgcf/issues/459)). Free WARP works today. The outbound
must therefore stay swappable — the routing rule names a tag, not a protocol — so option D can replace it
without touching anything else.

Acceptance, when it is picked up: a UDP probe through the tunnel reports `warp=on` in the trace, the node's
own SSH/ACME/decoy traffic still leaves from its own address, `wgcf`-equivalent credentials survive a
restart and are never logged, fail-open still moves traffic to `direct` when WARP stops answering, and no
`ip rule`/`ip route` change exists on the host.