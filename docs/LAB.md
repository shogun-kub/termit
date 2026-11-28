# Termit — the acceptance lab

Three Debian machines over SSH (one MN, two UVs), optionally a fourth UV for the mesh tests. The lab is a
manual gate, not part of `go test ./cmd/... ./internal/...`; `AGENTS.md` §4.3 points here.

## 1. What the tests read

Their coordinates never go into the repository — they are environment input:

```bash
export TERMIT_LAB_MN=root@…   TERMIT_LAB_UV1=root@…   TERMIT_LAB_UV2=root@…   TERMIT_LAB_UV3=root@…
export TERMIT_LAB_MN2=root@…                                  # the second server, for replication
export TERMIT_LAB_SSH_KEY=<path to the lab private key>       # never committed
export TERMIT_LAB_HOSTKEYS='host1=SHA256:…,host2=SHA256:…'    # pin every machine
export TERMIT_LAB_MN_ADMIN_TOKEN=<admin token>                 # never committed
export TERMIT_LAB_MN2_ADMIN_TOKEN=<the second server's token>  # its own; only for the mesh drills
export TERMIT_LAB_DNS_SUFFIX=lan
export TERMIT_LAB_ACME=https://…/dir                           # a test ACME directory
export TERMIT_LAB_PLINK='C:\Program Files\PuTTY\plink.exe'     # only when not on PATH
export TERMIT_LAB_PSCP='C:\Program Files\PuTTY\pscp.exe'
go test -tags=lab ./test/lab -v -timeout 30m
```

Run it with `-count=1`: `go test` caches a pass and reprints it in seconds, proving nothing about the
build in front of you.

`TERMIT_LAB_MN2` is optional and skips the tests that need a mesh. That server has to be prepared by hand
first: it holds a copy of the same `master.key`, each server lists the other under `mesh.peers` with the
peer's leaf pinned, and both run the same build — `OPERATIONS.md` §3.4 is the runbook. The harness only
addresses it, it does not build the mesh.

The replication drills (`TestDrillReplicationPeerDown`, `…Partition`, `…Restart`, `…SnapshotOnGap`,
`…ReadOnlySync`, `…Handoff`, `…AdoptGoneHome`, `…EpochRotation`, `…AdoptMissesNewNode`, `…SplitMerge`) need
the pair and its own admin token. They break one thing each — the replica stopped, the link cut in both
directions with `nft`, the process killed, a journal entry deleted so the replica's cursor no longer fits, a
node pointed at the replica while its home is alive, a node handed to the other server and back, a home
killed so the replica adopts one of its nodes, the pair credentials rotated, a node enrolled while the
replica was blind, a node adopted on one side and blocked on the other while the mesh was split — and assert
that the mesh converges afterwards without an operator. The gap drill edits the home's database and the
read-only drill compares sighting timestamps as stored (a minute is too coarse), so both skip without
`sqlite3`. The handoff and adoption drills carry the agent key a command prints; the harness redacts it from
every line it may print. The rotation cannot be undone — it leaves the lab's epoch one higher on every run,
which is what a rotation is. The split-merge drill ends with the node handed back by an operator: the merge
picks the higher home term and the loser stops writing, but nothing moves a node home by itself.

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

The opt-in `proxy` transport cannot run there at all: the `warp-cli` sidecar needs a KVM VM or a privileged
container, so the suite covers the default `wireguard` transport, which needs no device.

## 4. Rules of engagement

Tests are re-runnable and tolerate a previous failed run; never leave a machine needing manual repair (on
failure dump the logs and a `termit-uv doctor` report into the output); never put lab addresses, keys,
domains or ACME URLs in the repository; destructive actions (uninstall, reboot) only in tests explicitly
tagged for them. **A red test is repeated once, three minutes later, before it is investigated.** The
lab's egress and GitHub's release CDN hand out TLS and read timeouts that do not reproduce — a probe whose
worker listened but whose SOCKS handshake timed out is the usual shape — and a single re-run tells that
apart from a defect for the price of three minutes. Only a failure that repeats is a bug; quote both runs
when it does.
