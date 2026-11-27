# AGENTS.md

Instructions for any human or AI agent changing this repository. Read this file completely first, then
[`docs/DESCRIPTION.md`](docs/DESCRIPTION.md) for the design.

## Language

The user mixes Russian and English freely; read either, never ask which to use.

| Where | Language |
|---|---|
| Chat replies | **Russian**, unless asked otherwise |
| Everything committed — `docs/`, `README`, comments, commit messages | **English** |
| `docs/guide/ru/`, `README.ru.md`, `CHANGELOG.ru.md` | **Russian** deliberately: the guide and the front pages are for Russian-speaking participants. Keep them in step with their English counterparts |
| Quoted material — error messages, log lines, UI strings | Original, unchanged |
| Terms of art — `patch`, `offset`, tool and API names | Original, never translated |

A Russian question does not make the write-up Russian: investigate in whichever language is clearer,
write the finding in English, explain it back in Russian.

## 0. Read first

| Document | Why |
|---|---|
| [`docs/DESCRIPTION.md`](docs/DESCRIPTION.md) | The design: goals, protocol, credential model, state machine, invariants |
| [`docs/IMPLEMENTATION_PLAN.md`](docs/IMPLEMENTATION_PLAN.md) | Milestones, and the task you are probably implementing |
| [`docs/QUESTIONS.md`](docs/QUESTIONS.md) | Decisions made and still open. Do not silently re-decide them |
| [`docs/MN-API.md`](docs/MN-API.md) | The server's API: paths (and the configurable prefix), shapes, problem codes, admin operations |
| [`docs/RELEASE.md`](docs/RELEASE.md) | before cutting a release: branches, the tag, what must be proved, the checklist |
| [`docs/LAB.md`](docs/LAB.md) | the acceptance lab: what the tests read, the budget, the rules |
| [`docs/MAINTENANCE.md`](docs/MAINTENANCE.md) | this checkout: pushing, `deploy/clean`, the caches, the scanner |

If a task contradicts these documents, stop and ask — do not "fix" the document to match the code.

## 1. What this repository is

Two Go binaries from one module. `termit-uv` (`cmd/uv`) installs and supervises a VLESS+XHTTP+TLS node
on a Debian box: Xray config, ACME certificate, generated decoy site, traffic rules, Cloudflare WARP
(on by default), daily sync with the management server. `termit-mn` (`cmd/mn`) is that server: node
registry, health prober, credential derivation, subscription rendering, admin CLI over a unix socket.
Target runtime Debian 13, amd64 and arm64; the code compiles anywhere, Linux-only behaviour sits behind
build tags.

## 2. Non-negotiable rules

### 2.1 `references/` is read-only input, never part of the product

`references/autoXRAY-multi` and `references/Blitz` are third-party GPL-3.0 projects kept for study.

- **Never modify, move, rename, format or "fix" anything under `references/`; never commit it** (it is
  gitignored — if you find it staged, unstage it).
- **Never copy code from them.** Read for behaviour, write idiomatic Go: no dependency, no linking, no
  shelling out. This project is AGPL-3.0 and those are GPL-3.0.

### 2.2 The safety invariants are contracts, not suggestions

From `docs/DESCRIPTION.md` §11, restated because they are the easiest thing to break:

1. **Never mutate host routing or firewall state.** No `ip route`, `ip rule`, iptables, nftables: WARP only
   as the core's userspace WireGuard outbound or a loopback SOCKS5 sidecar, never as a host tunnel.
2. **`unknown` is not `failed`.** A check that could not run (prober down, DNS error, missing binary,
   timeout) changes **no state**; only a genuine negative result moves a node.
3. **Validate before applying.** Atomic write, `xray -test`, then activate: hot update first, validated
   restart second, rollback on any failure.
4. **Idempotency.** A re-run converges and never regenerates issued credentials, XHTTP paths or
   certificates.
5. **No secrets** in logs, metrics, error text, config examples or tests — agent keys, tokens, UUIDs,
   private keys, ACME account keys.
6. **Direct traffic stays direct.** SSH, the decoy, ACME keep working through every code path; a change
   that could capture host traffic is wrong.

### 2.3 Autonomous actions are narrow

The agent may restart its own services and rewrite Xray config. It must never reboot the host, change
`sshd` or `/etc/network`, install packages outside the list in `internal/uv/install`, delete user data
outside `/var/lib/termit` and its own generated files, or contact any host other than the configured MN,
the configured ACME directory, and Cloudflare's WARP endpoint and package repository (only while WARP is
enabled).

## 3. Repository layout

```
cmd/uv, cmd/mn    the two binaries: subcommand dispatch and the interactive menu
internal/cred     pair-credential derivation (has test vectors — see §6)
internal/proto    API types, problem codes, vless URL build/parse, profile parameters
internal/version  version, build metadata, api_version constants
internal/uv/…     agent, install, xray (model, generator, hot API, `xray -test`), service (units,
                  nginx drop-in), web (nginx config), acme, warp, state
internal/mn/…     server (API, middleware, auth, rate limiting, masking site), store (SQLite schema,
                  queries, migrations), probe (xray-core worker, cert, DNS), sub, sched, admin
internal/decoy    deterministic masking-site generator + templates (node AND server)
internal/termui   the shared interactive front end: numbered menu, prompts, colour
deploy/systemd    installed copies of the units, for readers; the node's are rendered by
                  `internal/uv/service` because paths come from the configuration
deploy/install    bootstrap shell script and release checks
deploy/release    the release builder (`go run ./deploy/release`), which CI runs too
deploy/vulngate   the vulnerability gate CI runs; `deploy/vulns` is its list of accepted advisories
                  (`deploy/vulnerabilities.json`), each with the release that must not carry it
test/lab          SSH-driven acceptance suite (build tag `lab`); test/integration is a planned
                  Compose suite — the lab is the gate
docs, docs/guide  design and operations docs; `docs/guide/ru` is the Russian mirror
```

Shared code lives under `internal/` (one module, not a library). Do not create `pkg/` unless a package
is genuinely meant to be imported by another module.

## 4. Toolchain and commands

### 4.1 This development host

Portable **Go 1.27.4** under `.spike-out/tools/go` (gitignored, not on `PATH`). Start every shell with:

```powershell
$env:Path = "$(Get-Location)\.spike-out\tools\go\bin;" + $env:Path
$env:GOCACHE = "$(Get-Location)\.spike-out\gocache"
$env:GOMODCACHE = "$(Get-Location)\.spike-out\gomodcache"
$env:GOPATH = "$(Get-Location)\.spike-out\gopath"
```

The cache overrides are required: the file sandbox denies writes to `%LOCALAPPDATA%`, and a build then
fails with a permission error rather than a compile error. The same confinement breaks Windows' own TLS
stack — `curl.exe`, `git`'s default backend and .NET fail with `SEC_E_NO_CREDENTIALS` while `node`,
`git -c http.sslBackend=openssl` and every Go client work. Before blaming the network, read
[`docs/MAINTENANCE.md`](docs/MAINTENANCE.md) §4.

Build `golangci-lint` from source with this toolchain: a prebuilt binary refuses a newer module. Go is
pinned by the `go` directive, patch included (`go 1.27.4`), because `govulncheck` gates on the toolchain
that built the binary — do not add a `toolchain` line above CI's. **CGO is off** (`CGO_ENABLED=0`, pure-Go
SQLite): never add a cgo dependency. `.golangci.yml` says why `fmt.Fprint*` is outside `errcheck`.

### 4.2 Common commands

```bash
# Patterns are explicit: `references/` holds third-party Go source inside this module's tree.
go build ./cmd/... ./internal/...     # compile everything we own
go test ./cmd/... ./internal/...      # unit tests
go test -race ./internal/...          # race detector on library code
go vet ./cmd/... ./internal/...       # cheap correctness pass
go test ./deploy/...                  # the release tool, the vulnerability gate and the exclusion list
go run ./deploy/vulngate ./cmd/... ./internal/...   # what CI's govulncheck step runs, with the
                                      # accepted advisories from deploy/vulnerabilities.json
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0   # once per toolchain
"$(go env GOPATH)/bin/golangci-lint" run ./cmd/... ./internal/...           # lint (CI gate)
go run ./cmd/uv --help                # and: go run ./cmd/mn init --domain mn.localhost --data-dir ./.dev/mn
gofmt -l cmd internal deploy test     # must print nothing
goimports -l cmd internal deploy      # must print nothing
go run ./deploy/release --version v0.4.1 --out dist   # the release directory, as CI builds it
go run ./deploy/clean                 # drop the artifacts and the scratch; --dry-run lists them first
                                      # cache flags, the scanner and the push setup: docs/MAINTENANCE.md
```

Windows: PowerShell equivalents or WSL; no `make`-only workflow. A `Makefile` may exist, but every
target must also be documented as a plain `go` command here.

**Never run `go mod tidy` at the repository root**: it walks `references/Blitz/...` and adds the MongoDB
driver plus a dozen transitive dependencies. Tidy a copy that holds only our code: copy `go.mod`, `cmd`
and `internal` into `.spike-out/tidy`, run `go mod tidy` there, copy `go.mod`/`go.sum` back.

### 4.3 Acceptance lab

Three Debian machines over SSH (one MN, two UVs), optionally a fourth UV for the mesh tests. The lab is a
manual gate, not part of `go test ./cmd/... ./internal/...`. What the tests read, where the coordinates
live, the budget, what the lab cannot prove and the rules of engagement are in
[`docs/LAB.md`](docs/LAB.md). The short version: `go test -tags=lab -count=1 ./test/lab`, coordinates in
the gitignored `.spike-out/lab.md`, never in the repository, and a red test is repeated once before it is
investigated.

## 5. Coding conventions

### Go

- Standard library first; every dependency needs a reason (§8).
- `context.Context` on every function that does I/O; no `context.Background()` below `main`/tests.
- Errors: wrap with `%w`, add context, expose typed errors where callers must branch (`errUnknown`,
  `errCheckFailed`, `errNotDue`). Never branch on error strings.
- `log/slog`, structured, one logger injected from `main`, redaction through a central helper.
- No global mutable state; constructors return fully wired structs. Inject a `Clock` where behaviour
  depends on time; tests must not `time.Sleep`.
- Files: temp file in the same directory, `fsync`, `os.Rename` (helper in `internal/uv/state`). Never
  truncate a live config in place.
- Platform code behind build tags (`*_linux.go`) with a non-Linux stub returning a clear error.
- Doc comments on public identifiers; comments explain *why*, not *what*.
- Table-driven tests, `t.Parallel()` where safe, golden files under `testdata/`.

### Configuration and state

- YAML, parsed strictly (`KnownFields(true)`), versioned (`schema_version`); state files are migrated,
  never silently discarded.
- Paths: config in `/etc/termit/<component>/`, state in `/var/lib/termit/<component>/`, sockets in
  `/run/termit/<component>/`. One exception: generated *public* content (decoy tree, ACME webroot) lives
  under `/var/www/termit/`, because nginx reads it as an unprivileged user and cannot traverse `0750`.
- Files are `0600` (secrets) or `0644` (public), directories `0750`. Certificate keys are always `0600`
  — the reference implementation's `0744` is a bug we do not repeat.

### Xray config generation

- `internal/uv/xray` owns a typed model; generate JSON from it, never by string templating.
- Deterministic output: stable ordering, no timestamps, no random values; golden tests compare the whole
  document.
- Profile parameters live in `internal/proto/profile.go` so the client URI builder and the server config
  generator cannot drift; `ParseVLESSUrl(BuildVLESSUrl(x))` round-trips are mandatory for profile changes.
- After writing: `xray -test -config <path>`; on failure restore the previous file and return an error
  naming the failing line.

### Decoy site

- Templates are embedded (`go:embed`) and self-contained, referencing **no external hosts** (no CDN
  fonts, no analytics, no remote images): a decoy that phones home is a fingerprint.
- Output is deterministic from `(domain, seed, template)`; the seed is stored at install so the site
  survives restarts unchanged, and `termit-uv renew-site` regenerates the tree without leaving stale
  files.
- **nginx serves the tree** on a node; the agent never listens on `:80`/`:443`. The page, its nginx
  location and the fake-auth snippet come from the same template data, with a golden test covering both.
- **The management server serves its own site from memory** (`internal/mn/server/decoy.go`): it is the
  listener, so nothing is written to disk there. Same generator, different transport — do not give it a
  file tree to keep in step.

## 6. Domain rules that tests must enforce

1. `internal/cred` vectors are immutable once shipped. Changing the derivation changes every node's
   credentials: that is a migration with a new `credential_epoch`, not a refactor.
2. The state machine is a pure function table (`state, event → state, side effects`); every `unknown`
   path keeps the current state. Test the cross-product, not just the happy path.
3. Subscription output is ordered and stable (label, then id): same peers ⇒ byte-identical response and
   ETag.
4. The sync response is the complete desired peer set; `changes` is informational. UV reconciles — a
   missed response must never leave a stale client behind.
5. Never trust MN data blindly: validate UV ids, domains, ports, paths and UUID syntax before writing
   them into an Xray config.

## 7. What a change must include

See §10 for the list, and the checklist in `docs/RELEASE.md` §6 when the change goes into a release.
Three rules it does not repeat: a golden-file update **only** when the generated config genuinely changed,
with the reason in the commit; no test that needs the public internet or the lab in the default
`go test` run; and a new or changed CLI command or flag reaches the menu, or its absence is stated with a
reason in `docs/guide/menu.md` — the menu is a front end for the CLI, not a second, smaller one.
`TestEveryDocumentedCommandIsInTheMenuOrExplained` in both binaries fails when a command is in neither
place; the guide's "What the menu does not offer" is the other half of that contract.

## 8. Dependencies

Check in this order: standard library, `golang.org/x/…`, then a well-maintained module with a compatible
licence (Apache-2.0, MIT, BSD; **not** GPL/AGPL). Record the choice and the alternative you rejected, and
run `govulncheck` after adding. No vendoring; the module graph stays small enough for `go mod graph` to
be reviewable. Decided already — do not revisit without a strong reason:

- **ACME** (`SPIKES.md` S6): `golang.org/x/crypto/acme`, not `lego` and not `autocert`. The challenge
  handler and the PEM layout are ours either way.
- **Config** (M1.1): `gopkg.in/yaml.v3`, because the loader needs strict unknown-field rejection and the
  owner edits the file by hand. Rejected: JSON (no comments) and a hand-written YAML subset. It is a
  leaf dependency.

## 9. Branches and releases

`main` is what a participant installs; nothing else is. The process exists so the tree behind a tag has
already passed CI and the lab.

| Branch | Purpose | CI |
|---|---|---|
| `main` | stable releases only; receives merges from `release/<version>`, nothing else | build + tests, never publishes |
| `dev` | current development | build + tests, never publishes |
| `release/<version>` | stabilisation for one release, cut from `dev` | build + tests; the only branch a pre-release can be built from |

- **No direct commits to `main`.** Work lands on `dev` and reaches `main` by merging `release/<version>`.
- **The version is written in four places and they must agree**: the branch name, the tag,
  `deploy/release --version` and the changelog heading. There is no version file to edit.
- **`push --force` is forbidden on `main` and `dev`.** Do not amend someone else's commit.

The rest — pre-releases, the counter, the final tag, the "latest" pointer, what must be proved on
production, the advisory gate and the checklist we run before every tag — is
[`docs/RELEASE.md`](docs/RELEASE.md). Read it before cutting a `release/<version>`.

Commit hygiene: Conventional Commits (`feat(uv): …`, `fix(mn/probe): …`, `docs: …`), one logical change
per commit. Never commit `references/`, build output, `.dev/`, lab credentials, `*.pem`, `*.key`, SQLite
files or logs.

Machine settings and habits — pushing, `deploy/clean`, the caches, the LAN-only scanner — live in
[`docs/MAINTENANCE.md`](docs/MAINTENANCE.md). After every push: `git push && go run ./deploy/clean`.

## 10. Definition of done

1. `gofmt`/`goimports`/`go vet`/`golangci-lint` clean; `go test ./cmd/... ./internal/...` passes (`-race`
   for touched packages) and new logic has tests for its failure and `unknown` branches.
2. The relevant lab or Compose scenario passes, or the task names the one deferred and why.
3. Docs and `--help` reflect the change; `docs/RELEASE.md` §6 is the release-wide sweep.
4. No secret, lab address or reference copy in the diff.

## 11. Stop and ask

Ask before proceeding when a change would alter the credential derivation, the API contract or the state
machine; add a third-party runtime dependency, a cgo requirement or a new long-running service; require
host firewall or routing changes, or anything that could affect non-proxied traffic; change what
`termit-uv uninstall` deletes; or contradict `docs/DESCRIPTION.md` — the document wins until we agree to
change it.

## 12. Anti-patterns (each is a defect from the reference implementations)

- `curl | bash` from an unpinned branch at install time. Pin versions; verify checksums.
- Interactive prompts in the install path. Flags and environment, `--yes` for unattended.
- Regenerating credentials or keys on re-run. Generate once, persist, reuse.
- `systemctl restart xray` as the answer to every change. Hot path first, restart as fallback.
- Treating "the port is listening" as "the proxy works". Probe end to end.
- One shared UUID for all clients, or a subscription token that doubles as a password. Secrets in URLs
  that end up in proxy logs, in error strings or in world-readable keys.
- Hardcoded domains, ports, paths and `Debian`-only assumptions. One config struct, validated at startup.