# Termit

[![ci](https://github.com/shogun-kub/termit/actions/workflows/ci.yaml/badge.svg?branch=main)](https://github.com/shogun-kub/termit/actions/workflows/ci.yaml)

[English](README.md) · [Русский](README.ru.md)

A private proxy mesh for a small group of people who each own a VPS.

Every participant runs **termit-uv** on their own Debian box. It turns the box into a disguised
VLESS-over-XHTTP endpoint with a generated decoy website, a self-renewing certificate and opinionated
traffic rules — proxied traffic leaves through Cloudflare's WARP network by default, the box's own
traffic never does. One small **termit-mn** keeps the group honest: it holds the membership list,
probes every node from the outside, and hands each owner a single subscription URL containing every
healthy node, with a credential that is valid for exactly one pair of nodes.

```
   phone ──┐                       ┌── laptop
           │  VLESS / XHTTP + TLS  │
           ▼                       ▼
   ┌───────────────┐       ┌───────────────┐
   │ UV node A     │       │ UV node B     │        one box per participant,
   │ xray + nginx  │       │ xray + nginx  │        decoy site, own certificate
   │ WARP egress   │       │ WARP egress   │
   └───────┬───────┘       └───────┬───────┘
           └──────────┬────────────┘
                      ▼   daily sync, TLS 443
             ┌──────────────────┐
             │ MN               │  registry, prober, subscriptions
             │ never proxies    │  admin CLI over a unix socket
             └──────────────────┘
```

## Documentation

Start with the guide. It is written for the person installing a node, not for the maintainer, and there
is a Russian version beside every page.

| English | Русский | What it covers |
|---|---|---|
| [Quickstart](docs/guide/quickstart.md) | [Быстрый старт](docs/guide/ru/quickstart.md) | two machines to a working phone, the shortest path |
| [How Termit works](docs/guide/overview.md) | [Как устроен Termit](docs/guide/ru/overview.md) | the idea, the diagrams, what you need, the order of installation |
| [Using a node](docs/guide/using-uv.md) | [Как пользоваться узлом](docs/guide/ru/using-uv.md) | install, enrol, connect a phone, every command |
| [Running the server](docs/guide/using-mn.md) | [Как запустить сервер](docs/guide/ru/using-mn.md) | install, invite nodes, watch and manage them |
| [The interactive menu](docs/guide/menu.md) | [Интерактивное меню](docs/guide/ru/menu.md) | doing all of it from a numbered menu instead of flags |
| [Hardening a node](docs/guide/hardening.md) | [Защита узла](docs/guide/ru/hardening.md) | the host firewall, the ban engine and the blocklist, and the way back |
| [When something goes wrong](docs/guide/troubleshooting.md) | [Если что-то пошло не так](docs/guide/ru/troubleshooting.md) | the failures you are likely to meet, and what to do |

The reference documents go deeper, and assume you are comfortable with TLS, ACME and systemd:

| Document | Contents |
|---|---|
| [`docs/DESCRIPTION.md`](docs/DESCRIPTION.md) | the design: architecture, credential model, state machine, protocol, invariants, security |
| [`docs/RUNBOOK.md`](docs/RUNBOOK.md) | something is wrong: triage, the recovery playbooks, upgrade, backup and restore, uninstall |
| [`docs/OPERATIONS.md`](docs/OPERATIONS.md) | the reference: what runs where, sizing, WARP, certificates, clients, monitoring |
| [`docs/PROTOCOL.md`](docs/PROTOCOL.md) | the wire contract: endpoints, payloads, problem codes, subscription formats |
| [`docs/THREAT_MODEL.md`](docs/THREAT_MODEL.md) | adversaries, mitigations, residual risk, and what is deliberately not defended |
| [`docs/IMPLEMENTATION_PLAN.md`](docs/IMPLEMENTATION_PLAN.md) | milestones, acceptance criteria, risks |
| [`docs/QUESTIONS.md`](docs/QUESTIONS.md) | decisions taken and questions still open |
| [`CHANGELOG.md`](CHANGELOG.md) | what each release changed — [`CHANGELOG.ru.md`](CHANGELOG.ru.md) is the same in Russian |
| [`AGENTS.md`](AGENTS.md) | rules for anyone — human or agent — changing this repository |

## Quick start

The management server first — a node needs a server to join, not the other way round. The shortest path is
[Quickstart](docs/guide/quickstart.md); the reasoning is in [How Termit works](docs/guide/overview.md), and the
server has its own page in [Running the server](docs/guide/using-mn.md).

```bash
# on the server (Debian 13, ports 80/443 free, DNS pointing at it)
wget -q https://github.com/shogun-kub/termit/releases/latest/download/mn.sh
sh mn.sh --domain mn.example.com --email you@example.com   # --version v0.4.1 pins a release
termit-mn invite create --note "node-a"                    # prints the code once

# on the node (Debian 13, ports 80/443 free, DNS pointing at it)
wget -q https://github.com/shogun-kub/termit/releases/latest/download/uv.sh
sh uv.sh --wizard
```

`wget` is part of a standard Debian 13 install; `curl -fsSLO <same URL>` works identically if you have
it, and the installers themselves fetch with either.

The wizard asks for the node's name, the server's address — including the API prefix that `init` printed —
and the invite code. Then `termit-uv status` prints the subscription URL to paste into a client, and
`termit-mn status` shows the mesh. The installer verifies the release against its `SHA256SUMS` and refuses
anything it cannot verify.

## What it is, and what it is not

**It is** a way for a handful of people to share several proxy paths without trusting a commercial
service: each node is owned by one person, credentials are per pair, and a blocked address costs one
path rather than the network.

**It is not** a commercial tunnel service, a reseller panel, or a mesh of strangers. Membership is
by invite, every node belongs to somebody the operator knows, and nothing is metered.

Deliberate design choices worth knowing before you deploy it:

- **The management server never carries traffic.** If it goes down, established connections keep
  working; only membership changes stop.
- **Only proxied traffic uses WARP.** SSH, the decoy site and certificate renewal always take the
  node's own path. Termit never touches host routing, and its firewall is opt-in: one nftables table
  exists only after somebody types `termit-uv firewall on`, and `off` puts the host back exactly as it
  was.
- **A failed check is not a failed node.** A probe that cannot produce a verdict leaves the node's
  state alone; only a genuine negative result moves it out of the mesh.
- **No secrets in logs or metrics.** Logs carry node identifiers; `/metrics` is loopback-only and
  carries no inventory.

## Status

`v0.4.1` is the newest release. M1–M5 are implemented and verified on a three-machine acceptance lab
(Debian 13 in unprivileged containers), and the release pipeline builds verified artifacts for
linux/amd64 and linux/arm64. Since `0.4.0` the WARP tunnel runs inside the proxy core instead of a
sidecar, so it needs no `/dev/net/tun`, and `warp.family` decides which address family leaves through it.
The passes behind the releases are recorded in [`docs/IMPLEMENTATION_PLAN.md`](docs/IMPLEMENTATION_PLAN.md)
(M5.8 for `0.3.2`, §11.9 for `0.4.0`): three production hosts — a management server in a container and two
nodes behind real domains — with the host firewall confirmed and surviving a reboot, the ban engine and
the blocklist active, WARP egress verified through Cloudflare's own trace, the owner's page reachable over
TLS, and a phone app passing traffic through both nodes. `v0.3.2` and its predecessors carry their own
sections of the same changelog. See [what is not here yet](#what-is-not-here-yet).

## Requirements

| | |
|---|---|
| Runtime | Debian 13 (trixie), amd64 or arm64, systemd, root |
| Download tool | `wget` (part of Debian 13; `curl -fsSLO <url>` works too if installed) |
| Ports | 80 and 443 reachable from the internet on every box |
| DNS | one name per node and one for the server, pointed at the right box |
| Certificate | issued automatically over ACME (Let's Encrypt by default) |
| Build | Go 1.27.4 or newer; `CGO_ENABLED=0`, no cgo dependencies |

## Building and testing

```bash
go build ./cmd/... ./internal/...                 # both binaries
go test ./cmd/... ./internal/...                  # unit tests
go vet ./cmd/... ./internal/...
gofmt -l cmd internal deploy test                 # must print nothing

go run ./deploy/release --version v0.4.1 --out dist   # the release directory, as CI builds it
```

Package patterns are explicit on purpose: `references/` holds third-party Go source inside this
module's tree, and `./...` would compile it.

Acceptance runs against real machines over SSH — three Debian boxes (one server, two nodes), optionally
a fourth for the mesh test:

```bash
export TERMIT_LAB_MN=root@mn.example.lan          # and the rest of AGENTS.md §4.3
go test -tags=lab ./test/lab -v -timeout 30m
```

Without those variables the lab suite skips: it is a manual gate, not part of `go test ./cmd/...
./internal/...`. CI runs format, vet, lint, the unit suite with `-race`, a cross-build, the release
tool's tests and `govulncheck`.

## What is not here yet

- **WARP through the `proxy` transport**: the lab has no `/dev/net/tun`, so the suite covers the default
  `wireguard` transport, which needs no device; installing and connecting the `warp-cli` sidecar needs a
  KVM VM or a privileged container.
- **A Docker Compose harness** for development without lab hardware.

## Licence

AGPL-3.0. See [`LICENSE`](LICENSE).
