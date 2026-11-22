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
scheduler's job (`DESCRIPTION.md` §7.2), not this milestone's.

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
express the invariants `DESCRIPTION.md` §6.3–§6.5 state: each node must serve exactly its two peers, the three pair
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

The three places where `DESCRIPTION.md` contradicted the build are reconciled as of 2026-11-21: the sync
example carries `mesh_generation` and `policy.mesh_poll_seconds`, §15 describes the credential store as it
is (256-bit random keys, SHA-256 digests, found through an index), and §13.4 lists `termit-mn sub rotate`
instead of a `sub show` command that never existed. `docs/PROTOCOL.md` stays what the build does; the
design document now says the same.

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

Deliberately absent: the long-running loops — starting `termit-mn serve`, `termit-uv agent` or `guard run`
from the menu would hide a log behind a menu screen, and the units own them; `firewall rollback`, which the
rollback timer runs and whose own help says it is not for interactive use; `version` and `menu`, which the
header and the command itself already answer; and `termit-mn init`, which is the command you need before
there is a server for the menu to talk to. Everything else in both binaries has an entry, so the menu and
the CLI cannot drift into disagreeing about what exists — and any state of its own is absent too (a dropped
SSH session loses nothing). Interactive prompts stay out of the install path — the menu builds flags, it
does not replace them, which is why `uv.sh`, `termit-uv init` and `termit-mn init` remain scriptable.

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

Nothing here is implemented yet except M5-0 (§5.0) and M5-1–M5-6 (§5.9). The decisions below were made
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

M5-0 asked two questions. Both are answered, and the second answer changed the config we planned to
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
  tree yet, and a check for a file nothing creates would be decoration — it lands with M5-6, where
  `init` starts taking one.

### 5.6 Configuration

What exists today (M5-1); the keys of the later milestones are added by them, so nothing is accepted
before something reads it:

```yaml
security:                       # everything here is off unless the operator says otherwise
  firewall:
    enabled: false              # the operator's intent; `firewall status` compares the host with it
    default_deny: true
    ssh_from: []                # CIDRs allowed to reach 22; empty means "from anywhere"
    udp_ports: []               # inbound UDP besides the TCP ports the component serves (a QUIC transport)
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

Still to come, with the milestone that reads it: `bittorrent_block` (M5-4). The blocklist keys
(`blocklist_url`, `blocklist_refresh`, M5-3) are in place.

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

**M5-0 — Spike (done).** On `termit-uv2.lan`: nftables 1.1.3 accepts the `meter … add @set { …
timeout … }` form and refuses the two shapes that look equivalent (element-level `timeout` with a
`limit`, and a `limit` matcher before `add`); the `add @set { … limit … }` form without a meter adds on
the first packet and is not a detector; `nft -c -f` accepts the whole table, both families, one meter
each. The pinned Xray 26.11.9 does classify a BitTorrent handshake, and `routeOnly` must be `true`. All
of it is in §5.0, including what changed because of it.

**M5-1 — Table and commands (done).** `internal/firewall` renders the table from a typed model and
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

**M5-2 — Guard and bans (done).** `internal/firewall` gained the ban record, the ladder as a pure
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

**M5-3 — Blocklist refresh (done).** `internal/firewall/blocklist.go` parses, normalises, renders and
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

**M5-4 — BitTorrent (done).** `security.bittorrent_block` (off by default, `security:` top level, not the
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

**M5-5 — MN parity (done).** The commands are one implementation, not two: `internal/harden` holds the
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

**M5-6 — Wiring (done).** `init --harden` sets `security.firewall.enabled` and `security.guard.enabled`
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

**M5-7 — Docs and the §11 amendment (done).** Done: `DESCRIPTION.md` §11 invariant 1 (never touch
routing; the table exists only after `firewall on`; the guard and the blocklist refresh only fill sets; the
boot unit only puts back a confirmed apply), `OPERATIONS.md` §13 with the exact rules, the way back, the
blocklist, the server's copy and what survives a reboot, `THREAT_MODEL.md` §5.6/§5.7 and the residual-risk
line, `QUESTIONS.md` §0a, and the page a new operator needed:
[`guide/hardening.md`](guide/hardening.md) and its [Russian mirror](guide/ru/hardening.md), linked from
both overviews, both READMEs and the command tables of `using-uv` and `using-mn`.

**M5-8 — Rollout.** Lab first, all of it; then the production MN (`firewall on` with the rollback
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

The build rolled out was the pre-release `v0.3.2p4` (2026-11-16), and `v0.3.2` is the release it became:
four builds on the `0.3.2` branch, each published as a pre-release and each installed on the live hosts
before the next. What the chain added, in order: the `0.3.1` fix, the owner's page listing local devices
and redrawing itself on every sync, the `device remove` argument fix, one link carrying every client's
fragmentation switch — Happ's `fragment=…` plus `sb.tls.fragment=1` and `sb.tls.record_fragment=1` for
`podkop-engine` and the patched sing-box builds — on the page, in `termit-uv uri`, in the device links and
in every line of the subscription list, and a route that sends UDP `direct` instead of into a SOCKS proxy
that cannot carry it (found on the production phones: every name resolved through the node timed out). The
`p<n>` counter numbered the builds of the branch, so each superseded the previous one without renumbering
the version; the tag `v0.3.2` was cut from the last of them, and the pre-release flag was cleared once
this pass was recorded.

**Production pass, 2026-11-16 — recorded, the release may be published.** Three hosts: the management
server `mn` (LXC on Proxmox) and two nodes (a VM and a reinstalled VM). Every gate was executed against
the live hosts, not the lab:

- **Firewall.** `termit-uv firewall on --ssh-from` on both nodes and `termit-mn firewall on` on the server,
  confirmed after the five-minute window, table `inet termit_uv` / `inet termit_mn`, default deny, ports
  22/80/443 only. The record was confirmed on each host and the table reappeared after rebooting both
  nodes (`systemctl reboot`), which is the thing the lab cannot prove. No other host service stopped
  answering.
- **Guard.** `security.guard.enabled: true` on all three, unit active, `sshd` reachable from the allowed
  source only.
- **Blocklist.** `security.firewall.blocklist_url` set on all three, 145 networks (128 kernel elements)
  loaded, 24-hour refresh timer active, `nft list set` non-empty.
- **WARP.** `warp.mode: all`, egress reported on (WAW and HEL), `warp-egress` balancer with `fallbackTag:
  direct`, and the WARP sidecar survived the node reboots.
- **The owner's page.** Rendered and reachable over TLS; lists the mesh, the local devices and the node's
  own entry; every link carries Happ's `fragment=…` **and** `sb.tls.fragment=1` /
  `sb.tls.record_fragment=1`; the base64 subscription carries the same; the sing-box profile carries
  `tls.fragment` and `tls.record_fragment`.
- **Agents.** Both nodes `active` on the server, syncs landing, subscription URLs served.
- **A real client.** The owner's phone imported a link from the page and passed traffic through both nodes;
  the server logs the sessions (`accepted tcp:… [proxy -> warp] email: owner`, nginx `200`).

Two findings from the live pass that a lab cannot produce, both fixed before this record: the `device
remove` argument order, and UDP being routed into a SOCKS proxy that cannot carry it — client DNS timed out
until UDP was sent `direct` (0.3.2). The remaining compromise, UDP leaving from the node's address, is
planned away in §11.

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

v0.5.0 takes part of this list back off it (§10); everything here without a 0.5.0 item stays deferred.

- Other xray-core protocol kinds; MTProto (§10.2). REALITY and Shadowsocks were dropped on 2026-11-22, with
  the reasons recorded in §10.2; Trojan was considered and left out there too.
- Multi-vantage probing (§10.7); federation between independent MNs; MN replication in one mesh
  (§10.4); node reputation.
- One machine running both the MN and a node (§10.6).
- Web admin UI; traffic accounting and quotas (§10.5); Telegram bot.
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

v0.5.0 is three of these items, in this order: a WARP that carries UDP (10.1), the profile options and
the envelope for a second kind (10.2), and Hysteria2 as that kind (10.3). Each unblocks the next, and the
first two are worth shipping even if the third slips. The first two shipped in `0.5.0`; the third went in
with them and was finished in `0.5.1`, and what is left of each is named below.

**Numbering.** `0.6.0` is cut and published; the next release is `0.7.0`, and its scope is §10.9 (update
offers) plus §10.10 (the owner's interface) and the three debts §11.15 recorded. The advisory entry
GO-2026-5288 is still in `deploy/vulnerabilities.json` with `must_be_gone_by: 0.7.0`: a stale record, not a
live advisory. Its source, GHSA-9fw6-xgg2-mq9q, lists 2.8.1 as the last affected release and 2.8.2 as the
fix; this build pins 2.14.0 and never enables the sniffing the PoC needs. `vuln.go.dev` never got the fixed
version — the record is `UNREVIEWED` — so `govulncheck` still reports the module and `deploy/release` will
refuse to cut `0.7.0` while the entry is there. Re-check the record first; if it is still stale, moving the
deadline is a deliberate act with its reason in the commit, not a quiet edit.

Everything after them is postponed to a later release: understood and wanted, but nothing in 0.5.0
depends on it. Each postponed item still names the rule that keeps it out, so nobody re-discovers the
reason from scratch.

### 10.1 WARP that carries UDP

Until 0.4.0 `warp.mode` routed TCP through `warp-cli`'s SOCKS5 listener and sent UDP `direct`
(`docs/OPERATIONS.md` §6): proxy mode has no UDP ASSOCIATE, so a UDP flow handed to it never returns and
the first casualty is client DNS. The split kept the nodes usable and gave up the address WARP exists to
hide, for UDP only. Four ways to close that gap were surveyed on 2026-11-16; option A was built in 0.4.0
and proved on a production node (§11), including a DNS query answered through the tunnel.

What keeps it a 0.5.0 item:

- **The `proxy` transport still splits UDP to `direct`.** A node that has to run `warp-cli` — the fallback
  when the WireGuard registration cannot be made — keeps that hole while reporting `warp=on` like any
  other.
- **The transport must stay swappable.** Cloudflare is moving WARP to MASQUE, and the tunnel is a
  registration plus an outbound whose tag the routing rule names. That indirection is the work; option D
  must drop into it without touching routing, the fail-open balancer or the state.
- **Hysteria2 carries UDP through it** (§10.3). The hop is proved for TCP on the lab and, with UDP
  payloads, on production (§11.13): the exit a QUIC client sees is Cloudflare's, and the node's own
  traffic keeps its own address.

| Option | UDP | Host routing | Cost |
|---|---|---|---|
| **A. Xray `wireguard` outbound** with WARP credentials (`noKernelTun: true`) | yes | **none** — gVisor netstack inside the core | needs a WARP registration and a stored private key; one fewer service than today |
| B. `wgcf` + `wg-quick` (what `references/Blitz` does) | yes | `ip rule`, routes, `resolvconf` | contradicts `AGENTS.md` §2.2, rule 1 |
| C. `warp-cli` in TUN mode | yes | full-tunnel capture | contradicts `AGENTS.md` §2.2, rule 1, and `/dev/net/tun` is absent on the LXC nodes |
| D. `usque` (MASQUE, userspace SOCKS5 with TCP and UDP) | yes | none | a third-party service and its netstack, plus a licence to check; MASQUE is where Cloudflare is going |

**Chosen: A, built in 0.4.0 (§11).** Xray has shipped a userspace WireGuard outbound since 1.6.5 and documents the WARP
setup end to end ([xtls guide](https://xtls.github.io/en/document/level-2/warp.html)); `noKernelTun` forces
the in-process gVisor stack, which is exactly the LXC case these nodes are
([outbound docs](https://xtls.github.io/config/outbounds/wireguard.html)). `wgcf generate --xray` prints the
credentials in the shape the outbound wants, including the three `reserved` bytes Cloudflare needs. Because
the tunnel lives in the core, the fail-open balancer keeps working unchanged and the node keeps one
long-running process instead of two. The registration is the only new secret (a WireGuard private key per
node, `0600` in the state directory) and it must never reach a log.

Measured on a production node (then5: one vCPU of a Ryzen 9 7950X3D, 2 GB) on 2026-11-16, 50 MB from
`speed.cloudflare.com`, both paths saturating the single core: kernel path (`direct`) 95–98 MB/s, the
userspace WARP stack (`warp-cli` SOCKS, which terminates TCP in userspace exactly as `noKernelTun` does)
66–69 MB/s. So the userspace data path costs about a third of the throughput at this size. Xray's own
documentation says the same in one line — the system interface "offers relatively higher performance" — and
`noKernelTun` is documented as the LXC-compatible fallback
([docs](https://xtls.github.io/config/outbounds/wireguard.html)). At the ~800 Mbit/s ceiling this node
measures, expecting roughly 500–550 Mbit/s per flow through gVisor is realistic, minus whatever the XHTTP
and VLESS work takes from the same core. That is above what this deployment needs; a node that must carry
more than that is a node whose problem is the vCPU count, not the routing.

The risk to design around: Cloudflare is moving WARP to MASQUE, and WARP+ already reportedly does not work
over WireGuard ([wgcf#459](https://github.com/ViRb3/wgcf/issues/459)). Free WARP works today. The outbound
must therefore stay swappable — the routing rule names a tag, not a protocol — so option D can replace it
without touching anything else.

Acceptance for the remainder: a node on the `proxy` transport either carries UDP through WARP or says in
`warp status` that it cannot; option D can replace the outbound by changing a tag; and with Hysteria2
enabled, a UDP payload from a client exits at Cloudflare's address, while SSH, ACME and the decoy still
leave from the node's own. No `ip rule`/`ip route` change exists on the host.

**Option D is in `0.6.0`** (`warp.transport: usque`): the MASQUE client runs as a pinned, digest-checked
sidecar with its own unit, user and loopback SOCKS5 listener, and its listener carries UDP, so the document
sends both protocols to the WARP hop and the split disappears. The proof it must still collect is the
production pass — the exit address and a UDP flow (DNS) through the tunnel — because every part of this
crosses a network nobody here controls. The pass found that the sidecar alone cannot carry UDP for this
core — `usque` keys a SOCKS5 UDP association on the control connection's own address — so the released form
puts a small loopback relay (`termit-usque-relay.service`) in front of it; §11.15 has the measurement and
the code in question.

The hysteria half of that is proved on production (§11.13): a client through the QUIC transport on then5
exited at `104.28.222.18` with `warp=on`, and the address families the node does not serve were refused
rather than answered with the node's own address. The `proxy` transport cannot carry UDP at all — `warp-cli`
has no UDP association — and since `0.5.2` `warp status` says so instead of reporting a bare `warp=on`, in
text and in `--json` as `udp: not carried` / `"udp": "direct"`. That leaves option D (MASQUE) as the only way
to close the gap for `proxy`, which is the swap the tag indirection exists for.

### 10.2 More protocols and protocol options

V1 is one profile: VLESS over XHTTP `packet-up` inside TLS that nginx terminates, with the parameters
frozen in `internal/proto/profile.go`, one `xray-core` probe worker on MN, and one URI builder plus one
sing-box renderer for the two subscription forms. This is not accidental — the path, mode, fingerprint
and padding were measured, and every component is shaped around that one profile. `DESCRIPTION.md` §3
rules a second protocol out for v1.

The request has two sizes, and the smaller one may be enough:

- **Options inside the profile** — XHTTP mode, ALPN, mux/padding, path shape. A small enumerated set,
  validated at enrollment and versioned; free-form values are refused because a client that cannot parse
  the URI fails silently, and because some values are already known-bad here: an `h2` ALPN cannot cross
  nginx's HTTP/1.1 hop, and a `chrome` hello was frozen on a measured path. These change the URI builder,
  the generator and the prober together.
- **A second protocol** — Hysteria2/TUIC are UDP, need another client on MN and another listener on UV;
  Hysteria2 shipped in `0.5.0` (§10.3) and is the one that answered a real block. MTProto is a different
  product. The profile becomes `{kind, version, params}` with a codec per kind, and that envelope is what a
  new kind plugs into — but only when a kind is actually coming, because a map with no consumer is a schema
  nobody can check.

**Dropped on 2026-11-22, with the reasons**, so nobody re-opens them from scratch:

- **REALITY.** In its honest form — `dest` = the node's own vhost, `serverNames` = its own domain — it is
  externally very nearly our current TLS: the same certificate, the same nginx answers a prober byte for
  byte, the same SNI resolves to the same IP. What it changes is internal: the core takes TCP/443, nginx
  moves to a loopback port, the decoy becomes a relay target instead of the terminator, and a dead nginx
  becomes a dead TLS server, which is a signal of its own. And the variant that is easier to deploy, a
  foreign `dest`, is the one active probing catches — the domain's real address is somewhere else than the
  IP being probed. So: real work, no gain over what the node already does.
- **Shadowsocks.** Its own credential shape (a password, not the per-pair UUID), and no TLS of its own: the
  protocol has a fixed, well-fingerprinted handshake, so a node running it stops passing for a website. The
  derived-password trick used for Hysteria2 could be repeated, but the fingerprint argument is the one that
  decides it.
- **Trojan.** Reasons and the alternative are in the next paragraph.

Constraints, in the order they decide the design:

1. **The credential model is VLESS-shaped.** Pair access is a UUID derived per pair (Shadowsocks and Trojan
   use passwords; Hysteria2 uses a password derived from that UUID, §10.3, so the mesh still expresses "A may
   use B" with what both sides already hold and the epoch does not move). A kind whose secret cannot be
   derived from the UUID would change `DESCRIPTION.md` §6.2, which is a new `credential_epoch` and a
   migration. Only a kind we actually adopt should pay that.
2. **Port 443 is shared with the decoy and ACME.** nginx owns TCP/443 today, and that is now a decision, not
   an accident: the profile rides *behind* nginx, so the decoy, the certificate and the ACME path stay in
   one place, and a QUIC protocol takes UDP/443 without colliding. A kind that wanted the client-facing
   handshake for itself — REALITY was the candidate — would end that, and is dropped.
3. **The prober is one binary.** MN's `xray-core` speaks every xray-core protocol; Hysteria2/TUIC need
   another client there. Decided: MN links hysteria's own client module
   (`github.com/apernet/hysteria/core/v2/client`), so the probe carries the same handshake by construction
   and the server runs no second process. The cost is module graph weight — quic-go and its dependencies
   enter MN's graph — and that is recorded and gated by `govulncheck` like any other dependency (§8). A
   hand-written QUIC prober would mean maintaining a fork of the handshake, which is not a prober but a
   second project. The narrow `Probe(ctx, url)` seam survives, but each transport is a new implementation
   behind it.
4. **Clients differ.** Stock sing-box already cannot read the XHTTP profile; a second protocol widens
   that matrix. The subscription must tell a client what it can parse — profile version, per-form
   rendering, or an explicit refusal — instead of emitting a URI that opens and fails.
5. **No host routing changes, and no new process unless the core cannot speak the transport.** TUN designs
   and anything needing a port forward stay out. A transport the core cannot speak is the one exception,
   under the conditions in `DESCRIPTION.md` §11, invariant 8; every other kind runs inside the core it already has.

- **Trojan.** The one real merit it has is client compatibility: `trojan://` is read by stock sing-box,
  Mihomo and older apps, while the XHTTP profile is not. Everything else argues against it. In Xray it is an
  inbound *with its own TLS*, so it wants TCP/443 exactly as REALITY does — the same rework of the port, the
  certificate path and the decoy — and it does not bring REALITY's active-probing story to pay for it: a
  prober that knows what to send gets a distinguishable answer, and the encapsulated traffic is TLS inside
  TLS, which is a statistical tell of its own. The password could be derived from the pair UUID the way
  Hysteria2's is, so credentials are not the blocker; the shape of the deployment is. **If client
  compatibility ever bites, the cheaper answer is a second rendering of the same profile behind nginx** —
  VLESS over WebSocket on a loopback port with an nginx `location` in front, TLS still nginx's, nothing
  taking 443, and stock sing-box able to read it. That is a profile option, not a new kind.

Order in 0.5.0: the options first (small, no credential change), and the `{kind, version, params}` envelope
built for the kind that ships in that release — Hysteria2 (§10.3), which is a binary of its own and
therefore the expensive one. Never expose raw Xray JSON: a per-kind schema with validation is the only thing
that keeps the generator, parser and prober from drifting apart.

Acceptance: a second kind end to end on the lab — URI build/parse round-trip, generated document golden
plus `xray -test`, an MN probe through it, both subscription forms, a client that does not support it
getting a clear refusal, and the decoy/ACME/direct-traffic invariants unchanged. A profile-version test
must show an old MN and a new UV (and the reverse) refusing the mismatch instead of writing a config
that cannot work.

### 10.3 Hysteria2 as a second transport — done in 0.5.0 and 0.5.1

**Completed.** Phase 1 (the pinned binary, the unit, the generated config, the loopback hop) shipped in
`0.5.0`; phase 2 (the envelope the mesh reads, the links in `uri`/`device`/the page) in `0.5.1`; the
production passes are §11.12 and §11.13. What this section argued is what the code does.

The node's profile is TLS that nginx terminates. Where a network interferes with that shape, the answer
that works today is a QUIC transport: it runs on UDP, so the interference aimed at TCP/443 does not see
it, and its handshake can pass for a browser's.

Hysteria2 is the one to take, on evidence rather than taste: its QUIC handshake is Chrome-shaped out of
the box (S16). Measured on 2026-11-21 against a capture of the same session from Opera 140 (Chromium 157):
1,886 bytes of `ClientHello` from hysteria 2.14.0, 1,935–1,951 from the browser, the same twelve
extensions, GREASE at both ends and no `0xff01`. `disable_chrome_parrot: true` drops it to 1,524 bytes
with the Go extension order — the difference between blending in and announcing a library.

What it costs, measured on a production node's own single vCPU (1 CPU, KVM, BBR, no obfuscation, the
node's Let's Encrypt leaf): 100 MB transfers at 44–52 MB/s (355–415 Mbit/s) across the WAN, 0.45–0.9 s of
CPU per 100 MB — 4–9 % of one core per 100 Mbit/s carried — a 50 MB resident set and about 2 % idle. That
is affordable at this deployment's scale, and it is the price of a transport the core cannot speak.

Three things decide the shape, and each one was learned the hard way:

- **Its egress is its own.** Hysteria dials the destination itself, so by default it bypasses every rule
  the core enforces: WARP, the blocklist, the direct rules, the opt-in BitTorrent block. Measured: on a
  node that refuses BitTorrent, torrent traffic went straight out through hysteria. Policy must live on
  the far side of the tunnel, not beside it — a `socks5` outbound to the core's own loopback `socks`
  inbound, whose routing then classifies the flow exactly as it does today. On the lab, 2026-11-21, 25 of
  25 requests through that hop succeeded at 80–95 MB/s, and the core added about 0.02 s of CPU per 100 MB.
- **It is a second daemon, and the design now says so.** xray-core has no hysteria2, so this is another
  pinned binary and another unit — allowed on the conditions `DESCRIPTION.md` §11, invariant 8 spells out, and pinned
  by version and checksum because its handshake is part of the fingerprint: an upgrade is a fingerprint
  change and is tested as one.
- **Its authentication is a restart.** `auth.userpass` is an inline map in the generated config — there is
  no file to watch and no reload: the server's only signal is shutdown, and its HTTP surface is read-only.
  So the agent regenerates the config and restarts the service whenever the peer set changes. The password
  is derived from the pair's UUID — `base64url(SHA-256("termit/hysteria2/" + uuid))` — so there is no new
  secret to store and no credential epoch to bump: both sides already know the UUID, and the derivation is
  one-way. A restart costs the QUIC transport's sessions and nothing else: with the TLS transport also
  enabled nobody notices, and with it alone clients reconnect in seconds. Enrollments and blocks are rare
  enough to pay that, which is why there is no password oracle (or helper service) listening on the node.
  The trade accepted here: whoever holds the subscription holds both access links to that node.

The certificate is the node's existing leaf: hysteria re-reads it on every handshake, so a renewal needs
no restart. The decoy stays byte-identical: `masquerade: proxy` forwards a non-hysteria visitor to the
vhost, which reproduces nginx's headers and 404s. No Brutal (`congestion.type: bbr` with
`ignoreClientBandwidth: true`): a transport that fights the network for bandwidth is a transport that gets
noticed. No obfuscation and no port hopping — both are fingerprints of their own, and neither is needed
here.

Firewall: TCP/80 and TCP/443 are accepted before the scan meter, and UDP/443 must join them in the same
place. An unlisted UDP port falls through to the meter, and the participant is banned for their own QUIC
traffic. Alt-Svc `h3` on the vhost is then what makes the listener part of the same site instead of a port
found by scanning.

Phases:

0. Spike (done: S16) — the handshake, the decoy, the hop for TCP (25 of 25) and for UDP (an echo through
   it and a real DNS answer).
1. Node (done): the pinned binary (version and digest from the release's `hashes.txt`), its unit under its
   own user `termit-hy`, the generated `auth.userpass` config with a strict read-back and a trial start
   instead of the missing `-test`, the core's loopback inbound, the firewall rule, Alt-Svc and a `doctor`
   line. Verified end to end on the lab: a client over UDP/443 exited through WARP (`104.28.254.16`) while
   the node's own traffic kept its own address, and the core logged the flow on the hop inbound.
2. Profile (done in `0.5.1`): the envelope exists — a node reports `{kind, version}` for what it serves and
   the subscription renders one link per kind (`hy2://` in the list, a `hysteria2` outbound in sing-box), with
   the password derived from each pair's UUID — and every place that hands out a link shows both: `uri`,
   `device add`, `device list`, and the owner's page, which since `0.5.1` files the node's own second link
   with the node rather than with its devices. What is left is the parameters side of the envelope: a kind
   whose parameters vary has no place to say so yet, and the prober has no way to read a kind it does not
   implement.
3. Prober (done): MN links hysteria's client and probes the transport (§10.2, constraint 3). The target
   carries the kind the check goes through, and only a node that serves nothing but QUIC takes the new
   path — every other node keeps the worker binary, which checks what a VLESS client would see. The
   certificate pin is read the same way on both paths, and the server's probe credential joins the node's
   transport user set so the QUIC half can authenticate at all.
4. Production pass (done, §11.12 and §11.13): the transport is network, so it shipped as a pre-release to
   the production nodes first, and what the exit really was — not what the code intended — is recorded
   there.

Acceptance: on the lab, a node with hysteria2 as a second profile — a client through it reaches the
internet with WARP as the egress and the blocklist applied, a torrent is refused, the decoy answers a
plain HTTPS visitor identically on both transports, the URI round-trip and the generated document pass,
an unmodified sing-box refuses the kind instead of writing a config that cannot work, and a killed
hysteria process comes back by itself (the `Restart=always` the core's unit now has). Production pass: the
exit address and the fingerprint of a real handshake from a second machine.

### 10.4 Several MNs in one mesh — postponed

Today one MN is the only authority: it owns the SQLite registry, derives every pair credential from its
`master_key`, probes every node and renders every subscription. A second MN in the same mesh is therefore
a second authority, not a mirror, and the work is an ownership rule plus replication — not a load
balancer in front of one database. It stays out because v1 is explicitly a single coordinating server
(`DESCRIPTION.md` §3 rules out federation between MNs), because the credential derivation in
`DESCRIPTION.md` §6.2 is a contract a second writer must not silently change, and because one MN is
already the most valuable target in the mesh: every extra copy carries the same power. This item is
replication inside one cooperative mesh — several MNs over the same node set. Federation of independent
groups that only share some nodes is a different thing (§5) and is not covered here.

What replicates: node records (`uv_id`, domain, profile, state, failure streak, certificate pin,
agent-key hash), `credential_epoch`, policy that changes pairs, verdicts with their timestamps, and the
schedule. What does not: the `master_key`. It is moved once, out of band, when the mesh is formed, or
replaced by a threshold scheme below — sending it through a relay, even encrypted, makes every relay a
candidate for the whole mesh.

| key model | consequence |
|---|---|
| shared `master_key` | any MN derives any pair; simplest; one compromised MN is the whole mesh, which a single MN already is |
| threshold (2-of-N) | no single MN derives; changes `DESCRIPTION.md` §6.2, so a new `credential_epoch` and a migration; new pairs need two MNs reachable |

That choice comes before any code, because it decides whether replication is additive or a credential
migration.

Authority:

- Every node has exactly one **home MN**, fixed at enrollment. Only the home MN accepts
  enroll/unblock/relocate/policy for that node, and only it writes the row; replicas apply events and
  never write a node they do not home. A node id is global: two homes may not own the same one, a replica
  never creates one, and a conflict is refused rather than merged. Moving a node is an explicit handoff —
  the new home issues a new agent key, the id and the pair credentials survive, the epoch is checked —
  never an implicit failover.
- `credential_epoch` is replicated, and a bump takes effect only when every configured MN has
  acknowledged it. A bump during a partition is a mesh-wide split no node can detect from the inside;
  gating it on quorum is the whole safety argument.

Transport, two paths, one envelope:

- **Direct**: MN↔MN over HTTPS with pinned static keys — no new CA and no new service.
- **Indirect**: through mesh nodes. A UV already has an always-on process and is already trusted to reach
  a management endpoint, so it carries opaque envelopes from its home MN to another MN, with a bounded
  queue, a TTL, an ack and a per-MN quota. The envelope is end-to-end encrypted and signed — `mn_id`,
  `seq`, `schema_version` and payload, X25519 + AEAD, Ed25519 — so a relay reads ciphertext, cannot forge
  or replay (sequence window) and cannot alter it without a signature failure. A relay that drops costs
  liveness only. The node does not become a general relay: only envelopes addressed to configured MNs,
  only below a size cap, removed on ack. Encryption and signatures come from the standard library plus
  the `x/crypto` already in the module, so this adds no dependency.
- Replication needs its own interval, not the daily sync: a state change that takes 24 h to reach the
  other half of the mesh is not replication. On the indirect path that means a short poll or long-poll
  between UV and its home MN — a new always-on network behaviour on the node, and part of the cost.

Replication itself is an append-only event log per MN, applied idempotently by `(mn_id, seq)`; a new or
gapped peer takes a schema-versioned snapshot and resumes. SQLite stays single-writer: do not share the
file and do not add a replication service. Byte-level tools (Litestream, rqlite) solve single-writer
failover and bring a dependency plus merge semantics the state machine does not have.

Decide before implementing: whether replicas may enroll (no — invites and approval stay at the home);
whether a UV may sync to a replica while its home is down (read-only desired state, checks written to
the home only — a stale subscription beats a wrong one); how the admin CLI names "the mesh" when the
local MN is a replica; and what a node does with two reachable homes (there is no split brain because
there is one home, but the failover story must be written down).

Acceptance: two MNs and two UVs. A node enrolled on A appears on B and in a subscription B renders; a
block on A reaches B and the peer's next sync. Cut the direct link and the same event arrives through a
UV relay with the same three results. A replayed envelope changes nothing; a tampered one is rejected and
logged. A partition admits no replica enrollment and no epoch bump; a schema mismatch refuses replication
but keeps serving the last good snapshot. The lab needs a second MN machine — the spare fourth container
is a UV — so the suite grows before this ships.

### 10.5 Traffic limits per node — postponed

Nothing counts bytes today: MN knows a node answers checks and nothing else. VPS plans cap traffic, so a
node can be suspended by its provider without warning — which removes a working exit and, because the
next failed check is up to a day away, leaves peers pointing at a dead endpoint. A cap the node enforces
itself keeps it reachable for SSH, ACME, sync and the decoy, and takes it out of rotation before the
provider does. It is out because `DESCRIPTION.md` §3 lists traffic accounting and quotas as non-goals and
§5.10 keeps rate limiting out; a quota is also a new state in the lifecycle, not a config flag.

The operator has to say what the provider bills; the node cannot know it:

| source | counts | matches the bill | cost |
|---|---|---|---|
| interface, `/sys/class/net/<if>/statistics/{rx,tx}_bytes` | every byte, proxy or not | yes — what the hypervisor or veth counts | none; the interface is config (default: the default route's) |
| Xray `stats` + `policy.system` | proxied bytes, per user if wanted | no — misses TLS/nginx overhead and retransmits | resets on restart; needs a persisted offset |
| provider API | exactly the bill | exactly | one integration per provider, secrets, network calls — out of scope |

Default to enforcing on interface **egress** (`tx_bytes`): proxy responses dominate it and most VPS bill
outbound. `rx+tx`, inbound-only and 95th-percentile plans exist, so the direction is a config value, not
an assumption. A byte through WARP still crosses the interface (the WireGuard tunnel is node-side), so it
is counted once whichever egress it takes. Xray's split is still reported, so the owner can see how much
was the mesh rather than their own downloads.

The awkward parts, which are the actual work:

- **Period.** Billing months start on the provider's day, not the first: a day-of-month plus the resident
  time zone, with a manual reset. A rolling 30-day window needs daily buckets; a calendar month needs one
  counter and a start date.
- **Headroom.** A cap at 100% leaves nothing for the agent, ACME or the owner's SSH, and those are
  exactly what must never be cut. Stop proxying at a configured reserve below the limit; direct traffic
  and the decoy stay up.
- **Enforcement and state.** Peers must stop being handed the node, but the node must not look "failed".
  The honest shape is a new self-reported state (`limited`) — out of peers' configs like `blocked`, and
  automatically `active` when the period resets, unlike `blocked`, which needs an unblock. That is a
  state-machine change, and the reason this is not a small patch.
- **Lag.** Daily sync is too slow to report; enforcement is local, the report rides the next sync, and a
  one-minute local poll is plenty for a multi-GiB cap.
- **Restarts and reboots.** Xray counters reset with the process; persist an offset and notice the reset
  (a counter below the last seen). Interface counters reset on reboot; persist a baseline and add the
  delta, treating a drop as a new baseline rather than negative traffic.

Acceptance: on the lab, a 50 MiB cap stops proxying at the reserve while SSH, the decoy, ACME and the
agent keep working; restarting the core does not lose the reported total; the next period returns the
node to `active` with no operator action; the peer set drops it while limited and gets it back;
`termit-mn status` shows the number and the period. This is a quota, not per-second rate limiting
(§5.10).

### 10.6 The MN and a node on one machine — postponed

Today the two roles are two machines by design: the node's nginx owns `:80` and `:443`, while the MN has
its own TLS listener on `:443` and its ACME challenge listener on `:80` (`docs/OPERATIONS.md` §1). For a
two- or three-person mesh that second box is the most expensive part of the deployment, and the natural
request is one VPS that runs both binaries.

It stays out of v1 because it is a topology change, not a packaging one: the invariants are written
against "one process owns the ports and the certificate" (`DESCRIPTION.md` §11), and nothing here
weakens them — but the ownership rule has to be decided first.

Constraints, in the order they decide the design:

1. **One owner per port.** nginx is the only `:80`/`:443` listener, and it routes by SNI/Host — the
   node's name to the local core, the MN's name to the MN on a loopback port — or the MN moves to its
   own port and every server URL and firewall rule changes. The first keeps the client shape and is the
   recommended direction.
2. **Two certificates, one ACME listener.** Both names need a leaf on the same address, and two ACME
   clients would fight over the challenge. The handler must be shared (one nginx `/.well-known/`
   location, forwarded to whichever role is renewing), while each role keeps renewing and pinning its
   own leaf: the MN pins every node's leaf, the node pins nothing.
3. **The decoy has two owners.** The node's decoy is an nginx tree under `/var/www/termit`; the MN
   serves its own from memory (`internal/mn/server/decoy.go`). Combined, SNI decides which site a
   visitor gets, and that is a fingerprint decision as much as routing: the two sites must not share a
   template seed or look like one deployment serving two names.
4. **The MN still never proxies.** Two systemd units, two service users, two state directories
   (`/var/lib/termit/mn`, `/var/lib/termit/uv`); the node keeps its own core and WARP, and the MN
   process stays traffic-free. No shared daemon.
5. **Firewall and ban ownership.** The node's optional table is `inet termit_uv`, the MN's is
   `inet termit_mn`; combined they must not both claim `input`, the node's `firewall on` must not cut
   the MN's ACME or admin socket, and the ban engine must never ban the MN's own loopback traffic.
6. **The failures become one.** A box that is down or whose subnet is blocked now takes the registry
   with it: one address costs the whole mesh, not one path. That is a policy choice (right for two
   people, wrong for six) and belongs in the install documentation next to the command.
7. **Lifecycle, not just install.** Two installers and two upgrade paths, and `termit-uv uninstall
   --purge` must leave the MN's vhost, certificate and units alone and restore only its own. The MN has
   no uninstall command today (it is done by hand, `docs/RUNBOOK.md` §11), so a combined box needs one
   that leaves the node serving. The backup story becomes two archives and one master key, and the
   resource budget is the sum of `DESCRIPTION.md` §13.1, which a 1 GB node box does not meet.

Acceptance: one box from a clean image — both names served over TLS, `termit-mn status` and
`termit-uv doctor` green, exactly one process per port, a client through the node reaching the internet
while the MN's own traffic never enters the tunnel, a subscription and an admin command working across
the shared challenge listener, `uninstall --purge` of either role leaving the other serving, and the
coupling stated in the guide. The co-located node's probe comes from a second vantage (§10.7): a check
that leaves from the same box is a self-check, not evidence that the node is reachable from outside.

### 10.7 A second vantage for the MN's own node — postponed

Today the home MN is the only observer: its probe decides a node's state, and its own vantage is assumed
to be the internet's. Two cases that this plan now makes common break that assumption. A node co-located
with its MN (§10.6) can only be reached through hairpin NAT or loopback, so the check stops measuring
external reachability. An MN whose path to one node is blocked, while every other node is fine, has no
way to say "this is my vantage, not the node" — and by `DESCRIPTION.md` §7.2 a timeout is a verdict, so
the node is blocked.

The fix is to let another MN, on a different network, perform the probe and hand the verdict back: a
second vantage. It is deferred with the rest of multi-vantage probing (§5), but its wire shape belongs in
the plan now, because §10.6 cannot honestly claim external validation without it.

It is a delegation, not a federation. A node keeps one home MN (§10.4) and only the home MN decides and
writes state; a peer observes and answers, it never writes a row.

**Verdict rule**, generalising the retry aggregation `runCycle` already does ("failed only if every
attempt concluded"):

- an `ok` from any vantage is `ok`;
- `failed` only when every vantage that formed a verdict says failed;
- no vantage could form one — peer down, its DNS broken, its prober unavailable — is `unknown`, and
  `unknown` changes nothing (invariant 2).

Shape:

- **Config**: `prober.peers: [{name, url, key, delegate: [domains]}]`. The list is explicit: an automatic
  "delegate my own domain" rule would hard-code the topology, and §10.6 is not the only reason to
  delegate.
- **Protocol**: one call, `POST {prefix}/peer/probe` with `{uv_id, target{domain, port, sni, path, mode,
  uuid, pin}}`, answered with the `Result` the local prober would have produced (`ok`, class, latency,
  timestamp). The peer key is a bearer credential like an agent key: random, stored as a digest,
  rotatable, TLS pinned to the peer. A result older than one cycle is not a verdict.
- **Code**: `internal/mn/probe/peer.go` implementing the existing `Prober` seam, so the transition
  table, the grace streak and the `unknown` rule are untouched; `cmd/mn/serve.go` picks the local worker
  or the peer per node.
- **Freshness and staleness**: the peer's answer is an observation with a timestamp, not a state. The
  format is a list even while one vantage is used, so N vantages need no rework.

Decisions that come before any code:

1. **How many negatives block.** With one delegated vantage, a compromised or simply misconfigured peer
   can block any node it can reach. Options: accept it (the mesh is cooperative, as with a hostile UV),
   require unanimity once there are two or more vantages, or let a single peer's failure move a node only
   to `unavailable` while `blocked` needs the node's own self-check to fail as well. This is the same
   trust question as §10.4's shared master key and must be answered with it.
2. **What the peer learns.** A probe target is the probe UUID, the secret path and the certificate pin —
   the ability to connect to a node it does not own. Under a shared master key (§10.4) a peer derives
   pair credentials anyway, so this is not new power; without replication it is, and must be stated.
   Whether the probe UUID becomes rotatable (today `uv reissue` rotates only the agent key) is open.
3. **Who picks the peer.** Explicit selection is honest; automatic selection from the list is convenient
   and hides the topology. A dead peer leaves the node unverified but `active` — correct by invariant 2,
   and invisible to the operator unless `termit-mn status` says "no external vantage" per node.
4. **Visibility of the vantage.** Every served verdict should name the vantage that produced it, in
   `uv show` and in the metrics, or a disagreement between two MNs cannot be investigated after the fact.

Acceptance, on a lab with two MNs on different networks: a node co-located with its home MN is checked
only by the peer and reaches `active` on the peer's `ok`; stopping the peer leaves the node `active` and
`status` reports no vantage; a peer `failed` while the node's self-check is `ok` follows the rule chosen
in decision 1, recorded in the guide; a stale result never moves the node; the state-machine tests are
unchanged, because no transition was added.

### 10.8 A QR code beside every client link — shipped in 0.5.2

**Shipped in `0.5.2`**, with the decisions this section argues for settled this way: the code is drawn by
the node's own Go code (`rsc.io/qr`, BSD-3 and dependency-free, rather than ~300 lines of encoder nobody
could verify without a phone), one `<svg>` per link inside a `<details>` so the page keeps its size, level L,
the quiet zone in the viewBox, and a code on every row — the node's own, each local device's, each peer's.
The full link is encoded rather than a shortened form, because the code has to be exactly what the copy
button copies; at level L a 570-byte mesh URI is an 81×81 grid, which a 320-pixel-wide element shows at about
four pixels a module. What follows is the reasoning that led there.

Every row of the owner's page (`termit-uv options`) carries a copy button, and copying is the wrong gesture
for the device the links exist for: a phone has no shared clipboard with the node, so the owner either types
a 600-character URI or sends it through a messenger. A QR next to the link turns that into one camera action,
and it is the same secret, only drawn — the page is behind the node's unguessable token, so nothing new is
exposed, though a screenshot of the screen becomes scannable by whoever sees it, which the docs will say.

The obvious implementation was forbidden by a rule this repository already has: no `img src="https://api.
qrserver.com/…"` and no CDN copy of a QR library, because the page would (a) send the node's credentials to
a third party and (b) reference an external host — the decoy's whole point is that a page which phones home
is a fingerprint (`AGENTS.md` §5, "Decoy site"). So the encoder is ours, in one of two shapes:

| shape | cost | notes |
|---|---|---|
| Go encoder, inline SVG per link | byte mode + Reed–Solomon, versions 1–40, ~300 lines with vectors | the page keeps the only JS it has (copy); testable in Go; no vendored JavaScript to audit |
| a QR library embedded in the page | ~15 KB of MIT JavaScript, with attribution | smaller Go change, a second language to keep in step with the page's CSP and with the options test that fails a page referencing an external host (`internal/uv/options/options_test.go`) |

The Go encoder is the recommendation: it is the shape the rest of the page already uses (the page is
`html/template` over a Go value, so a `QR string` field per link renders as an `<svg>` with no script at
all), and a QR is arithmetic, which tests well — the standard vectors for "HELLO WORLD" and a short URL, plus
a round-trip through an independent decoder if one can be kept out of the shipped binary.

What decides the real design, before any code:

- **Density.** The node's own `vless://` link is a few hundred characters before fragmentation: XHTTP
  extras, `mode=packet-up`, the path, `fragment=…`, `sb.tls.*`. That is a large, dense code — readable from a
  still screen, unreliable from a moving one, and hopeless in a small inset. `hy2://` is a tenth of it. Two
  ways out, and the choice must be made explicitly rather than discovered on a phone: draw the full link and
  accept that the scan needs a steady hand, or draw a *minimal* link (scheme, credential, host, `type`,
  `security`, `sni`, path — the parameters that connect) and leave `fragment` and `sb.tls.*` to the copy
  button. The second is more useful and needs a size on the QR that a screen can show.
- **Which rows.** All of them: the node's own two, every local device's two, and each peer's. A peer's link
  is the one a new device most often needs, and it arrives with the subscription rather than from `device`.
- **Where it appears.** Inline under the row (a second line in the same `<li>`), not a modal: the page has no
  modal machinery and a phone cannot easily close one. One code visible at a time keeps the page readable.
- **Error correction and size.** Level M with a module size that fits the column; the quiet zone is part of
  the SVG's viewBox, because a code that touches the row's border does not scan.
- **The terminal.** `termit-uv uri --qr` printing half-block characters is a nice-to-have, not part of this
  item: the phone case needs the page, and a terminal QR is a second renderer to keep right.

A `0.5.x` patch carried it after all: `0.6.0` was reserved for a hysteria bump that never had anything to
bump (GO-2026-5288 turned out to be a stale record, §10 intro), and a page nobody can scan is not worth
waiting for that.

Acceptance: on a phone, the camera opens a row's code and the client imports the link; the page references
no external host and still passes the existing test that fails a page referencing one (`internal/uv/options/options_test.go`); a Go test round-trips the encoder
against the published vectors and fails if a link with a control character or an over-long URI is passed
without a clear error; `go test` grows no dependency.

Documents when this ships: `docs/guide/ru/using-uv.md` and its English counterpart (the page description),
`docs/OPERATIONS.md` §8 for the owner's page, `--help` for the optional flag, and a changelog entry that
names the screenshot consequence.
### 10.9 Update offers: the node follows the server, the server follows GitHub

Two problems that look alike and are not, deliberately kept as two code paths.

**The node.** The server is the mesh's authority and already knows which build every node runs — `agent_version`
in the sync request, printed by `termit-mn uv list`. The reverse half shipped in `0.6.0`: `server_version` in
the sync answer, remembered in the node's state beside the last sync and printed by `termit-uv status`. `server_version` joins the sync answer (`omitempty`, and the node's decoder is lenient, so an
old node ignores it and an old server simply sends nothing), the node stores it beside the last sync, and the
menu, on launch, compares it with the running build. The policy is the owner's, and both halves have a reason
beyond caution: **offer the server's version and never "latest"** — the server is what the mesh has been proved
against, and a node that outruns its server is a node whose links that server may not know how to render; and
**never a prerelease** — a `p` suffix names a build that exists to be proved on one box, and offering it in a
menu would turn the prompt into noise. Equal or older is not an update, so the prompt stays silent.

The *execution* is where the care is, because it replaces the binary the process is running from. The shape:
fetch the release's `SHA256SUMS` and the asset for the architecture, check the digest before anything else,
swap with a rename in the same directory while keeping the previous binary, run `termit-uv init --no-enroll`
with the new one, and put the old one back if that fails. That is `termit-uv upgrade --to <version>`, and the
menu entry wraps it; it is not `curl | bash` (§12) — the digest is from the release itself and is checked
before the swap, and a convergence that fails rolls back. An unattended node never updates itself: an updater
on a timer is a remote-code path, and the prompt exists so a human is in the loop.

**The server.** An MN cannot learn "the newest release" from itself; it has to ask GitHub, which would make it
the one place in the project that talks to a host it does not otherwise need. So the offer is explicit:
`termit-mn update --check` prints the newest release above the running build, `--apply` runs the same verified
download and restart, and the menu offers it. Its policy is the mirror of the node's — **it may offer what
GitHub says is newest, prereleases excluded**, because there is no higher authority to follow. The GitHub call
must tolerate no network and rate limiting (a reminder, not a check), and its result must never enter `status`
or `doctor`: a server that cannot reach GitHub is not an unhealthy server.

Open when this is implemented: whether the menu line is the only door (for the node, yes — the menu is the
interactive door and the prompt is the point), and whether `upgrade` should accept a version the server never
offered (yes, with the operator naming it; the prompt is the convenience, not the permission).

### 10.10 The owner's interface — 0.7.0's UI/UX work

The commands work; this is about the minutes an owner spends on them, and about what a reader can answer
without a second command. Each item names the thing the reader sees, and each is independent, so the list
can be cut anywhere.

- **An invite that carries its own instructions.** `termit-mn invite create` prints the ready-to-send
  block: the `sh uv.sh --mn <url> --invite <code>` line with the MN URL and its API prefix already filled
  in, and the note as the person's name. Today the operator assembles that line by hand out of three
  commands — the first-day papercut of the whole mesh. (The terminal QR this item first imagined is
  dropped: a node is installed by typing a command, not by scanning one.)
- **The page the owner sends.** `termit-uv options` gains what the reader needs next to each link: a copy
  button (inline script; the no-external-host rule is about the decoy, not this page), a "voice and video"
  marker on the `(QUIC)` row — that is the link that carries UDP — and the node's own egress line
  (`warp: usque, via …`) so the page answers "what does this node do" without `warp status`.
- **One screen for "what does my node do?"** The menu's status screen becomes a card: domain, build, the
  server's build with a marker when they differ, transports served, egress, peers grouped by transport,
  last sync, drift. It is the screen an owner screenshots when asking for help, so it should not need a
  second one.
- **`doctor` grouped, with a verdict.** Host, node and tunnel as three groups with one summary line each,
  and a closing line that says ok / warn / unknown. Today it is one flat list of twenty lines that the
  reader has to scan to the end to answer "is anything wrong".
- **`uv show` as the operator's answer.** `termit-mn uv show <node>` gains the last probe's detail, the
  transports the node serves, when it is next checked, and its subscription URL — so "check this person's
  node" and "send them the link" are one command instead of three.
- **Completion for both binaries.** `termit-uv completion bash|zsh` and `termit-mn completion …`, generated
  from the same flag definitions the parser uses, so it cannot drift from the commands.

Open when implemented: whether the page's copy button is worth inline script at all (clipboard APIs are
gated, and a plain selectable link is already copyable), and whether completion ships in the binaries or in
the packaging — the binaries are the only thing we control on both.

### 10.11 0.7.0: the scope and the order it lands in

`0.7.0` is §10.9 (both halves), §10.10 (the six items above), three items below that came out of the
planning discussion, and the three debts §11.15 recorded (already on `dev`). Everything except §10.9 is
local: the menu, the wizard and the printed reports. §10.9 is the release's one network-facing piece, and
the only part a production pass has to see — the upgrade path itself.

**State on 2026-11-22.** The menu tree, the node picker, the wizard's QUIC question, the invite block, the
invite policy, the grouped doctor, the node card, the page's egress line and QUIC note, `termit-uv upgrade`
and `termit-mn update` are on `dev`, and the two update paths have been run on the lab with locally built
binaries (`v0.7.0`, commit-stamped, `pscp`'d into `/usr/bin`): the menu and its card on the node, a real
`termit-uv upgrade --to v0.6.0` (download, digest, swap, converge) and a real `termit-mn update --apply`
(the server came back on `0.6.0` and was put back). Completion is deferred out of this release: both
binaries declare their flags inside each command, so there is no registry to generate a script from —
building one is a 0.8.0 item, not a polish task. What is left here is the production pass.

**The lab found two defects the tests could not.** The first upgrade replaced the binary, failed the
converge step and rolled back — `init` ran without `--config`, so an installed node looked like a fresh
machine asking for its domain; `runConverge` now carries the path, and the rollback that followed is the
reason the node kept serving. The server's update asked `api.github.com`, which answered `403` from the
lab — the same block that makes `uv.sh` read the `releases/latest` redirect instead; `release.Newest`
now follows that redirect, which needs no token, no quota and no host that a node cannot reach.

**Two menu items that join §10.10.**

- **A tree instead of a list.** Both menus grow one level: at most ten choices per screen, grouped by
  object. Node: Состояние (`status`, `doctor`), Сеть (`net`, `warp`, `transport`), Клиенты (`options`,
  `uri`, `devices`), Меш (`sync`, `drift`, `enroll`, `unblock`), Защита (`firewall`, `bans`, `guard`),
  Сайт (`certificate`, `decoy`), Узел (`install`, `wizard`, `migrate`, `uninstall`), Помощь. Server:
  Состояние (`status`, `check`, `guard`), Узлы, Приглашения, Клиенты (`sub`, `policy`), Защита
  (`firewall`, `bans`), Обслуживание (`backup`, `restore`, `admin`), Помощь. `menuEntry.children`, a level
  stack with the breadcrumb in the title, `0 ←` on every level below the first, `q` from anywhere. The
  four tables that were fake sub-menus (devices, warp, firewall, bans) become real ones and move into
  `internal/menuplan`, where firewall and bans already live. A test walks the tree: ≤10 per level, and the
  leaves are exactly the commands the menu offered before, so nothing is lost in the move.
- **The QUIC question in the wizard.** Between WARP and `harden`, one yes/no: serve Hysteria2 on UDP/443
  beside the TLS profile. The answer is stored in the draft, becomes `--hysteria` in `wizardInitArgs`, and
  appears in the summary. Default stays "no", so a TLS-only node is what pressing Enter gives.
- **A picker instead of an id.** The server's menu draws the mesh and takes a domain, an id, or a unique
  part of a domain (`then5` finds `then5.bookinet.ru`); `0` cancels, and `ui.MenuFree` is what lets a
  picker accept a value its list does not contain. When the server cannot be reached the question says so
  and takes a typed domain. An empty answer to a question that names something already in existence — a
  node, an invite code — cancels, and the label says so; a question that sets a new value still asks
  again.

**A server item of its own: the invite policy.** `enrollment.mode` (`invite`, `invite+approval`, `open`)
is read at startup, so today changing it means editing YAML by hand and restarting. It gets a command and a
menu line: `termit-mn invite policy` shows the mode and `--mode …` writes it, editing the YAML node tree so
the operator's comments survive, validating the result with the same loader before the file is replaced,
and saying plainly that the running server keeps the old mode until it restarts (`systemctl restart
termit-mn`). `open` needs `--force`: at that mode a leaked invite is an account.

**§10.9 in the same release.** Node: `termit-uv upgrade [--to v<version>] [--check]` downloads the release
binary and `SHA256SUMS` from GitHub, verifies the digest, writes beside the running binary and renames over
it, keeps the previous file, then runs the new binary with `init --no-enroll` to converge, and restores the
previous file if that fails. Without `--to` it installs the version the MN reports as its own (`server_version`,
which sync already carries) — never `latest`, never a `p*` build. The menu's Узел group offers it only when
the server's build is a plain release and differs from the node's. Server: `termit-mn update --check/--apply`
does the same against the newest release on GitHub; neither result appears in `status` or `doctor`.

**Order.** (1) The menu tree, both binaries. (2) The wizard's QUIC question. (3) The §10.10 items in the
order they were written. (4) The invite policy. (5) The upgrade and the update. (6) `docs/guide/menu.md`
and its Russian mirror, `CHANGELOG` and the plan's §11.15-style record of the pass.

**Proving it without a pre-release.** The lab runs the tree built locally: `deploy/release --version
v0.7.0` produces the same layout CI does (binaries, `uv.sh`, `SHA256SUMS`), its output goes onto the lab
machines with `pscp`, and `go test -tags=lab -count=1 ./test/lab -run '…'` runs everything except the warp
scenarios. The lab is reinstalled from scratch first, so the run proves a first install and the menu that
follows it; then §10.9 is proved against the lab as well — `upgrade --to v0.6.0` (a real download,
digest check and swap), a corrupted `SHA256SUMS` to exercise the rollback, and a downgrade back. Nothing is
published as `v0.7.0p<n>`: the first pre-release is cut when the tree is ready for the production pass, and
not before.

**What the release must show.** A fresh install through the menu (piped answers, not a hand-driven
terminal), every group reachable in two keystrokes, `doctor` with a verdict line, an invite block that
installs a node without the operator assembling a command, the wizard producing a node that serves both
transports, the policy command writing a mode the next start obeys, an upgrade that keeps the node
serving, and a deliberate upgrade failure that leaves the previous binary in place.

## 11. Planned: WARP over WireGuard inside the core (0.5.0)

The one thing `0.3.2` gave up is that UDP leaves from the node's address: `warp-cli`'s proxy mode cannot
carry it, so §10.1's option A replaces the proxy with a WireGuard outbound inside Xray. This is the plan;
the code comes later. Everything below is deliberately testable, and every item names the observable that
says it is done.

### 11.1 What changes for an operator

Nothing in the interface they know. `warp.mode` keeps its three values, `warp.fail_open` keeps its
meaning, `termit-uv warp status` keeps its shape. Two things become true that are not true today: UDP
through the tunnel exits at Cloudflare, and the host no longer runs `cloudflare-warp` at all — no package,
no second daemon, no SOCKS port.

A new optional key selects the transport while the old path is still supported:

```yaml
warp:
  mode: all
  fail_open: true
  transport: wireguard     # wireguard (default from 0.4.0) | proxy (what 0.3.x does)
```

A configuration written before this key exists behaves as `proxy` and is not migrated silently; flipping
it is `termit-uv init --warp-transport wireguard`, which registers, writes the identity, regenerates the
core config and applies it. The reverse flag returns the node to the proxy path.

### 11.2 The identity: one secret, written once

WARP over WireGuard needs a registration: a Curve25519 private key, the address Cloudflare assigns
(`172.16.0.2/32` and an IPv6), the peer public key, the endpoint, and the three `reserved` bytes without
which the handshake is dropped. That is a credential and it belongs where the node's other credentials
live:

- `/var/lib/termit/uv/warp.yaml`, `0600`, written atomically, never regenerated once it exists (the node
  would change its WARP identity on every `init` otherwise).
- Never in a log line, an error string, `termit-uv status`, `doctor` or the page. `status` may print the
  endpoint and the device id; the private key and the reserved bytes never leave the file.
- Removed by `termit-uv uninstall` together with the rest of the state, and only by an explicit flag
  otherwise — a lost identity is a new device on Cloudflare's side, not a disaster.

Two ways to obtain it, and the plan takes the second:

1. Shell out to `wgcf` (MIT) and parse what it prints. Fast to write, but it adds a third-party binary to
   the install path, and its output format is not ours to promise.
2. Speak Cloudflare's registration API from Go: generate a key pair, `POST /reg` with the public key and
   the WARP client's shape, read back `config.peers[0]` and the reserved bytes. It is a handful of HTTP
   calls behind an interface, testable against `httptest`, and leaves the node with no new binary. The
   existing `internal/mn/secret` sealing helpers are the wrong shape here (the key must be usable, not
   sealed), so it is a plain `0600` file, like the peer secrets already in `state.json`.

### 11.3 The generated outbound

```json
{ "tag": "warp", "protocol": "wireguard",
  "settings": {
    "secretKey": "…", "address": ["172.16.0.2/32", "2606:4700:110:…/128"],
    "peers": [{ "publicKey": "bmXOC+…", "endpoint": "162.159.192.1:2408",
                "allowedIPs": ["0.0.0.0/0", "::/0"], "reserved": [240, 25, 146] }],
    "mtu": 1280, "noKernelTun": true } }
```

`noKernelTun` is not a tuning knob here: it is what keeps the node out of host routing (invariant 1 in `AGENTS.md` §2.2)
and what makes the outbound work in the LXC containers the lab runs on. The measured cost — two thirds of
the kernel path's throughput on a one-vCPU node (§10.1) — is accepted and recorded.

With the tunnel in the core, the routing rule goes back to a single catch-all:

```json
{ "type": "field", "network": "tcp,udp", "balancerTag": "warp-egress" }
```

The `0.3.2` split (UDP `direct`) survives only under `transport: proxy`, where it is still the correct
answer, and the golden tests cover both documents.

### 11.4 Work breakdown

| # | Task | Done when |
|---|---|---|
| 1 | `internal/uv/warp` registration client behind an interface: key pair, `POST /reg`, parse peer/reserved, error classification | unit tests against a fake API; no network in `go test` |
| 2 | `warp.yaml` in the state directory: atomic write, `0600`, load/validate, idempotent, survives `init` twice | state test: two runs, one identity, unchanged bytes |
| 3 | Config key `warp.transport` with `proxy` as the legacy default and `wireguard` from fresh installs | config tests: absent key = proxy; validation refuses an unknown value |
| 4 | Generator: wireguard outbound + single catch-all when `transport: wireguard`; the `0.3.2` split when `proxy` | golden documents for both, plus `xray -test` in the lab |
| 5 | CLI: `termit-uv init --warp-transport`, `warp status` shows transport and endpoint, `warp rotate` re-registers | `warp status` never prints a key; a redaction test greps the output |
| 6 | Install/uninstall: skip `cloudflare-warp` when the transport is wireguard, remove the outbound and keep the unit when switching back | lab: proxy → wireguard → proxy leaves no stale package, unit or rule |
| 7 | Docs: `OPERATIONS.md` §6, `DESCRIPTION.md` §Egress, the guide, `CHANGELOG` for `0.4.0`; §10.1 moves to "done" | a reader can tell which transport their node runs without reading the config |
| 8 | Transport variants and the migration between them: absent key loads as `proxy`, `init --warp-transport` writes and applies `wireguard`, and switching back leaves nothing behind | a table-driven test loads each variant, flips it through the CLI path and asserts the resulting config, the generated document and the on-disk state; an old config gains no key it did not have, and a switch does not orphan the previous transport's artefacts (package, outbound, identity file, SOCKS port) |

### 11.5 Acceptance

Automated: generator goldens, an `xray -test` on the generated document in the lab, the identity surviving
`init` twice, redaction of the key in every command's output, and a switch back to `proxy` that restores
the old document byte for byte.

Manual, because it is the whole point: a client resolves a name through the tunnel and the answer comes
back; `curl https://www.cloudflare.com/cdn-cgi/trace` through the node reports `warp=on`; and the node's
own SSH, ACME and decoy traffic still leaves from its own address — checked from the server side by
watching the exit address of a request the node itself makes.

### 11.6 Risks

- **Cloudflare may stop accepting WireGuard registrations.** WARP+ reportedly already does not work over
  WireGuard ([wgcf#459](https://github.com/ViRb3/wgcf/issues/459)). The routing rule names a tag, so a
  MASQUE transport (§10.1 option D) can replace the outbound without touching anything else; keep the
  switch a config value, not a compile-time choice.
- **A WARP identity is per node and cannot be shared.** Two nodes behind one registration look like one
  device; the registration must be per node and stored, not derived.
- **Fail-open must keep working over a tunnel that is down.** The observatory probes through the tag, which
  is transport-agnostic; the test suite must cover the balancer pointing at a wireguard outbound.
- **The package removal is a one-way door on a running node.** Switching back to `proxy` reinstalls
  `cloudflare-warp`; the lab proves the round trip before a node in production takes it.

### 11.7 Implemented, 2026-11-16

Tasks 1–8 are in `dev`: the registration client, the identity file, `warp.transport`, the generator for
both documents, `init --warp-transport` / `warp rotate` / a `warp status` that probes the exit,
`internal/uv/tunnelprobe` for that probe, and the transport-switch test. The preflight no longer warns
about a missing `/dev/net/tun` on the wireguard transport, because the userspace tunnel does not need one.

Verified on the lab node (`termit-uv1`, an unprivileged container, no `/dev/net/tun`) against the real
Cloudflare API:

- registration succeeded — device `40d665d2…`, peer `engage.cloudflareclient.com:2408`, three reserved
  bytes, the tunnel addresses, the identity on disk as `0600`;
- `xray -test` accepted the generated document, and the route collapsed to one `tcp,udp` rule through the
  balancer with a `wireguard` outbound whose `noKernelTun` is true;
- `termit-uv warp status` reported `egress: on` with a `2a09:bac5:…` address — a WARP address, fetched
  through the node's own tunnel;
- **UDP through the tunnel answered**: a DNS query to `1.1.1.1:53` sent over SOCKS5 UDP through the node
  to that tunnel came back with two answers. This is the thing 0.3.2 could not do.

The install itself ends with the step that would have caught the 0.3.2 problem on the node instead of on a
phone: `warp-egress` runs after the core is started, and when WARP is on and the node is registered it
fetches Cloudflare's trace through the tunnel and reads the address. A wrong answer — the tunnel is
configured but the exit is the node's own — is a warning naming that address, not a failed install, and
`warp-egress ok` on the lab node is the same evidence a client would produce.

Still open before a release: the acceptance-lab run, and the production pass that a `0.4.0` release needs.

#### The acceptance lab, 2026-11-17

`v0.4.0p1` reached the lab the way a tester gets it — the MN by hand against `SHA256SUMS`, the three nodes
with `uv.sh --version v0.4.0p1 --no-enroll`. All four came up on the release binary and the upgrade
converged the units, the certificate and the Xray document without touching identity or path.
`termit-uv1`, which carries `warp.transport: wireguard`, ended its `init` with `warp-egress ok`.

That run also found the defect this milestone could not ship with: **a sync rewrote a wireguard node as
the proxy sidecar**. The agent's reconciler built the WARP half of the document itself, without the
transport and without the registration, so the first sync after the install pointed the balancer at a
SOCKS port nothing listens on; fail-open then took the traffic and the node exited directly — the failure
`warp-egress` exists to catch, arriving a day late instead of at install time. `warp mode all` had the
same shape: it refused on a wireguard node for the missing `/dev/net/tun`, which that transport does not
need.

Fixed in `d66f947`: `install.WarpSpecFor` is the only place that spec is built and the sync goes through
it, and `warp mode` skips the sidecar — and its tun check — on the wireguard transport, registering the
node when it has never been registered. `TestWarpSpecFor` covers the four combinations and
`TestApplierKeepsTheWireGuardTunnel` pins the reconciler.

`v0.4.0p2` (`7ea5e23`) re-ran the suite: **17 pass, 2 skip** (`TestDrillDiskFull` and
`TestDrillClockSkew`, both limits of an unprivileged container), 346 s. On the lab node, `sync` keeps the
wireguard outbound in the document, `warp mode off` and `all` round-trip it, and `init` ends with
`warp-egress ok`.

Still open before a release: the production pass on the two nodes.

### 11.8 Size

Two to three days of work: the registration client and the state file are the bulk of it, the generator
change is small, and the lab pass is a half-day. It ships as `0.4.0` from a `release/v0.4.0` branch, with
its own production pass recorded the way `M5-8` was.
### 11.9 Production pass, 2026-11-17

Both nodes took `v0.4.0` with `uv.sh --version v0.4.0`: the upgrade converged the units, the certificate
and the document, and each `init` ended with `warp-egress`, the step this milestone added.

- **One node runs the in-core tunnel.** `init --warp-transport wireguard` registered a fresh device,
  wrote `transport: wireguard`, and `termit-uv warp status` answered `egress: on` with a `2a09:bac1:…`
  address fetched through the tunnel — UDP now rides WARP on a production node, which is the point of
  the milestone.
- **The other cannot.** The same `init` registered and applied the route, and `warp-egress` then
  reported the exit as the node's own address: that network drops the WireGuard handshake to
  `engage.cloudflareclient.com:2408`, while `warp-cli`'s HTTPS tunnel works there. The step turned a
  silent privacy regression into a named warning, and the node went back with
  `init --warp-transport proxy` — `warp status` reported `on` again with a `2a09:bac5:…` address, so the
  round trip is exercised on a live node.

That is the answer to one of the risks above: the transport is a per-network choice, and `0.4.0` gives
the operator the check and the switch instead of a default that lies.

#### The endpoint, later the same day

That pass found a second defect, and it was the one that mattered: the node written off as "cannot reach
WARP" was fine. Cloudflare names its endpoint for both address families and the core resolves that name
itself; on a host without a global IPv6 it took the AAAA record, the handshake died with
`sendto: network is unreachable`, the observatory marked the outbound dead and the balancer fell back to
`direct` — a node that looked healthy while exiting from its own address. The same identity and the same
document, run by hand against an IPv4 endpoint, handshook in half a second.

`8c15d16` resolves the endpoint once, at registration, and stores the address. `v0.4.0p4` carries it: both
nodes rotated onto `162.159.192.1:2408` and reported `egress: on` with a Cloudflare address — including
the one the first pass had written off.

The lesson is a rule in `AGENTS.md` §9 now: a release that touches the network is proved on the
production nodes as a pre-release, and the final tag waits for that.

#### The family, the same day

`0.4.0` also lets the owner choose which family leaves through WARP: `ipv4` by default, because
Cloudflare's IPv6 egress is what large services mark as unsafe, and a provider's own IPv6 is not the
node's to fix. `warp.family` keeps the tunnel address of that family, gives the peer its default route
and sends the other family direct — the way it went before WARP, and named in the document rather than
left to the trailing rule.

`v0.4.0p6` on both production nodes: `family: ipv4`, `172.16.0.2/32` and `0.0.0.0/0` in the core's
document, `::/0` named direct, and `warp status` answering `egress: on` with `104.28.222.18` and
`104.28.193.183` — Cloudflare's IPv4, where the same nodes reported `2a09:…` a few hours earlier.

**Changed for 0.5.0, 2026-11-21: the untaken family is refused, not sent direct.** Routing it direct put
the node's own address on the wire for every IPv6 destination — the identifier the tunnel exists to hide,
measured as `ipv6.icanhazip.com` answering with a node's `2a12:…` address. The rule now sends those
destinations to `block`. A client gets a connection that fails and can say so, instead of an address it
never asked for; the price is that a service reachable only over the untaken family no longer works
through the node, which with the IPv4 default means IPv6-only services. `OPERATIONS.md` §6 states the
behaviour and its limit — the rules match addresses, not names.

### 11.10 Production pass, 0.4.1, 2026-11-17

`0.4.1` is a small release — the wizard's install branch, the agent's write access to the page it
re-renders, two installers that no longer demand a version, and the pages checked against a clean box —
but it changes the network half of the agent, so it went out the same way `0.4.0` did: as a pre-release on
the production mesh, with the final tag waiting for what the mesh answered.

**The pre-release was built, and it caught a defect in the pipeline first.** `workflow_dispatch` on
`release/v0.4.1` computed the pre-release number from a tag pattern with one `v` too many
(`vv0.4.1p*`), found no build, and published over `v0.4.1p1`: the tag stayed on its commit and the assets
were rebuilt from the branch tip. Every earlier pre-release had been published by pushing a tag, which runs
the other job, so the counter had never been exercised. `37b84fb` fixes the pattern and refuses a version
whose tag already exists before anything is built or uploaded; `v0.4.1p1` was rebuilt from its own tag and
the assets replaced, and `v0.4.1p5` is the build this record is about.

**Both nodes took `v0.4.1p5` with `sh uv.sh --version v0.4.1p5 --no-enroll`.** then5 came from `p4`, then4
from `0.4.0p6`. On both, `init` reported eleven steps `present` and rewrote only the agent unit — the
generators did not change between those builds — so `xray.json` and `config.yaml` kept their digests and
`termit-xray` was never restarted (then5 still on its 08:01:37 EET start, then4 on 06:01:54 UTC). The
rewritten unit is the one change that matters: `ReadWritePaths` now names the site tree, and the page the
agent re-renders after a sync can finally be written.

That claim was checked rather than assumed: on each node the same render (`termit-uv options --quiet`) was
run inside the unit's own sandbox properties — `ProtectSystem=strict`, the unit's `ReadWritePaths`,
`ProtectHome=yes` — and it exited 0 where, on the lab, the same command with the previous path list failed
with the production's exact `read-only file system`. `termit-uv doctor` reports 19 ok on both nodes and
TLS answers `200` on both names.

**The server went from `0.3.2` to `v0.4.1p5`.** The database schema did not move (`SchemaVersion` was 5 in
both trees), so the upgrade is reversible with the previous binary, and the unit shipped with the release
is byte-identical to the installed one — `daemon-reload` was not needed. The order was `stop`, replace the
binary, `start`; the service came back on `:443` with the same certificate, `termit-mn status` lists both
nodes `active` with `failed cycles: 0`, a probe of one node returned `check_ok` with `changed: false`, and
the API answers as it should from outside (`401 unauthorized` on `agent/sync`, a `400 url_invalid` problem
document on `enroll`). The recorded backup holds the old binary, the unit, the configuration, the master
key, the admin token and a consistent database snapshot.

**Node and server talk again on the new builds.** `termit-uv sync` on both nodes answered `already in
sync: 3 client(s), 1 peer(s)` without touching either core document, and the server now records both as
`termit-uv v0.4.1p5 (37b84fb)`.

**Still open, and said so rather than assumed:** the full daily agent cycle — sync, the transport-aware
self-check, the page render — has not yet run on `p5` by the schedule; the next one is due a day later. What
was exercised is the same code reached by hand, plus the server's own probe. If the cycle disagrees, it is
the page render and the `warp` line in the self-check report that will say so, and the rollback is the
previous binary plus `termit-uv init`.

**One host detail worth recording:** the production server has no `curl`, and every installer and one-liner
in the guide assumes it. The update was done with `wget` and `sha256sum -c`; installing the package or
teaching the scripts a `wget` fallback is a decision, not yet made.

### 11.11 Production pass, 0.4.2, 2026-11-20

`0.4.2` is the release that makes a first installation work on the machine a participant actually has:
the installers resolve a release with `wget`, `mn.sh` upgrades without being handed the domain it already
knows, the wizard asks for the certificate authority and finishes a registration the first run did not, the
menu covers every command, and the guard no longer treats the node's own WARP endpoint as a scanner. Only
the last two touch a running node, but the installers are the first thing a participant meets, so the
release went to the production mesh as a pre-release before any tag existed.

**The pre-release this record is about is `v0.4.2p3`.** `p1` was the release branch's first build; `p2`
carried the flagless `mn.sh`; `p3` added the never-ban entry for the tunnel endpoint. The lab gated `p3`
(`19 pass, 2 skip` — `clock skew` by design, and `disk full` refused by its own precondition when the
filesystem stopped at 28 KB free rather than claim a drill it could not set up).

**The server took `v0.4.2p3` with `sh mn.sh --version v0.4.2p3` and no other argument**, which is the
behaviour this release adds: the script printed `domain: … (from /etc/termit/mn/config.yaml)`, replaced the
binary and the unit, re-ran `init` and restarted the service. The database schema did not move (5 in both
trees, so the previous binary still reads it), the shipped unit is byte-identical to the installed one, and
`init` rewrote the configuration only to normalise it — it added the `locale`, the guard's default
thresholds and `default_deny: true`, and dropped quotes the YAML did not need; every operator decision
(domain, prefix, `ssh_from`, blocklist URL, subscription) came back unchanged. `status` lists both nodes
`active` with no failed cycles, `healthz` answers `"build":"v0.4.2p3"` with `schema_version: 5`, and
`agent/sync` still answers `401` without a key.

**Both nodes took `v0.4.2p3` with `sh uv.sh --version v0.4.2p3 --no-enroll`.** Each `init` reported eleven
steps `present` and three `ok`; `xray.json` and `config.yaml` kept their digests, the units were already up
to date, and `termit-xray` was never restarted (one node still on its 2026-11-17 08:01:37 EET start, the
other on 06:01:54 UTC). `warp status` reports `egress: on` through Cloudflare on both, `doctor` says 19 ok,
and the certificate and the decoy answer `200` to a request from outside the node. The firewall table
survived the upgrade untouched — still confirmed from three days earlier, SSH still restricted to the
operator's own address, 145 blocklist networks in force — which is the invariant that matters: an upgrade
never touches host firewall state.

**The never-ban change was proved where it matters.** On both nodes `termit-uv bans block 162.159.192.1`
now exits 2 with the refusal, and neither ban list holds a Cloudflare address, while the guessable-SSH and
blocklist machinery is unchanged. The node's own tunnel endpoint is read from `warp.yaml` whatever
`warp.mode` says, so a node that turns WARP off and on again cannot start by banning its peer.

**Still open, and said rather than assumed:** the daily agent cycle has not yet run on `p3`; the next
self-check reports to the server within the hour, and until it does the server's `AGENT` column still names
the previous build. The refusal message from `bans block` lists loopback, the private ranges, the
management server and `ssh_from` — it does not yet mention the WARP endpoint it now also refuses, which is
a one-line correction owed to the next release. Also worth recording: this workstation could not open a
public TLS connection during the pass (Windows `schannel: AcquireCredentialsHandle failed`), so the
external `200`s above were taken from the server host with `wget`; the nodes' own end-to-end checks are the
server's probes and their own `warp status`.

### 11.12 Production pass, 0.5.0, 2026-11-21

`0.5.0` is the release that gives the mesh a second transport, makes the WARP tunnel carry UDP, and stops
the node's own address from leaving on the family the tunnel does not carry. It touches the core's document
on every node, so it went to production as a pre-release before any tag existed.

**The pre-release this record is about is `v0.5.0p2`.** `p1` carried the release; `p2` added one fix found
during the pass — the sync body carried a `transports` field from every node, and the 0.4.2 server, whose
decoder is strict, rejected the whole request with `url_invalid: json: unknown field "transports"`. It
showed up on then4 the moment the node was upgraded ahead of its server, which is the order a participant
can reach for. `b9ec13e` sends the field only when the node serves more than the TLS profile.

**Order: server, then nodes.** The server took `sh mn.sh --version v0.5.0p2` with no other argument; the
database migrated `5 → 6` (the `uv_transport` table), which makes this upgrade one-way — the 0.4.2 binary
cannot read it. `status` lists both nodes `active`; `healthz` answers `"build":"v0.5.0p2"` with
`schema_version: 6` and a `mesh_generation`; `agent/sync` without a key answers `401`. A fourth node still
on `v0.4.2` — a participant's, not ours — kept syncing across the migration, which is the server side of
the same compatibility question the p2 fix answers on the node side.

**then4 is the node with the transport.** Its temporary `hysteria2-temp` — the stopgap that proved the path
— was stopped, disabled and removed (unit, `/etc/hysteria`, `/usr/local/bin/hysteria`), and the product's
own unit replaced it: `termit-hysteria.service`, user `termit-hy`, UDP/443, `MemoryMax=256M`. The
configuration gained `hysteria: {}` and `security.firewall.udp_ports: [443]`; `init` refused the first
attempt with `hysteria needs UDP/443 in security.firewall.udp_ports, or its traffic is counted as
scanning`, which is the validation added in this release doing its job.

Measured through the product, a client on the node itself against the node's own link:

| Check | Result |
|---|---|
| TLS profile end to end | `200` |
| exit through the transport | `ip=104.28.193.183`, `warp=on` — Cloudflare, not the node |
| 10 MB down | 14.8 MB/s ≈ 118 Mbit/s |
| **IPv6 literal through the transport** | refused in 13 ms (`::/0 → block`) |
| **IPv6-only name** (`ipv6.icanhazip.com`) | fails; it does **not** answer with `2a12:bec4:1485:67a::2` |
| decoy, direct | `200` |

The last two rows are the point of the routing change: before it, the same request answered with the node's
own IPv6 address, because the family the tunnel does not carry was routed `direct`. then4 has a global
address, so this is the environment where the leak was measurable, and it is gone.

**then5 took `v0.5.0p2` with the transport left off**, which is its default: the node's document gained the
same IPv6 refusal and nothing else. `warp status` `egress: on, via 104.28.222.16`, TLS `200`, both nodes
`already in sync: 4 client(s), 2 peer(s)`, `doctor` 0 on each. Enabling the transport on then5 needs the
firewall table re-applied with `udp_ports: [443]` — a separate, deliberate act with a rollback timer, not
something a release pass does on its own.

**The transport's off-path was proved on the lab**, not on production: removing the `hysteria:` block and
running `init` reported `hysteria-off ok`, left the unit inactive and deleted the unit file, the
configuration, the binary and the cached download, closed the port, and `doctor` still exited 0; restoring
the block brought it back and a real client fetched `https://example.com` through it with `200`.

Backups: MN `/root/termit-mn-pre-050p1-20261121-181849` (binary, unit, `etc-termit.tgz`,
`var-lib-termit.tgz`, sha256), then4 `/root/then4-config-before-0.5.0.yaml` and
`/root/termit-pre-050p1-*`, then5 `/root/termit-pre-050p1-20261121-181930`.

### 11.13 Production pass, 0.5.1, 2026-11-21

`0.5.1` fixes the owner's page — with the transport enabled it filed the node's own `hy2://` link under
"Your own devices", whose note promises a link that never reaches the mesh — and adds the two things that
made the transport usable without an editor: `init --hysteria` / `--no-hysteria`, and the `hy2://` link in
`uri`, `device add` and `device list`.

**The pre-release this record is about is `v0.5.1p1`** (`3d6a76f`, 14 assets). Order: server, then5 (the
node that had the transport off), then4. The server took it with `sh mn.sh --version v0.5.1p1`; schema
stayed 6, `healthz` answers `"build":"v0.5.1p1"`, both nodes active.

**then5 was where the new interface was proved**, because it is the node that had `udp_ports: []` and no
`hysteria:` block. `termit-uv init --hysteria` wrote both, reported `11 ok, 5 present`, started
`termit-hysteria` on UDP/443, and printed exactly the warning the design calls for: *"UDP/443 is in the
configuration; the table already in force was rendered without it"*, with the `firewall on` /
`confirm` next step. The table was re-applied and confirmed (`udp port 443 accept` in `inet termit_uv`,
default deny, ports 22/80/443), `doctor` 0 — and through the transport, with a client on the node itself:
`200`, exit `ip=104.28.222.18 warp=on`, IPv6 literal refused, `ipv6.icanhazip.com` failing rather than
answering with the node's address.

**The links reach the terminal.** `termit-uv uri` printed the `vless://` and the `hy2://` one
(`#then5.bookinet.ru%20(QUIC)`); `device add --label probe-device` printed both for the device, and it was
removed afterwards. On then4 the subscription then carried both styles and both nodes' QUIC links
(`hy2` present, `then5 … (QUIC)` found), so a client can now take either node over QUIC.

Backups: MN `/root/termit-mn-pre-050p1-20261121-181849` (previous pass) and the 0.5.1 run's own; then5
`/root/termit-pre-050p1-20261121-181930` plus the 0.5.1 backups; then4 `/root/termit-pre-050p1-*`.

### 11.14 Production pass, 0.5.2, 2026-11-22

`0.5.2` carries two fixes for the transport record and two things the owner can see: a QR code beside every
link on the page, and a `warp status` line that admits the `proxy` transport cannot carry UDP.

**The pre-release is `v0.5.2p1`** (`e85fa2e`), installed on the server, then5 and then4; schema stayed 6,
`healthz` answers `"build":"v0.5.2p1"`, `doctor` 0 on both nodes.

**The transport record was exercised in both directions, which no unit test can do on this network.** On
then5, which serves the transport, `init --no-hysteria` removed the block, stopped the unit, printed
`what this node serves travels with a sync, so it runs now:` and synced (`already in sync: 4 client(s), 2
peer(s)`). then4's subscription immediately showed **one** `hy2://` link instead of two — the server wrote
the record with the TLS profile alone, which is exactly what the old code did not do, so a retired transport
used to stay in the mesh for ever. `init --hysteria` put the block and the port back, the unit came up, and
the subscription had **two** `hy2://` links again (`then4`, `then5`).

**The page.** `termit-uv options` on then5 rendered 182 KB with five link rows and five codes — the node's
own `vless` and `hy2`, then4's two and the participant's one — and no external reference at all: the encoder
runs on the node, which is the reason it exists at all.

**The honest UDP line.** then5, whose transport is the core's own WireGuard, reports
`udp:     through warp (the core's own stack)`. A node on `proxy` says `not carried — this transport has no
UDP association, so UDP exits directly`, and `--json` carries `"udp": "direct"`; that is §10.1's second half
met for the one transport that cannot do better.

Backups: `sh uv.sh` and `sh mn.sh` wrote their own before the install (see `.spike-out/prod.md`).
### 11.15 Production pass, 0.6.0, 2026-11-22

`0.6.0` adds the MASQUE transport (`warp.transport: usque`), carries the server's build in the sync answer,
decodes a peer's label on the page, and records the advisory and the three protocol decisions. The pass was
required because option D is a third-party daemon plus a loopback hop, and Cloudflare's MASQUE endpoints are
not reachable from the lab.

**It found four bugs before it found the transport working**, each invisible to the unit tests and to the
lab because each is a fact about a real host:

1. The pinned release URL carried the tag twice (`.../download/v4.2.1/v4.2.1/...`, a 404); the base now
   stops at `download`, and a test pins the URL the release really serves.
2. `usque register` asks for Cloudflare's terms on stdin unless `--accept-tos` is passed, and an install has
   no stdin: `failed to read user input: EOF`. The flag is passed and OPERATIONS says whose acceptance it is.
3. The unit was written and nothing started it — `units` only reloads systemd, and the start step knew
   `termit-hysteria` but not `termit-usque`: `loaded; disabled; inactive`, with the core's SOCKS hop pointing
   at a closed port. The start step enables it.
4. The unit's `User=termit-usque` did not exist (`Failed to determine user credentials: No such process`,
   status 217/USER), and the device file was `root:root 0600`, which the sidecar's account could not read.
   The user step creates the account and joins it to the core's group; the file is `root:termit-usque 0640`,
   and a registration written before that rule is repaired instead of registering a second device.

**Then the real blocker.** With the sidecar up, TCP through it worked and UDP did not. `usque`'s SOCKS5
(upstream `txthinking/socks5`, `request.go`) keys the association on the client address of the control
connection — for a wildcard request it takes `c.RemoteAddr()` — and `UDPHandle` refuses a datagram from any
other source: `udp address 127.0.0.1:53284 is not associated with tcp`. Xray's socks client opens a fresh
UDP socket, so every flow died there; a plain `socks -> socks(127.0.0.1:40002)` config with no VLESS in it
reproduced it. `internal/socks5.go` on usque's `main` is identical to `v4.2.1`, and `v4.2.1` is still the
newest release, so there was no upstream fix to wait for.

**The relay.** `termit-usque-relay.service` (`termit-uv warp-relay`) listens on `127.0.0.1:40001`, which is
what the core dials, and the sidecar moved to `40002`. Per association the relay opens its own control
connection to the sidecar, binds the UDP socket to that connection's port and forwards the SOCKS5 datagrams
verbatim; TCP goes through the same relay as a plain CONNECT. A unit test drives a fake sidecar that refuses
a mismatched source port, so the property the relay exists for is checked without usque.

**What production proved, on then5 with `v0.6.0p7`.** Through `127.0.0.1:40001`:
`curl` exits at `2a09:bac5:31cd:…` with `warp=on`, and a STUN query — the same ICE/STUN a voice client uses —
returns `104.28.222.16:…`, i.e. UDP leaves at Cloudflare and not at the node's own `144.31.53.114`.
`warp status` reports `udp: through warp (the MASQUE sidecar)`. With the sidecar and the relay stopped it
reports `egress: unavailable` and `udp: direct`, the decoy still answers `200`, and direct traffic leaves at
the node's address: the document's balancer carries `fallbackTag: direct`, which is the fail-open. The node
then went back to `wireguard`: `egress: on via 104.28.222.16`, the sidecar's unit, binary and device file
gone, `doctor` 20 ok, `in sync: 5 client(s), 3 peer(s)`.

One observation recorded rather than fixed: `warp.family: ipv4` is not applied on this transport — the MASQUE
tunnel is dual-stack and the trace egressed over IPv6 (`2a09:…`). That is not the node's own address and so
not the 0.5.0 leak, but it is not what the key says either; the sidecar would have to be told, which it
cannot be through its command line today.

Pre-releases on the way: `v0.6.0p1` (`9445a9b`), p2 (`d3ddce1`), p3 (`9738ee2`), p4 (`bdd963d`), p5
(`295931a`), p6 (`9a53ab2`, whose CI failed on the relay's spoof test), p7 (`8e4fe30`). The MN took p1, p2
and later `v0.6.0`; then5 took p1–p7. Backups: `/root/termit-mn-pre-060p1-20261122-153137`,
`/root/termit-pre-060p1-20261122-173207`, and the p2/p5 copies beside them.

**After the pass, on `dev`, the three debts were fixed**: `warp.family` is documented as a `wireguard` key
(the MASQUE sidecar is dual-stack and takes no family flag), the egress probe waits for the relay's port
instead of warning about the node's own address, and `init --hysteria` prints its firewall hint only when
`firewall.json` really lacks UDP/443. Then then5 was put on `usque` for the steady state and left there:
`warp status` says `udp: through warp (the MASQUE sidecar)`, TCP through the relay exits at
`2a09:bac1:2520::48e:5e` (`warp=on`), a STUN query through the same port returns `104.28.222.16`, the decoy
answers `200`, `doctor` 20 ok and `in sync: 5 client(s), 3 peer(s)`. The `0.6.0` binary still prints the old
probe warning during `init` — the fix ships in `0.7.0`.
