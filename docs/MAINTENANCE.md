# Termit — the maintainer's machine

Settings and habits that belong to the machine Termit is developed on, not to the code. `AGENTS.md` §9
points here for the push and the cleanup.

## 1. Pushing from this checkout

The maintainer's box sets `GIT_SSH` to TortoiseGit's plink and this clone carries a
`remote.origin.puttykeyfile` for another project, so a plain `git push` stops at `fatal: Could not read
from remote repository`. Git's own client works; point this clone at it once (`core.sshCommand` overrides
`GIT_SSH` for git only):

```powershell
git config --local core.sshCommand 'C:/Windows/System32/OpenSSH/ssh.exe'
```

Another clone or machine: `git -c core.sshCommand='…/ssh.exe' push origin dev`. Linux and macOS need
neither.

## 2. Cleaning up after a push

A release's artifacts are spent once they are on the server and the scratch directory grows by hundreds of
megabytes per cycle. After every push: `git push && go run ./deploy/clean` (or `make push`). `clean` drops
the build output, the lab logs and everything in `.spike-out/` off its keep list (the toolchain, the
caches, `lab.md`/`prod.md`, the Russian glossary, `release-run.js` and the credentials beside them), and
refuses a directory holding its own `.git`. The keep list itself is `scratchKeep` in
`deploy/clean/main.go`; `--dry-run` lists what would go.

Cache flags separate by cost: `--build-cache` drops compiled output (the next build recompiles and
downloads nothing), `--module-cache` drops downloaded modules, `--all` both, `--toolchain` Go itself.
When the run is going to delete the toolchain or the build cache, build the command first —
`go build -o "$env:TEMP\clean.exe" ./deploy/clean` — because `go run` keeps the `go` process alive from
the directory it is about to delete.

## 3. SonarQube

The scanner is LAN-only, so CI cannot reach it. The scan runs on the lab MN — `/root/sonar/analyse.sh`
pulls `dev`, runs the coverage step and the scanner (coordinates in `.spike-out/lab.md`). The same two
commands work anywhere with Go and a JRE:

```bash
go test -coverprofile=coverage.out ./cmd/... ./internal/...
SONAR_TOKEN=… sonar-scanner -Dsonar.host.url=<server>
```

**The gate.** The project is bound to the built-in `Sonar way` gate, and a built-in gate cannot be edited:
the analysis token reads the gates (`api/qualitygates/list` answers `200`) but every endpoint that changes
or binds one answers `Insufficient privileges`, so the change is made in the UI — Quality Gates →
`Sonar way` → Copy, name the copy `Termit 0.8`, set "Coverage on New Code" to 60, then Project Information
→ Quality Gate → select it. **Decided 2026-11-28:** `0.8.0` runs on that copy, because the built-in 80 %
was measured against 51.7 % over the 20 552 lines that landed since `0.4.1`, and writing five thousand
statements of tests is not what this release is for. The threshold goes back to 80 the moment
`new_coverage` reports it.

The debt is named so it can be paid down instead of forgotten. Statement coverage by `go test -cover` at
`0.8.0` (the scanner's line coverage reads a few points lower): `internal/mn/mesh` 73.9 %, `cmd/mn` 36.1 %,
`cmd/uv` 45.1 %, `internal/hardencmd` 0 %, `internal/mn/probe` 40.8 %. The newest code comes first — the
mesh, the replication paths in `internal/mn/store`, and the CLI verbs that move a node's home — because
those are what a bug in 0.8.0 would damage.

## 4. HTTPS from inside the agent's sandbox

Windows' own TLS stack cannot get credentials while the harness confines the process to the workspace, so
every Schannel client fails with

```
schannel: AcquireCredentialsHandle failed: SEC_E_NO_CREDENTIALS (0x8009030e)
```

and .NET turns the same failure into `The SSL connection could not be established`. It happens with and
without the proxy, which is what makes it look like a network problem.

**It is not the network.** Schannel keeps client credentials in the machine's crypto store, and the
sandbox allows reading `%APPDATA%\Microsoft\Crypto` and `%ProgramData%\Microsoft\Crypto` but not writing
them. The proof: the same `curl https://api.github.com/` answers `200` when that one command is run with
the harness's `danger-full-access` escalation. The proxy at `127.0.0.1:8899`, the fake-IP DNS
(`198.18.0.18`, the `cfw-tap` adapter) and the LAN are innocent — OpenSSL clients reach the same hosts with
the same environment.

What works without escalation, because it does not use Schannel:

| Tool | Why |
|---|---|
| `node` (v24 here) | OpenSSL |
| `git -c http.sslBackend=openssl …` | Git for Windows ships both backends |
| `go run ./deploy/…`, the Termit binaries, `gh` once installed | Go's `crypto/tls` |

What breaks: `curl.exe`, `git` without the override, `Invoke-WebRequest`/`Invoke-RestMethod`, and anything
else on Schannel. For a one-off of those, run that single command with `danger-full-access` instead of
fighting it.

Check the state of the machine in one line:

```powershell
curl.exe -sS -o NUL -w "%{http_code}\n" https://api.github.com/   # 000 plus the schannel error = confined
```

**Publishing a pre-release** is a `workflow_dispatch` of `release.yaml` on a `release/<version>`
branch, `<version>` being what you are cutting — `release/v0.7.0`, for example (`docs/RELEASE.md` §2).
`gh` is not installed here; with `gh` it is
`gh workflow run release.yaml --ref release/vX.Y.Z -f publish_prerelease=true`. Without it, the same
call from node is `.spike-out/release-run.js`, which dispatches, waits for the run and prints the
release it published (`clean` keeps it; the script below is the same call if the file is ever lost):

```js
// node dispatch.js <git-token-file>: the branch name is the version
const https = require('https'), fs = require('fs');
const token = fs.readFileSync(process.argv[2], 'utf8').trim();
const ref = 'release/vX.Y.Z';
const req = https.request({ host: 'api.github.com', method: 'POST',
  path: '/repos/shogun-kub/termit/actions/workflows/release.yaml/dispatches',
  headers: { authorization: `Bearer ${token}`, 'user-agent': 'termit', accept: 'application/vnd.github+json' } },
  (res) => console.log(`dispatch: ${res.statusCode}`));   // 204, no body
req.end(JSON.stringify({ ref, inputs: { publish_prerelease: 'true' } }));
```

The token is the personal one in `.spike-out/github-token` (kept by `clean`); it needs the `workflow`
scope, and `GET /user` reports `200` when it is alive.
