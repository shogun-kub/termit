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
fails with a permission error rather than a compile error.

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
go run ./deploy/clean --build-cache   # and the compiled output (the next build recompiles, downloads nothing)
# SonarQube is LAN-only: the scan runs on the lab MN (/root/sonar/analyse.sh); the same two commands work
# anywhere with Go and a JRE.
go test -coverprofile=coverage.out ./cmd/... ./internal/...
SONAR_TOKEN=… sonar-scanner -Dsonar.host.url=<server>
```

Windows: PowerShell equivalents or WSL; no `make`-only workflow. A `Makefile` may exist, but every
target must also be documented as a plain `go` command here.

**Never run `go mod tidy` at the repository root**: it walks `references/Blitz/...` and adds the MongoDB
driver plus a dozen transitive dependencies. Tidy a copy that holds only our code: copy `go.mod`, `cmd`
and `internal` into `.spike-out/tidy`, run `go mod tidy` there, copy `go.mod`/`go.sum` back.

### 4.3 Acceptance lab

Three Debian machines over SSH (one MN, two UVs), optionally a fourth UV for the mesh tests. Their
coordinates never go into the repository — they are environment input:

```bash
export TERMIT_LAB_MN=root@…   TERMIT_LAB_UV1=root@…   TERMIT_LAB_UV2=root@…   TERMIT_LAB_UV3=root@…
export TERMIT_LAB_SSH_KEY=<path to the lab private key>       # never committed
export TERMIT_LAB_HOSTKEYS='host1=SHA256:…,host2=SHA256:…'    # pin every machine
export TERMIT_LAB_MN_ADMIN_TOKEN=<admin token>                 # never committed
export TERMIT_LAB_DNS_SUFFIX=lan
export TERMIT_LAB_ACME=https://…/dir                           # a test ACME directory
export TERMIT_LAB_PLINK='C:\Program Files\PuTTY\plink.exe'     # only when not on PATH
export TERMIT_LAB_PSCP='C:\Program Files\PuTTY\pscp.exe'
go test -tags=lab ./test/lab -v -timeout 30m
```

Run it with `-count=1`: `go test` caches a pass and reprints it in seconds, proving nothing about the build
in front of you.

Coordinates for this machine live in the gitignored `.spike-out/lab.md`; the key is a PuTTY `.ppk`, so the
harness calls `plink`/`pscp` and pins each machine. A missing variable skips the suite — the lab is a
manual gate, not part of `go test ./cmd/... ./internal/...`. Beside `lab.md` sit the credentials it lists
(`github-token`, `mn-admin.token`, `sonar-token`) and the scripts that use them; none may appear in the
repository, a document, a commit message or a log line — this repository is public.

Budget: UV = 1 vCPU / 1 GB RAM / 10 GB disk, MN = 2 vCPU / 2 GB, unprivileged LXC with **no
`/dev/net/tun`**. A lab's egress may block the WARP API — the block this project routes around — so WARP
egress is proved in a production pass (§9) and the lab proves the degradation: traffic keeps flowing and
the reason is reported. Everything else — TLS, ACME against a local directory, the mesh scenarios,
resource usage — is testable.

Rules: tests are re-runnable and tolerate a previous failed run; never leave a machine needing manual
repair (on failure dump the logs and a `termit-uv doctor` report into the output); never put lab
addresses, keys, domains or ACME URLs in the repository; destructive actions (uninstall, reboot) only in
tests explicitly tagged for them. **A red test is repeated once, three minutes later, before it is
investigated.** The lab's egress and GitHub's release CDN hand out TLS and read timeouts that do not
reproduce — a probe whose worker listened but whose SOCKS handshake timed out is the usual shape — and a
single re-run tells that apart from a defect for the price of three minutes. Only a failure that repeats
is a bug; quote both runs when it does.

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

See §10 for the list, and §9.1 when the change is going into a release. Two rules it does not repeat: a
golden-file update **only** when the generated config genuinely changed, with the reason in the commit; and
no test that needs the public internet or the lab in the default `go test` run.

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
- **`release/<version>`** is created from `dev` when a release is planned; the branch name **is** the
  version (`release/v0.2.0`). Cherry-picks from `dev` and small fixes committed on the branch are
  allowed. There is no version file to edit — `deploy/release --version v<version>` stamps the binaries,
  so the branch name, the tag and `--version` must agree.
- **Pre-release** is started by hand: `workflow_dispatch` of `.github/workflows/release.yaml` on a
  `release/<version>` branch with `publish_prerelease` set. It builds both binaries, `uv.sh`, the units
  and `SHA256SUMS`, and may publish them as a pre-release named **`v<version>p<n>`** — `v0.3.2p1` is the
  first build of `0.3.2` from its release branch, `v0.3.2p2` the second, and `n` never resets for a
  version: the existing tags are the counter. Nothing is published from `dev` or `main`. The artifacts
  carry the pre-release version itself, so `uv.sh --version v0.3.2p1` installs exactly that build, and
  the changelog section of `0.3.2` supplies its notes.
- **Final release:** merge `release/<version>` into `dev` and into `main`, delete the branch, then create
  an annotated tag `v<version>` on `main`. Pushing the tag runs the final build and publishes the GitHub
  Release. The release stays marked pre-release until that release's own production pass is recorded in
  `docs/IMPLEMENTATION_PLAN.md` (§3.3.1 for `0.2.1`, §3.4.11 for `0.3.2`); flipping the flag is a deliberate,
  recorded act. **Flipping `prerelease` does not move the "latest" pointer.** A release published as a
  pre-release keeps `make_latest` on the previous one, so `releases/latest` — and therefore `uv.sh` and
  `mn.sh` without `--version` — go on offering the old build. Patch the release with `make_latest: "true"`
  after the flip; the API answers immediately and the `github.com/.../releases/latest` redirect lags
  minutes behind it while its cache expires.
- **The release body is the changelog, from that version back to the last release** — `--notes` prints the
  section; for a release (never a `v<version>p<n>` build) `--since <last release>` prints every section
  down to, but not including, it, because the versions in between existed only as pre-releases. The
  section must exist in `CHANGELOG.md` and `CHANGELOG.ru.md` with a real date before tagging (the tool
  refuses a placeholder — §9.1).
- **A release that touches the network is proved on production before it is tagged.** Anything under
  `internal/uv/xray`, `internal/uv/warp` or the network half of `internal/uv/install` depends on networks
  we do not control. A pre-release goes onto the production nodes first, and what is observed there is the
  egress the node really uses — `warp status`, the tunnel's exit, what a client sees — not the one the
  code intends; only then does the tag exist. Local code (decoy, terminal, docs) may ship on CI and the
  lab alone.
- **An accepted advisory is a debt with a due date.** `govulncheck` has no exclusion flag, so
  `deploy/vulngate` is the gate: it fails on any finding not named in `deploy/vulnerabilities.json` and
  prints the action for each entry. An entry carries `must_be_gone_by` — the first release that must not
  see it — and `deploy/release` refuses that version while it is there. **Before every release: update the
  module, delete the entry.** Moving the deadline is a deliberate act with a reason in the commit, not a way
  to silence the reminder.
- **A tag points at a commit that has already been tested.** Retagging is allowed only while the release
  is unpublished; `v0.1.0` is published and stays where it is. Never retag a released commit — a version
  that means two different trees is worse than a wrong one.
- **`push --force` is forbidden on `main` and `dev`.** Do not amend someone else's commit.

Commit hygiene: Conventional Commits (`feat(uv): …`, `fix(mn/probe): …`, `docs: …`), one logical change
per commit. Never commit `references/`, build output, `.dev/`, lab credentials, `*.pem`, `*.key`, SQLite
files or logs.

**After pushing, clean up**: `git push && go run ./deploy/clean` (or `make push`). A release's artifacts are
spent once they are on the server and the scratch grows by hundreds of megabytes per cycle; `clean` drops
the build output, the lab logs and everything in `.spike-out/` off its keep list (the toolchain, the
caches, `lab.md`/`prod.md` and the credentials beside them), and refuses a directory holding its own
`.git`. `--dry-run` lists what would go.

**Pushing from this checkout**: the maintainer's box sets `GIT_SSH` to TortoiseGit's plink and this clone
carries a `remote.origin.puttykeyfile` for another project, so a plain `git push` stops at
`fatal: Could not read from remote repository`. Git's own client works; point this clone at it once
(`core.sshCommand` overrides `GIT_SSH` for git only):

```powershell
git config --local core.sshCommand 'C:/Windows/System32/OpenSSH/ssh.exe'
```

Another clone or machine: `git -c core.sshCommand='…/ssh.exe' push origin dev`. Linux and macOS need
neither.

Cache flags separate by cost: `--build-cache` drops compiled output (the next build recompiles and
downloads nothing), `--module-cache` drops downloaded modules, `--all` both, `--toolchain` Go itself. Build
the `clean` command outside the build cache first — `go build -o /tmp/clean ./deploy/clean` — because
`go run` executes it from inside the directory it is about to delete.

### 9.1 Checklist before a release

Every item here exists because it was missed once. Run them in order; the ones with a command are meant to
be run, not remembered.

**1. Freeze the scope.** `git log --oneline v<last>..dev` and make every commit appear in this release's
section of `CHANGELOG.md` — and in `CHANGELOG.ru.md`, same facts, same order. The tool refuses a missing
section, a section that still says `unreleased`, and a placeholder date.

**2. Document what changed, in every place it shows.** A feature is not done until its surface is written
down; the surface is wider than the command:

| Added or changed | Also update |
|---|---|
| a command, a flag, a default | the `<binary> --help` summary line and its command block, the `docs/guide/using-*.md` table, the `docs/OPERATIONS.md` reference |
| a menu entry, a picker, a prompt, a label | `docs/guide/menu.md` (en and ru), `internal/i18n` for both languages, the screen mock in that guide if it shows the line |
| a config key or its default | the struct comment, `DESCRIPTION.md`, `OPERATIONS.md`, the guide's row |
| a rendered page or a printed block | the sample in the guide and `README` if it carries one |
| a port, a unit, an outbound host | `OPERATIONS.md`'s service table and `DESCRIPTION.md` §5.1, and §2.3 here if the host is new |
| an error a person may see | `docs/guide/troubleshooting.md` in both languages |

`--help` summaries are code and nobody reads them until an operator does: `invite` and `uv` carried
incomplete ones through a whole release.

**3. Make the README true.** `README.md` and `README.ru.md` are the project's face and the first thing
anyone reads: they name the newest release, list what works, and say what does not. Before tagging, read
both end to end against the release — the status line names this version and nothing later, every feature
claimed is in this release and not the next one, no claim is unverified (a pass that was never recorded does
not count), the quick-start commands run as written, "what is not here yet" is still true, and the two
languages say the same thing. A release left the README naming a version two releases back: it is a
five-minute read and the only document a stranger judges us by.

**4. Grep for the truth you just invalidated.** For every rename, removal or reversed decision, search the
tree, not the diff: `rg 'warp\.family|status and doctor|§1[01]\.' docs README* AGENTS.md`. Every hit is
either updated or still true, and the answer goes in the commit message if it is not obvious. The audit
after `0.7.0` found five stale claims that no test could see — a key documented as ignored when the loader
refused it, a diagnostic that did not exist, a forwarded command that could not work.

**5. Renumbering is a change to every document that points at you.** Moving a section in
`docs/IMPLEMENTATION_PLAN.md` breaks `§x.y` in `DESCRIPTION.md`, `OPERATIONS.md`, `RUNBOOK*.md`,
`QUESTIONS.md`, `SPIKES.md`, `AGENTS.md` and in the plan's own prose. Keep the old→new map, then
`rg '§\d' docs README* AGENTS.md` and fix each hit.

**6. Prove it on a machine, not only in tests.** Build both binaries for `linux/amd64` with `-ldflags`
version stamping, copy them to the lab, run the scenario, then put the lab back on a normal build. The
`0.7.0` run paid for itself twice: `upgrade` converged the node with `init` and no `--config` (the rollback
kept it serving), and the server's update asked `api.github.com`, which answered `403` where `github.com`
worked. Neither was reachable from a unit test, and both would have shipped.

**7. Prove the way back.** For anything that replaces a binary, a config or a unit: break the release on
purpose (a corrupted `SHA256SUMS`, a bad asset), run the swap, and watch the previous file return and the
service stay up. An unproven rollback is a rumour; the lab is where it is cheap.

**8. Prove the network on production.** A change under `internal/uv/xray`, `internal/uv/warp` or the network
half of `internal/uv/install` is not released on the lab's word: install the pre-release on the production
nodes, read `warp status`, the tunnel's exit and what a client actually sees, and record the pass. §9 above
says what counts as recorded.

**9. Make the versions agree.** Branch name, tag, `deploy/release --version` and the changelog heading are
one version; `p<n>` continues from the existing tags, never resets; `--notes --since <last release>` prints
every section since the last release, because the pre-releases in between were skipped by whoever installs
this one. Print the notes and read them before publishing.

**10. Run the gate deliberately.** `go run ./deploy/vulngate ./cmd/... ./internal/...`, then
`go run ./deploy/release --version v<version> --notes`. It refuses while an accepted advisory names this
version; clear it by moving the dependency, or move `must_be_gone_by` in a commit that says why.

**11. Publish, then move the pointer.** Tag `main`, publish, and check the release body. When the production
pass exists, flip `prerelease` **and** set `make_latest` — a flipped pre-release otherwise leaves
`releases/latest`, and therefore `uv.sh` and `mn.sh` without `--version`, on the previous build.

**12. Leave nothing behind.** No `<binary>.prev` on a machine you return to service, no lab host on a dev
build, no secret or address in a document, `go run ./deploy/clean` after the push, and the next release's
section absent from the changelog until it is written.

## 10. Definition of done

1. `gofmt`/`goimports`/`go vet`/`golangci-lint` clean; `go test ./cmd/... ./internal/...` passes (`-race`
   for touched packages) and new logic has tests for its failure and `unknown` branches.
2. The relevant lab or Compose scenario passes, or the task names the one deferred and why.
3. Docs and `--help` reflect the change (§9.1 is the release-wide sweep).
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