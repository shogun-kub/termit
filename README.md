# Termit

A cooperative proxy mesh for people who own a small VPS.

Each participant runs **UV** on their own Debian box: it becomes a disguised VLESS over XHTTP+TLS
endpoint with a generated decoy website, opinionated traffic rules, and optional Cloudflare WARP
egress. A small self-hosted **MN** verifies that every node actually works (through the tunnel, and
with a valid certificate on the decoy), keeps the membership list, hands out per-pair credentials, and
serves each owner one subscription containing every healthy node in the mesh.

Status: **pre-implementation**. The design and plan are written; no code yet.

## Documents

| Document | Contents |
|---|---|
| [`docs/DESCRIPTION.md`](docs/DESCRIPTION.md) | The design: architecture, credential model, state machine, protocol, invariants, security. |
| [`docs/IMPLEMENTATION_PLAN.md`](docs/IMPLEMENTATION_PLAN.md) | Milestones, tasks, acceptance criteria, risks, effort. |
| [`docs/QUESTIONS.md`](docs/QUESTIONS.md) | Decisions taken and questions still open. |
| [`AGENTS.md`](AGENTS.md) | Rules for anyone (human or agent) changing this repository. |

## Shape of the system

- `termit-uv` — installer, agent and CLI for one node (Debian 13, amd64/arm64).
- `termit-mn` — management server and admin CLI (single binary, SQLite, built-in ACME).

Planned topology on a node: the agent owns `:80` (ACME + redirect) and serves the decoy site on a unix
socket; Xray owns `:443`, terminates TLS, speaks VLESS+XHTTP, and falls back to the decoy for
everything else. Nothing on the host is re-routed, so SSH and any other service keep working.

## Building

Requires Go (version pinned in `go.mod`). The runtime targets Linux; the code builds anywhere.

```bash
go build ./...
go test ./...
go run ./cmd/uv --help
go run ./cmd/mn --help
```

The development host currently has no Go toolchain installed; see `AGENTS.md` §4.1.

## References

`references/` holds two third-party projects (`autoXRAY-multi`, `Blitz`) kept for study only. They are
GPL-3.0, are **excluded from this repository**, and their code is not copied. See `AGENTS.md` §2.1.

## Licence

AGPL-3.0-only. See [`LICENSE`](LICENSE).
