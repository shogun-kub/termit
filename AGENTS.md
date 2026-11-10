# AGENTS.md

Instructions for any human or AI agent working in this repository. Read this file completely before
your first change, then read [`docs/DESCRIPTION.md`](docs/DESCRIPTION.md) for the design.

---

## Language

The user works in Russian and English interchangeably, and may mix both inside a single message. Read
either without asking for a translation, and never ask which language to use.

| Where | Language |
| --- | --- |
| Chat replies | **Russian**, unless the user asks for another language |
| Everything committed to the repo — `docs/`, `README`, code comments, commit messages | **English** |
| Quoted material — error messages, UI strings, log lines, vendor text | Original, unchanged |
| Terms of art — `patch`, `offset`, `Evidence`, tool and API names | Original, never translated |

A Russian question does not make the write-up Russian: investigate in whichever language is clearer,
but write the finding in English and explain it back in Russian.

## 0. Read first

| Document | Why |
|---|---|
| [`docs/DESCRIPTION.md`](docs/DESCRIPTION.md) | The design. Goals, protocol, credential model, state machine, invariants. |
| [`docs/IMPLEMENTATION_PLAN.md`](docs/IMPLEMENTATION_PLAN.md) | Milestones and the task you are probably implementing. |
| [`docs/QUESTIONS.md`](docs/QUESTIONS.md) | Decisions already made and still open. Do not silently re-decide them. |

If a task contradicts these documents, stop and ask — do not "fix" the document to match the code.

## 1. What this repository is

Termit builds two Go binaries from one module:

- `termit-uv` (`cmd/uv`) — installs and supervises a VLESS+XHTTP+TLS node on a Debian box: Xray
  config, ACME certificate, generated decoy site, traffic rules, Cloudflare WARP (on by default), and
  a daily sync with the management server.
- `termit-mn` (`cmd/mn`) — the management server: node registry, health prober, credential
  derivation, subscription rendering, admin CLI over a unix socket.

Target runtime: Debian 13 (trixie), amd64 and arm64. Development happens on Windows or Linux; the code
must compile on any platform, but Linux-only behaviour belongs behind build tags.

## 2. Non-negotiable rules

### 2.1 `references/` is read-only input, never part of the product

`references/autoXRAY-multi` and `references/Blitz` are third-party GPL-3.0 projects kept for study.

- **Never modify, move, rename, format or "fix" anything under `references/`.**
- **Never commit `references/`** — it is in `.gitignore`. If you find it staged, unstage it.
- **Never copy code from them into this repository.** Both are GPL-3.0; this project is AGPL-3.0 and
  must be a clean re-implementation. Read them for behaviour, then write idiomatic Go.
- Never add a runtime dependency on them, link to their files, or shell out to their scripts.

### 2.2 The safety invariants are contracts, not suggestions

From `docs/DESCRIPTION.md` §11, restated because they are the easiest thing to break:

1. **Termit never mutates host routing or firewall state.** No `ip route`, `ip rule`, iptables,
   nftables. WARP is used only in `warp-cli` proxy mode (a loopback SOCKS5), never as a full tunnel.
2. **`unknown` is not `failed`.** If a check could not be performed (prober down, MN-side DNS error,
   missing binary, timeout of the prober itself), the result is `unknown` and **no state change**
   happens. Only a genuine negative result may move a node to `unavailable` or `blocked`.
3. **Config is validated before it is applied.** Every generated Xray config is written atomically,
   checked with `xray -test`, and only then activated. Hot update (Xray API) first, validated restart
   second, rollback on any failure.
4. **Idempotency.** Re-running `uv init`, a sync, or an install step converges to the same state and
   never regenerates issued credentials, XHTTP paths or certificates.
5. **No secrets in logs, metrics, error text, config examples or tests.** That includes agent keys,
   subscription tokens, VLESS UUIDs, private keys and ACME account keys. Log identifiers and
   public metadata, never credentials.
6. **Direct traffic stays direct.** SSH, the decoy, ACME, and any other service on the box must keep
   working through every code path. If a change could capture host traffic, it is wrong.

### 2.3 Autonomous actions are narrow

The agent may restart its own services and rewrite Xray config. It must never:

- reboot the host, change `sshd`, or touch `/etc/network`;
- install packages outside the declared dependency list in `internal/uv/install`;
- delete user data outside `/var/lib/termit` and its own generated files;
- contact any host other than the configured MN, the configured ACME directory, and Cloudflare's WARP
  endpoint and package repository — the two the WARP feature exists to talk to, and only when WARP is
  enabled.

## 3. Repository layout

```
cmd/uv/                 entry point: subcommand dispatch (init, doctor, status, device, uri, enroll,
                        sync, agent, unblock, migrate, renew-cert, renew-site, warp, uninstall,
                        version)
cmd/mn/                 entry point: serve, init, invite, uv, check, policy, sub, status, backup,
                        restore, version
internal/cred/          pair-credential derivation (shared, has test vectors — do not change
                        derivation without a plan for rotating every existing credential)
internal/proto/         API request/response types, problem codes, vless URL build/parse
internal/version/       version, build metadata, api_version constants
internal/uv/agent/      scheduler, sync loop, state machine client side
internal/uv/install/    install/upgrade/uninstall steps, package management
internal/uv/xray/       config model, generator, hot API client, `xray -test` gate
internal/uv/service/    generated systemd units and the nginx drop-in
internal/uv/decoy/      deterministic site generator + embedded templates
internal/uv/web/        generated nginx config (TLS, decoy root, proxy of the secret prefix)
internal/uv/acme/       ACME (HTTP-01), certificate store, renewal
internal/uv/warp/       warp-cli lifecycle, proxy-mode health and the SOCKS5 trace
internal/uv/state/      local state + atomic JSON persistence
internal/mn/server/     HTTP API, middleware, auth, rate limiting
internal/mn/store/      SQLite schema, queries, migrations
internal/mn/probe/      health prober (xray-core worker; cert/decoy; DNS)
internal/mn/sub/        subscription rendering (base64 and sing-box JSON)
internal/mn/sched/      scheduler, check queue, state machine server side
internal/mn/admin/      unix-socket admin API + CLI glue
deploy/systemd/         installed copies of the units, for readers; the node's units are rendered by
                        `internal/uv/service` (paths come from the configuration, so nothing is shipped
                        as a static file), while `termit-mn.service` has no generator and is shipped here
deploy/install/         bootstrap shell script and release checks
deploy/release/         the release builder (`go run ./deploy/release`): cross-compiles, stamps the
                        version, writes SHA256SUMS. Not a shipped binary; CI runs the same command
test/lab/               SSH-driven acceptance suite (build tag `lab`)
test/integration/       planned Docker Compose suite (not built; the lab is the gate)
docs/                   design and operations docs
```

Shared code lives under `internal/` (single module, not a library). Do not create `pkg/` unless a
package is genuinely meant to be imported by another module.

## 4. Toolchain and commands

### 4.1 State of the development host

The Windows host has git plus a **portable Go 1.27.3** unpacked under `.spike-out/tools/go` (gitignored,
not on `PATH`). Start every shell with:

```powershell
$env:Path = "$(Get-Location)\.spike-out\tools\go\bin;" + $env:Path
$env:GOCACHE = "$(Get-Location)\.spike-out\gocache"
$env:GOMODCACHE = "$(Get-Location)\.spike-out\gomodcache"
$env:GOPATH = "$(Get-Location)\.spike-out\gopath"
```

The cache overrides are required: the file sandbox denies writes to the default `%LOCALAPPDATA%`
cache, and a build then fails with a permission error rather than a compile error.

Still missing: `docker` (only needed once the planned Compose suite exists), `make` (the `Makefile` is convenience only),
`golangci-lint` (CI lints anyway). `puttygen` on this host rejects `-O`, so PPK conversion does not
work here — see §4.3.

The acceptance lab is three Debian machines reachable over SSH (one MN, two UVs), optionally a fourth
(another UV) when the mesh tests need a third node. Their coordinates never go into the repository —
they come from environment variables (§4.3).

Go version: pinned by the `go` directive in `go.mod`. Use the toolchain the module declares; do not
add a `toolchain` line that requires a newer release than CI uses.

**CGO is off.** Builds are fully static (`CGO_ENABLED=0`) and the SQLite driver is pure Go. Never
introduce a cgo dependency.

### 4.2 Common commands

```bash
# Package patterns are explicit on purpose: `references/` contains third-party Go source inside this
# module's directory tree, and `./...` would compile and lint it.
go build ./cmd/... ./internal/...                # compile everything we own
go test ./cmd/... ./internal/...                 # unit tests
go test -race ./internal/...                     # race detector on library code
go vet ./cmd/... ./internal/...                  # cheap correctness pass
go test ./deploy/release                         # the release tool's own tests
golangci-lint run ./cmd/... ./internal/...       # lint (CI gate)
go run ./cmd/uv --help
go run ./cmd/mn init --domain mn.localhost --data-dir ./.dev/mn
gofmt -l cmd internal deploy                     # must print nothing
goimports -l cmd internal deploy                 # must print nothing

# the release directory (four binaries, uv.sh, termit-mn.service, SHA256SUMS), as CI builds it
go run ./deploy/release --version v0.1.0 --out dist
```

On Windows use PowerShell equivalents or WSL; do not introduce `make`-only workflows. A `Makefile`
may exist for convenience but every target must be documented as a plain `go` command above.

**Never run `go mod tidy` at the repository root.** It walks every package in the module tree,
including `references/Blitz/...`, and will happily add the MongoDB driver and a dozen transitive
dependencies to `go.mod`. Tidy in a copy that contains only our own code:

```powershell
$tmp = ".spike-out/tidy"
Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Force $tmp | Out-Null
Copy-Item go.mod $tmp; Copy-Item cmd,internal $tmp -Recurse
Push-Location $tmp; go mod tidy; Pop-Location
Copy-Item "$tmp/go.mod","$tmp/go.sum" . -Force
```

### 4.3 Acceptance lab

```bash
export TERMIT_LAB_MN=root@termit-mn.lan
export TERMIT_LAB_UV1=root@termit-uv1.lan
export TERMIT_LAB_UV2=root@termit-uv2.lan
export TERMIT_LAB_UV3=root@termit-uv3.lan          # optional: the mesh test needs it, the rest runs with two
export TERMIT_LAB_SSH_KEY=<path to the lab private key>   # never committed
export TERMIT_LAB_HOSTKEYS='termit-mn.lan=SHA256:…,termit-uv1.lan=SHA256:…,termit-uv2.lan=SHA256:…'
export TERMIT_LAB_MN_ADMIN_TOKEN=<admin token>            # never committed
export TERMIT_LAB_DNS_SUFFIX=lan
export TERMIT_LAB_ACME=https://termit-mn.lan:14000/dir    # Pebble
export TERMIT_LAB_PLINK='C:\Program Files\PuTTY\plink.exe'  # only when not on PATH
export TERMIT_LAB_PSCP='C:\Program Files\PuTTY\pscp.exe'
go test -tags=lab ./test/lab -v -timeout 30m
```

The concrete coordinates for this machine (addresses, host keys, key path, hardware) live in the
gitignored `.spike-out/lab.md`; the environment variables above are still what the tests read. On
Windows the key is a PuTTY `.ppk` that OpenSSH cannot read and `puttygen -O` is broken here, so the
harness calls `plink`/`pscp` (decided in M0.5) and pins each machine by the fingerprint in
`TERMIT_LAB_HOSTKEYS`. A missing variable skips the suite instead of failing it: the lab is a manual
gate, not part of `go test ./cmd/... ./internal/...`.

Lab hardware is the acceptance budget: UV = 1 vCPU / 1 GB RAM / 10 GB disk, MN = 2 vCPU / 2 GB. The
containers are unprivileged LXC with **no `/dev/net/tun`**, so WARP cannot be exercised there; WARP
acceptance needs a KVM VM or a privileged container. Everything else (TLS, ACME against Pebble, the
mesh scenarios, resource usage) is testable on this lab.

Rules for the lab:

- Tests must be re-runnable: they clean up nodes they created and tolerate a previous failed run.
- Never leave a machine in a state that needs manual repair; on failure, dump the relevant logs and
  a `uv doctor` report into the test output.
- Never put lab addresses, keys, domains or ACME URLs in the repository. They are environment input.
- The lab may run destructive actions (uninstall, reboot) **only** when the test is explicitly tagged
  for it; default runs stay reversible.

## 5. Coding conventions

### Go

- Standard library first. Every dependency needs a reason in the PR description (§8).
- `context.Context` on every function that does I/O; no `context.Background()` below `main`/tests.
- Errors: wrap with `%w`, add context, expose typed errors where callers must branch
  (`errUnknown`, `errCheckFailed`, `errNotDue`). Never branch on error strings.
- Logging: `log/slog`, structured, one logger injected from `main`. Redact via a central helper.
- No global mutable state. Constructors return fully-wired structs.
- Time: inject a `Clock` interface where behaviour depends on time; tests must not `time.Sleep`.
- Files: write to a temp file in the same directory, `fsync`, `os.Rename` (helper in
  `internal/uv/state`). Never truncate a live config in place.
- Platform code behind build tags (`*_linux.go`), with a non-Linux stub that returns a clear error.
- Public identifiers get doc comments; unexported helpers do not need them. Comments explain *why*,
  not *what*.
- Table-driven tests; `t.Parallel()` where safe; golden files under `testdata/`.

### Configuration and state

- Config format: YAML, parsed strictly (`KnownFields(true)`), versioned (`schema_version`).
- Paths: config in `/etc/termit/<component>/`, state in `/var/lib/termit/<component>/`, runtime
  sockets in `/run/termit/<component>/`. Anything else is a bug on Linux, with one exception:
  generated *public* content (the decoy tree, the ACME webroot) lives under `/var/www/termit/`, because
  nginx reads it as an unprivileged user and cannot traverse a `0750` state directory.
- Every file Termit writes has `0600` (secrets) or `0644` (public), and directories `0750`.
  Certificate private keys are always `0600` — the reference implementation's `0744` is a bug we do
  not repeat.
- State files carry a `schema_version` and are migrated, never silently discarded.

### Xray config generation

- `internal/uv/xray` owns a typed model; generate JSON from it, never by string templating.
- Generated config is deterministic: stable ordering, no timestamps, no random values. Golden tests
  compare the whole document.
- Profile parameters live in one place (`internal/proto/profile.go`) so that the client URI builder
  and the server config generator cannot drift apart. A round-trip test (`ParseVLESSUrl(BuildVLESSUrl(x))`)
  is mandatory for every profile change.
- After writing: `xray -test -config <path>`; on failure, restore the previous file and return an
  error that names the failing line.

### Decoy site

- Templates are embedded (`go:embed`), self-contained, and reference **no external hosts** (no CDN
  fonts, no analytics, no remote images). A decoy that phones home is a fingerprint.
- Output is deterministic from `(domain, seed, template)`; the seed is stored at install so the site
  survives restarts unchanged.
- The generated tree is regenerable: `uv renew-site` rewrites it and never leaves stale files.
- **nginx serves the tree**; the agent must never listen on `:80`/`:443`. The page, the nginx location
  that serves it and the nginx snippet for the fake auth endpoint are generated from the same template
  data, and a golden test must cover both so they cannot drift apart.

## 6. Domain rules that tests must enforce

1. `internal/cred` vectors are immutable once shipped. Changing the derivation means changing every
   node's credentials — that is a migration with a new `credential_epoch`, not a refactor.
2. The state machine is a pure function table (`state, event → state, side effects`). Every
   `unknown` path keeps the current state. Test the full cross-product, not just the happy path.
3. Subscription output is ordered and stable (label, then id). Same peers ⇒ byte-identical response
   and ETag.
4. The sync response is the complete desired peer set. `changes` is informational. UV reconciles; a
   missed response must never leave a stale client behind.
5. Never trust data from MN blindly: validate UV ids, domains, ports, paths, and UUID syntax before
   writing them into an Xray config.

## 7. What a change must include

- Unit tests for new logic, including the failure and `unknown` branches.
- A golden-file update **only** when the generated config genuinely changed; say why in the commit.
- Documentation update when behaviour, config keys or CLI flags change (`docs/DESCRIPTION.md`,
  `docs/OPERATIONS.md`, `--help` text).
- For anything touching install, ACME, WARP, or Xray lifecycle: a note in the PR saying which lab
  scenario was run and the observed result.

Do not add a test that requires the public internet or the lab to the default `go test ./...` run.

## 8. Dependencies

Before adding one, check in this order: standard library, `golang.org/x/…`, then a well-maintained
module with a compatible licence (Apache-2.0, MIT, BSD; **not** GPL/AGPL, which cannot be linked
into AGPL code without care). Record the choice and the alternative you rejected. Run
`govulncheck ./...` after adding. Vendoring is not used; the module graph must stay small enough for
`go mod graph` to be reviewable.

Decided already (`SPIKES.md` S6): ACME uses `golang.org/x/crypto/acme`, not `lego` and not `autocert`.
Do not replace it without a strong reason — the challenge handler and the PEM file layout are ours
either way.

Decided already (M1.1): configuration is parsed with `gopkg.in/yaml.v3`, because the loader needs
strict unknown-field rejection and the owner edits the file by hand. Rejected: JSON (no comments, no
comfortable hand-editing) and a hand-written YAML subset (a parser nobody asked for). The dependency
is a leaf: nothing else imports it.

## 9. Git workflow

- Never commit: `references/`, build output, `.dev/`, lab credentials, `*.pem`, `*.key`, SQLite
  files, or logs.
- Commit messages: Conventional Commits (`feat(uv): …`, `fix(mn/probe): …`, `docs: …`). One logical
  change per commit.
- Do not force-push shared branches. Do not amend someone else's commit.
- Tags are releases. `v0.1.0` exists; a new release is a tag plus `go run ./deploy/release`, and
  pushing the tag is what publishes it (`.github/workflows/release.yaml`). Never move or delete a
  published tag, and never retag a commit that was already released: a version that means two
  different trees is worse than a wrong one.

## 10. Definition of done

A task is done when:

1. `gofmt`/`goimports` clean, `go vet` clean, `golangci-lint` clean.
2. `go test ./cmd/... ./internal/...` passes; new logic has tests; `-race` passes for touched packages.
3. The relevant lab or Compose scenario passes, or the task explicitly says which scenario is
   deferred and why.
4. Docs and `--help` reflect the change.
5. No secret, lab address or reference copy is in the diff.

## 11. Stop and ask

Ask before proceeding when a change would:

- alter the credential derivation, the API contract, or the state machine;
- add a third-party runtime dependency, a cgo requirement, or a new long-running service;
- require host firewall/routing changes, or anything that could affect non-proxied traffic;
- change what `uv uninstall` deletes;
- contradict `docs/DESCRIPTION.md` — the document wins until we agree to change it.

## 12. Anti-patterns (each is a defect from the reference implementations)

- `curl | bash` from an unpinned branch at install time. Pin versions; verify checksums.
- Interactive prompts in the install path. Flags and environment, `--yes` for unattended.
- Regenerating credentials or keys on re-run. Generate once, persist, reuse.
- `systemctl restart xray` as the answer to every change. Hot path first, restart as fallback.
- Treating "the port is listening" as "the proxy works". Probe end to end.
- Blocking a node because *our* prober failed.
- One shared UUID/credential for all clients, or a subscription token that doubles as a password.
- World-readable private keys, tokens in URLs that end up in proxy logs, secrets in error strings.
- Hardcoded domains, ports, paths and `Debian`-only assumptions scattered through the code. One
  config struct, validated at startup.
- Host firewall or `ip rule` surgery to make WARP work. If it needs routing changes, it is wrong.
