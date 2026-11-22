# Termit — the acceptance lab

Three Debian machines over SSH (one MN, two UVs), optionally a fourth UV for the mesh tests. The lab is a
manual gate, not part of `go test ./cmd/... ./internal/...`; `AGENTS.md` §4.3 points here.

## 1. What the tests read

Their coordinates never go into the repository — they are environment input:

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

Run it with `-count=1`: `go test` caches a pass and reprints it in seconds, proving nothing about the
build in front of you.

## 2. Coordinates and credentials

Concrete coordinates for this machine live in the gitignored `.spike-out/lab.md`; the key is a PuTTY
`.ppk`, so the harness calls `plink`/`pscp` and pins each machine. A missing variable skips the suite.
Beside `lab.md` sit the credentials it lists (`github-token`, `mn-admin.token`, `sonar-token`) and the
scripts that use them; none may appear in the repository, a document, a commit message or a log line —
this repository is public.

## 3. Budget, and what cannot be tested there

UV = 1 vCPU / 1 GB RAM / 10 GB disk, MN = 2 vCPU / 2 GB, unprivileged LXC with **no `/dev/net/tun`**. A
lab's egress may block the WARP API — the block this project routes around — so WARP egress is proved in a
production pass (`docs/RELEASE.md` §5) and the lab proves the degradation: traffic keeps flowing and the
reason is reported. Everything else — TLS, ACME against a local directory, the mesh scenarios, resource
usage — is testable.

## 4. Rules of engagement

Tests are re-runnable and tolerate a previous failed run; never leave a machine needing manual repair (on
failure dump the logs and a `termit-uv doctor` report into the output); never put lab addresses, keys,
domains or ACME URLs in the repository; destructive actions (uninstall, reboot) only in tests explicitly
tagged for them. **A red test is repeated once, three minutes later, before it is investigated.** The
lab's egress and GitHub's release CDN hand out TLS and read timeouts that do not reproduce — a probe whose
worker listened but whose SOCKS handshake timed out is the usual shape — and a single re-run tells that
apart from a defect for the price of three minutes. Only a failure that repeats is a bug; quote both runs
when it does.
