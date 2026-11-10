# Termit

A cooperative proxy mesh for people who own a small VPS.

Each participant runs **UV** on their own Debian box: it becomes a disguised VLESS over XHTTP+TLS
endpoint with a generated decoy website, opinionated traffic rules, and Cloudflare WARP egress (on by
default, one flag to turn it off). A small self-hosted **MN** verifies that every node actually works —
through the tunnel,
with a valid certificate, from its own network — keeps the membership list, hands out per-pair
credentials, and serves each owner one subscription containing every healthy node in the mesh.

Status: milestones M1–M3 are implemented and verified on a three-machine lab, and M4.1 (WARP) is in:
the mode, the routing, the fail-open policy and the refusal path are tested, while the tunnel itself
needs a host with `/dev/net/tun`, which the lab does not have. A tagged release is still ahead. See
[What is not here yet](#what-is-not-here-yet).

## Documents

| Document | Contents |
|---|---|
| [`docs/DESCRIPTION.md`](docs/DESCRIPTION.md) | The design: architecture, credential model, state machine, protocol, invariants, security. |
| [`docs/IMPLEMENTATION_PLAN.md`](docs/IMPLEMENTATION_PLAN.md) | Milestones, tasks, acceptance criteria, risks, effort. |
| [`docs/QUESTIONS.md`](docs/QUESTIONS.md) | Decisions taken and questions still open. |
| [`AGENTS.md`](AGENTS.md) | Rules for anyone (human or agent) changing this repository, including the lab setup. |

## Shape of the system

- `termit-uv` — installer, agent and CLI for one node (Debian 13, amd64/arm64).
- `termit-mn` — management server and admin CLI (single binary, SQLite, built-in ACME).

### How one node is put together

Nothing on the host is re-routed: SSH, the decoy site and the ACME challenge keep working through
every code path, and Termit never touches the routing table or the firewall.

- **nginx** owns `:80` and `:443`. It answers ACME HTTP-01 from a webroot, redirects plain HTTP,
  serves the generated decoy site, and proxies only the node's secret XHTTP path to the core.
- **`termit-xray.service`** runs the proxy core on loopback (`127.0.0.1:8443`, management API on
  `127.0.0.1:10085`). The core is never exposed directly.
- **`termit-uv-agent.service`** runs the node's daily cycle: local self-check, `uv sync` against the
  management server, and reconciliation of the core's client set. `termit-uv-renew.timer` renews the
  certificate; the agent reports the new leaf on its next sync so the server's probe pin follows it.
- **`warp-cli`** runs in proxy mode and listens on `127.0.0.1:40000`. The core sends proxied traffic
  there (`warp.mode: all` by default); the host's own traffic never enters it. A node without
  `/dev/net/tun` says so and exits directly, and a WARP that dies mid-life is bypassed within half a
  minute by the core's own balancer.
- Config lives in `/etc/termit/uv/config.yaml`, state in `/var/lib/termit/uv/`, public content in
  `/var/www/termit/`, binaries in `/usr/lib/termit/bin/`.

The management server keeps its own world in `/etc/termit/mn/` and `/var/lib/termit/mn/`. Its admin
CLI talks to the running server over a unix socket (`/run/termit/mn/admin.sock`) and never opens the
database behind the server's back.

## Building and testing

Requires the Go version pinned in `go.mod`. The runtime targets Linux; the code builds anywhere.

Package patterns are explicit on purpose: `references/` holds third-party Go source inside this
module's directory tree, and `./...` would compile it.

```bash
go build ./cmd/... ./internal/...                # compile everything we own
go test ./cmd/... ./internal/...                 # unit tests
go vet ./cmd/... ./internal/...                  # cheap correctness pass
gofmt -l cmd internal test                       # must print nothing

CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath ./cmd/...
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath ./cmd/...
```

Acceptance runs against real machines over SSH: three Debian boxes (one MN, two UVs), optionally a
fourth UV for the mesh test. The suite covers the three scenarios in `docs/DESCRIPTION.md` §7, the
three-node mesh, and the failure drills of `docs/IMPLEMENTATION_PLAN.md` M3.6 — including a
management server outage, a broken prober, a wrong DNS answer, an expired certificate, a locked
database and a full disk.

```bash
export TERMIT_LAB_MN=root@mn.example.lan   # and the rest of AGENTS.md §4.3
go test -tags=lab ./test/lab -v -timeout 45m
```

Without those variables the lab suite skips: it is a manual gate, not part of `go test ./cmd/...
./internal/...`. The maintainer's Windows host has a portable Go toolchain under `.spike-out/tools/go`
and needs the environment from [`AGENTS.md`](AGENTS.md) §4.1.

## What is not here yet

- **WARP against a real tunnel**: the lab has no `/dev/net/tun`, so what the suite proves is the
  refusal, the generated route, fail-open (traffic keeps flowing while the sidecar port is dead) and
  the switch back. Installing `cloudflare-warp`, registering and connecting is implemented but its
  acceptance needs a KVM VM or a privileged container.
- **A bootstrap installer** (`deploy/install/uv.sh`) with checksum verification and an upgrade path
  that preserves the node's identity.
- **Operator documentation**: `docs/OPERATIONS.md`, `docs/PROTOCOL.md`, `docs/THREAT_MODEL.md`.
- **Release artifacts**: no tags yet; goreleaser, checksums and the release workflow arrive with the
  first tag.
- **A Docker Compose harness** for developers without lab hardware. The lab is the acceptance gate.

## References

`references/` holds two third-party projects (`autoXRAY-multi`, `Blitz`) kept for study only. They are
GPL-3.0, are **excluded from this repository**, and their code is not copied. See `AGENTS.md` §2.1.

## Licence

AGPL-3.0-only. See [`LICENSE`](LICENSE).
