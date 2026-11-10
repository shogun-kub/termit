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
  `mn uv list` through the socket.
- Provisioning helpers: `Machine.Run`, `Upload`, `WriteFile`, `Unit`, `WaitFor`, and `MNAdmin` (the
  admin token is passed in the remote environment and redacted from every captured output).
- `FromEnv` skips, not fails, when the environment is incomplete: the lab is a manual gate and a
  developer machine must still run the default `go test`.

**Done when**: `go test -tags=lab ./test/lab -run Smoke` passes against the three machines, and a
failed run prints enough to debug without logging in by hand. Both hold: the suite runs from the
Windows host with `TERMIT_LAB_PLINK`/`TERMIT_LAB_PSCP` pointing at PuTTY, and a failing scenario dumps
both units' journals and `mn uv list` before restoring the lab.

---

## M1 — UV standalone (8–12 d)

Purpose: a fully working node with no MN involved. This is the first shippable artifact: a user can
install it, get a valid certificate, see a decoy site, and connect with a manually built
`vless://` URI.

### M1.1 CLI, config, state — **done**

- `uv init`, `doctor`, `status`, `uninstall` and `version` are implemented. The rest of the command
  set was declared and exited `3` ("not implemented in this build") until it landed; every documented
  command is implemented now, so the reserved status is gone and the usage text no longer lists it.
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
  ACME directory reachability, `/dev/net/tun` when WARP is wanted (it is by default; a missing device
  is a warning with the reason, not a failed install), free space (measured on the nearest existing
  ancestor, so it works before the first install), memory, apt.
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

### M2.1 Store and bootstrap — **done**

Done:

| Piece | Where | Notes |
|---|---|---|
| Atomic writes | `internal/atomicfile` | moved out of the UV state package; both binaries write config a running service reads |
| Shared ACME | `internal/acme` | was `internal/uv/acme`; both sides issue certificates |
| Database | `internal/mn/store` | pure-Go SQLite, schema version in `PRAGMA user_version`, migration in one transaction, a newer schema refused, `VACUUM INTO` for backups, the seven tables from `DESCRIPTION.md` §14 |
| Credentials | `internal/mn/secret` | Argon2id with a per-value salt, readable invite codes, constant-time comparison |
| Configuration | `internal/mn/config` | strict YAML, one schema version, defaults, validation that collects every problem |
| `mn init` | `cmd/mn/init.go` | config, master key (`0600`, `TERMIT_MN_MASTER` override, never replaced), database, first admin token, first invite; re-running keeps what exists |
| `mn serve` | `cmd/mn/serve.go` | `/healthz`, loopback-only `/metrics`, graceful shutdown, TLS when a certificate exists |

Measured on `termit-mn`: `config.yaml` `0644`, `master.key` `0600`, `termit.db` `0600`, schema 1; a
second `mn init` reported the existing key instead of replacing it; `/healthz` answered with the build
identity and the schema version; SIGTERM shut the server down inside the grace period.

Still to do in this milestone:

- ACME issuance for the server's own certificate (the challenge listener on `:80`), so `mn serve` is
  TLS by default rather than by luck.
- `mn backup` / `mn restore` on top of `store.Backup`, with a schema-version guard on restore.

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

Also done since: the admin CLI (`mn invite|uv`) over a `0600` unix socket; the certificate check with a
14-day minimum validity; and the certificate pin the probe uses instead of a CA.

Lab evidence (termit-mn + termit-uv1, Pebble as the CA):

| Step | Result |
|---|---|
| `mn invite create --uses 2` | code issued, stored as a digest |
| `mn invite list` | `inv_T5bh68a-FS  0/2  usable` |
| `POST /api/v1/enroll` from uv1 | `201`, node `uv_tsFkPtgUuYn6`, state `active` |
| credentials in the response | `agent_key`, `sub_token`, `probe_uuid` — all present |
| `mn uv list` | node with its domain, state and source address `192.0.2.38` (documentation range) |
| `mn invite list` afterwards | `1/2` — the invite was consumed exactly once |

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
| Tokens | `mn sub rotate` | new token returned once, old one stops working at once, rotation is audited |
| Storage | `enroll` | the record holds the parsed profile (domain, credential, path); a node enrolled before this change must re-enroll |
| Tests | `internal/mn/sub` | ordering, stability under reordering, degraded marking, empty set, JSON shape, ETag changes |

Acceptance on the lab — two nodes, one subscription, traffic through each URI:

| Step | Result |
|---|---|
| `uv init` on termit-uv2 | 12 steps ok, certificate issued by Pebble, identity `ac7af400` |
| enroll uv2 | `201`, node `uv_R055z4RzZHki`, state `active` |
| `mn uv list` | two nodes, both `active` |
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
| `mn uv reissue` | admin socket | a lost agent key can be replaced; the old one stops at once |

Lab: uv1's sync returned uv2 with pair `9821a6ed-3746-47b3-a5d2-149af557bf2f`; uv2's sync returned the
same value for uv1 — the property the derivation exists for. A wrong key answered `401`.

Also done since:

- `uv sync` (`internal/uv/agent`): fetch, generate, `xray -test`, install, restart, roll back on a
  refused restart. Client order is sorted, so an unchanged set touches nothing.
- `subscription_url` is no longer empty. The token is stored twice — a digest that answers lookups and
  an AES-GCM copy sealed under the master key — so MN can print the URL again without the database
  alone revealing it. Schema v2 with a migration map carries existing databases forward.

Lab evidence for this milestone:

| Step | Result |
|---|---|
| `uv sync` on uv1 and uv2 | `applied: 3 client(s), 1 peer(s)`; second run `already in sync` |
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
| `uv enroll` | registers the node, stores the credentials in the state file and takes the first sync; refuses a second enrollment without `--force` |
| Sealed agent key (schema v3) | `mn uv agentkey` reads the key back without rotating it; the row holds a digest and an `aead:` blob |
| `uv sync --check` | exit 0 in sync, 4 on drift, 1 when the server is unreachable; writes nothing |
| Retention | checks 90 days, audit 1 year, pruned at startup and daily in `mn serve` |

Lab: reissue then agentkey returned the same key; `sync --check` said `in sync` (0), then `drift: 1 to
add` (4) after a peer credential was removed from the core config, then `0` again after `uv sync`.

---

## M3 — Full lifecycle (8–12 d)

Purpose: the three scenarios from the concept, end to end, verified in the lab.

### M3.1 Scheduler and state machine (MN) — **done**

- Queue keyed by `next_check_at` with jitter; worker pool; per-node serialization.
- Retry policy inside a cycle (3 × 10 min, configurable); cycle outcome aggregation (`ok`, `failed`,
  `unknown`).
- Pure transition table + side effects (peer invalidation, subscription ETag bump, audit event).
- `mn check now`, `mn policy set`, `mn uv block|unblock|rm` (admin override, audited).
- Retention job (checks 90 d, audit 1 y).

Done so far:

| Piece | State |
|---|---|
| Transition table, pure, full cross-product under test | done (`internal/mn/sched`) |
| `mn uv block` / `unblock` / `rm` through the same table, audited | done |
| `mn uv forget` — frees a removed node's domain | done |
| Retention (checks 90 d, audit 1 y, at start and daily) | done |
| Queue, cycle runner with in-cycle retries, aggregator, jitter, `mn uv check now` | done |
| Certificate pin stored at enrollment (schema v5) — without it every scheduled check fails | done |
| `mn uv show` prints the last probe | done |
| `mn policy` show and set, per-node serialisation within a pass | done |
| `mn check now` for the whole mesh; `mn status` dashboard (counts, last/next check) | done |

The table is the contract M3.2 and the scenario milestones build on: `active → unavailable` on the
first failed cycle, `unavailable → blocked` on the second, `blocked → active` only through an explicit
unblock, and `unknown` never moves anything.

Lab: block, block again ("nothing changed", one event row), unblock, rm, approve-after-rm refused with
`conflict`; a sync attempt after removal answered `node_inactive`; `uv forget` freed the domain and uv2
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
- `uv unblock`, `uv migrate --relocate`, `uv status --json`.

Done so far:

| Piece | State |
|---|---|
| Loop: state lock, daily jittered cycle from the server's schedule, 5 min → 1 h backoff, `agent` unit with `-v` | done (`internal/uv/agent`) |
| Self-check: `xray`, `proxy`, `api`, `web`, `certificate`, `warp`; `unknown` is reported, never a failure | done |
| Reconcile: desired vs file vs the core's own client answer, hot API first, validated restart fallback, canary, rollback | done |
| `uv agent [--once]`, `uv status --json` (schedule, self-check, peers, enrollment), `uv unblock` | done |
| `uv migrate --relocate` | deferred to M3.5: it belongs to Scenario C, where the endpoint actually changes |

The reconcile order is the contract: the file is compared with the desired set *and* the running core
is asked what it serves, because an API update or a hand edit can leave the core different from a
correct file. Only the client set may go through the API; a path, port or stream change is always a
validated restart, and the canary (`127.0.0.1:80` through the local tunnel, `Host` = the node domain)
runs after either path and before the result is reported. `uv sync --check` reads the live set too and
exits 4 on drift.

Lab: the agent ran on both UVs, reported seven self-check items, wrote `next_sync_at` a day out with
per-node jitter, and `mn uv show` printed the self-check. A device added to the state landed through
the hot API with the core's `MainPID` unchanged; the same held for removing a peer after `mn uv
block`, and re-adding it after `uv unblock` (the MN verified DNS and the decoy and moved the node back
to `active`). Two defects surfaced: the desired-set map is `id → email` and the reconciler initially
read it as `email → id`, so every hot verification failed and every change fell back to a restart;
and the agent unit's `ProtectHome=yes` hides `/root`, so a CA file there was unreadable and the unit
now drops `ProtectHome` when a configured readable path lives under a home directory.

### M3.3 Scenario A acceptance — **done**

Enroll two UVs → wait for one cycle (or `mn check now`) → both nodes present in each other's Xray
config and subscription → kill node 2 → cycle fails after retries → `unavailable`, still in node 1's
config → second failed cycle → `blocked`, removed from node 1's config at its next sync → restart
node 2 → stays blocked until an explicit unblock → `uv unblock` → `active`, peers re-add it.

Done so far:

| Piece | State |
|---|---|
| Scenario A as one lab test (`test/lab/scenario_a_test.go`), driven through the M0.5 harness | done |
| Subscription URL learned from every sync, not only from enrollment | done |

The test asserts the server's view, the core's **live** client set (not the file) and the subscription
body at each step, and it restores the lab on every exit path. The subscription check is the reason a
small product gap surfaced: a node enrolled before the field existed never learned its subscription
URL, because only enrollment stored it. Every sync now stores what the server sends, so `uv status`
works on an old node too.

Lab, both runs identical: the two nodes listed each other's `peer-…@termit` credential and each
other's domain in the sing-box subscription; killing uv2's core produced `check_failed → unavailable`
while uv1 kept the credential; the next cycle produced `blocked`, and uv1's sync dropped the peer
through the hot API (`canary=true`); a restarted, healthy uv2 stayed `blocked` through two passing
checks, its sync was refused with `node_inactive` and its config hash was unchanged; `uv unblock`
passed DNS/decoy/VLESS verification and moved it to `active`; uv1 re-added the peer by hot API. The
whole scenario takes ~18 s, which is what makes it usable as a regression gate.

One flake is recorded rather than hidden: the first `uv unblock` immediately after a cold start of the
core can hit a single failed probe (`check_failed: EOF`) even though the previous check passed, because
a fresh listener answers before its handlers settle. The test now requires the node to pass two checks
in a row before unblocking; the product path is unchanged and the retry is not automatic.

The subscription check also found a real defect the manual transcripts had missed: the endpoint shared
the enrollment limiter (10 requests / 15 minutes) keyed per token, so the fourth fetch in one scenario
run answered `429`. A subscription token is unguessable and clients refresh on every launch, so the
subscription counter is load control, not brute-force defence: it is now a separate limiter at 60 per
15 minutes, with a boundary test (60 pass, 61 is refused).

### M3.4 Scenario B acceptance — **done**

From a rebuilt machine (new `self_uuid`, same agent key), `uv init --relocate` followed by
`uv unblock` (the box builds the new URL from its own identity) → MN verifies DNS/decoy/VLESS →
`active`; peers converge; the subscription contains the new endpoint. A blocked node that merely comes
back healthy stays blocked until this explicit request.

Done so far:

| Piece | State |
|---|---|
| `uv init --relocate --agent-key-file` — fresh install, new identity, saved key, no enrollment | done |
| `uv unblock` builds the node's own URI from the local identity when `--vless-url` is absent | done |
| Scenario B as a lab test (`test/lab/scenario_b_test.go`) | done |

The key file has to be `0600`; the check is part of the command, not a convention. Passing the URL on
the command line would have put a credential into the shell history and the remote process list, so the
default is to build it locally — this is also what makes a rebuilt box work without the operator
having to copy anything but the key.

Lab, repeatable: uv2 blocked, its agent key saved to a 0600 file and its config to a copy,
`uv uninstall --purge`, restore the config, `uv init --relocate --agent-key-file` → new identity
(different fingerprint), `enrolled: true`, server still `blocked`, still two nodes; `uv unblock` →
`active` (DNS + decoy + VLESS against the new UUID/path), and its own sync restores the probe
credential and the peer. uv1's subscription then carries the rebuilt path and not the old one, while
the `peer-<id>` credential and the node id are unchanged. Whole scenario ~15 s.

The test also proves the negative: after the rebuild and before the unblock, the server has not been
touched (blocked, same node count). Cleanup removes the key file, restarts the units, unblocks and
syncs; if the rebuild died half-way it finishes the init from the saved key and config.

One lab-only obstacle was also a product wart: a rebuild restarts the agent several times, and the
fifth start inside five minutes tripped systemd's start limit, leaving the unit failed. `uv init` now
clears the unit's failed state before restarting the agent (a re-runnable command must not break its
own service), and the agent's burst is ten instead of five — a real crash loop still stops after ten
attempts with `RestartSec=30s`.

### M3.5 Scenario C acceptance — **done**

The box moves to a different domain, keeps its agent key, and the server's record follows it. The
command is split the same way as Scenario B: the install is `uv init --relocate` with the new domain,
and the network action is `uv migrate --relocate`, which posts the endpoint the box now serves.

Done so far:

| Piece | State |
|---|---|
| `POST /api/v1/agent/relocate` — verify the claimed endpoint from the requesting address, then swap domain, self UUID, profile and pin together | done |
| `uv migrate --relocate` — builds the local URL, refuses a box whose config names another domain | done |
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
name yet), `uv uninstall --purge`, install on the new domain with `--relocate`, then
`uv migrate --relocate` → `active`, record on the new domain, uv1's subscription on the new domain and
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
| Install step between `packages` and `xray`; refuses an unsupported host with a reason and still reports a converged node | `internal/uv/install` | unit tests; lab: `uv init`-time refusal path |
| `uv warp status\|mode`, `uv init --warp/--no-warp/--warp-selected`, `uv status` line, `uv doctor` check, uninstall disconnects the tunnel | `cmd/uv`, `internal/uv/install` | unit tests; lab |
| Self-check reports off / unavailable-with-reason / healthy / failing (fail-open says "traffic exits directly") | `internal/uv/agent` | unit tests for all five outcomes |

The lab cannot run WARP itself (unprivileged LXC, no `/dev/net/tun`), so `test/lab/warp_test.go`
proves what surrounds the tunnel, end to end: the migration to a mode, the refusal with its reason,
the generated route accepted by the node's own core, **fail-open with a dead sidecar** (a real Xray
client fetches `https://1.1.1.1/cdn-cgi/trace` through the node and gets an answer with `warp=off`
while nothing listens on port 40000), the server still seeing the node as healthy, status and doctor
telling the truth, and the switch back removing the route without interrupting traffic. Registration
and the connected tunnel remain on the release checklist for a host with TUN.

Two defects the lab found and the fix:

- `uv warp mode` restarted the core twice (the `xray-config` step already restarts it after `xray
  -test`), which tripped systemd's start limit; the command now leaves the restart to the step.
- The running agent holds the configuration it read at start, so its next sync would have regenerated
  the old route; the command now restarts `termit-uv-agent` after the change.

### M4.2 Operations surface — done

Delivered:

| Piece | Where | Verified by |
|---|---|---|
| Operator documents: install/upgrade/backup/recover/uninstall/troubleshooting, the wire contract, the threat model | `docs/OPERATIONS.md`, `docs/PROTOCOL.md`, `docs/THREAT_MODEL.md` | written against the code; endpoint examples checked against a running server on the lab |
| A systemd unit for the management server (there was none) | `deploy/systemd/termit-mn.service` | started on the lab under systemd; `healthz`, `mn status` and `/metrics` answer |
| Bootstrap installer with checksum verification and an upgrade path that keeps the node's identity | `deploy/install/uv.sh` | lab: a local release directory installs, a corrupted `SHA256SUMS` is refused before anything is written, a missing one is refused too, a re-run upgrades `termit-uv` and `uv init` converges |
| Real Prometheus metrics: enrollments, syncs, check outcomes, a check-duration histogram, nodes by state, scheduler backlog, probe-worker health, server certificate expiry; loopback only, no node identifiers in any label | `internal/mn/metrics`, `internal/mn/server` | unit tests for buckets, labels, escaping, the 403 and the counters; scraped from the live lab server |
| The dashboard's server block: uptime, database size and schema, prober state with its reason, queue backlog and age, certificate expiry | `admin server.state`, `mn status` | unit tests; live lab output |
| Per-node history in one series: the last ten probes and self-reports together | `admin uv.show` → `mn uv show` | live lab output |
| A redaction test: enrolment and sync log identifiers, never the agent key, subscription token, probe UUID, invite code or node UUID | `internal/mn/server` | the test fails if any of them appears |
| In-place certificate renewal on the server: it re-checks every six hours, writes through the CA's chain and serves the new pair on the next handshake — no restart, and a failed attempt keeps the old certificate in service | `internal/mn/cert` (loader + loop), `mn serve` | unit tests for the reload, the broken-pair case and the loop; lab: a certificate swapped under a running server was served immediately, swapped back, and the real chain still validated |

Also outstanding, and a maintainer decision rather than a defect fix: `DESCRIPTION.md` §8 shows a sync
body shape the server does not send, §15 says agent keys are hashed with Argon2id and compared in
constant time (they are SHA-256 digests looked up through an index, deliberately), and §13.4 lists
`mn sub show`, which does not exist. `docs/PROTOCOL.md` is what the build does; the design document
should be reconciled with it before the first tag.

### M4.3 Release — done except the manual VPS pass

| Task | Where | Verified |
|---|---|---|
| A release tool, `go run ./deploy/release --version v0.1.0`, that cross-compiles both binaries for linux/amd64 and linux/arm64 (`CGO_ENABLED=0 -trimpath`), stamps version/commit/date, copies `uv.sh` and `termit-mn.service` out of the repository, and writes `SHA256SUMS` | `deploy/release` | unit tests for the asset-name contract, the version pattern, the checksum format, digesting and both refusals; two builds with a fixed commit and date produce byte-identical `SHA256SUMS` |
| Asset names exactly as the installer expects, with `uv.sh` published beside them; no tarballs | `deploy/release` | lab: `sha256sum -c SHA256SUMS` on all six files, then an install from the release directory — `termit-uv v0.1.0 (commit …, api 1, profile v1)`, identity and certificate intact, services active; the server binary reports the same version after a restart |
| CI: `govulncheck` against our packages; a tag-triggered release job that builds, verifies the checksums and attaches the artifacts | `.github/workflows/ci.yaml`, `.github/workflows/release.yaml` | `govulncheck v1.9.0` locally: no vulnerabilities reachable from our code; the workflow runs the same commands the release tool documents |
| `v0.1.0` tag | `git tag` | created locally; pushing it publishes a **pre-release** release, and flipping that flag is the act that says the production pass is done |

### Interactive front end — done

Both binaries carry a menu (`internal/termui`, `cmd/uv/menu.go`, `cmd/mn/menu.go`): a numbered screen
that asks what the flags would ask and then runs the same command line a script would run, so a menu
entry and its flags cannot drift apart. On a terminal the bare binary opens it; anywhere else it stays
usage text and exit 2. It is line-oriented on purpose — no raw terminal mode, no third-party TUI
library, and it can be driven by a script, which is also how it is tested. Colour appears only on a
terminal and obeys `NO_COLOR`/`TERM=dumb`; the screen is never cleared without colour, so a captured
session reads as sections rather than one overwritten page.

Deliberately absent: starting `mn serve` from the menu (the unit owns the log), and any state of its
own (a dropped SSH session loses nothing). Interactive prompts stay out of the install path — the menu
builds flags, it does not replace them, which is why `uv.sh`, `uv init` and `mn init` remain scriptable.

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
| R5 | Let's Encrypt path untested in the LAN lab | High | Medium | Pebble covers the ACME code path; the real-domain pass gates promoting a release out of pre-release, and the workflow publishes pre-release until it is done |
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
4. ~~Whether `uv` should enable WARP by default on fresh installs~~ **Resolved** on 2026-11-10: on by
   default, opt-out with `--no-warp` or `warp.mode: off`. See M4.1.
5. Whether the MN domain and the UV domains are expected to live in the same DNS zone (affects the
   DNS-consistency check and documentation).
