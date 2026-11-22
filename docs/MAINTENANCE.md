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
caches, `lab.md`/`prod.md` and the credentials beside them), and refuses a directory holding its own
`.git`. `--dry-run` lists what would go.

Cache flags separate by cost: `--build-cache` drops compiled output (the next build recompiles and
downloads nothing), `--module-cache` drops downloaded modules, `--all` both, `--toolchain` Go itself. Build
the `clean` command outside the build cache first — `go build -o /tmp/clean ./deploy/clean` — because
`go run` executes it from inside the directory it is about to delete.

## 3. SonarQube

The scanner is LAN-only, so CI cannot reach it. The scan runs on the lab MN — `/root/sonar/analyse.sh`
pulls `dev`, runs the coverage step and the scanner (coordinates in `.spike-out/lab.md`). The same two
commands work anywhere with Go and a JRE:

```bash
go test -coverprofile=coverage.out ./cmd/... ./internal/...
SONAR_TOKEN=… sonar-scanner -Dsonar.host.url=<server>
```
