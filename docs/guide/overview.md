# How Termit works

[English](overview.md) · [Русский](ru/overview.md)

Termit turns a few personal VPS boxes into one private proxy network that its owners can actually
check. Every participant keeps their own server; nobody has to trust anybody else's machine with
anything except the traffic they choose to send through it.

This page explains the idea and the order of installation. Then:

| Document | What it covers |
|---|---|
| [Using a node (termit-uv)](using-uv.md) | install a node, enrol it, connect a phone, every command |
| [Running the server (termit-mn)](using-mn.md) | install the management server, invite nodes, watch them |
| [The interactive menu](menu.md) | the same commands, chosen from a list, with the command line it runs printed for you |
| [When something goes wrong](troubleshooting.md) | the failures you are most likely to meet, and what to do |
| [Design](../DESCRIPTION.md) · [Operations](../OPERATIONS.md) · [Protocol](../PROTOCOL.md) | the full reference, for whoever runs the thing for real |

## The idea

A commercial VPN asks you to believe that its operator does not log, cannot be blocked, and will
still be there next year. A self-hosted proxy fixes the trust problem and inherits a worse one: one
address, one server, easy to fingerprint as a proxy and easy to block.

Termit spreads that risk over a small group. Each owner runs a node; every node is a plain-looking
HTTPS site to anyone who probes it; the nodes share one membership list, so each owner's client ends
up with several paths to the same internet. And because the nodes belong to different people, a
blocked address costs one path, not the whole network.

The catch with any mesh is bookkeeping: who is a member, is their node alive, what credentials does
each pair need. Termit does that with a small **management server (MN)** — one more box, owned by
whoever is willing to run it. The MN never proxies traffic. It keeps the membership list, checks
every node from the outside, and hands each owner one subscription URL containing every healthy node.

## How the pieces fit

```
     your phone / laptop                     a friend's phone
              │                                      │
              │  VLESS over XHTTP + TLS              │
              ▼                                      ▼
   ┌──────────────────────┐              ┌──────────────────────┐
   │ UV node A            │              │ UV node B            │
   │ your VPS             │              │ their VPS            │
   │ xray + nginx         │              │ xray + nginx         │
   │ decoy HTTPS site     │              │ decoy HTTPS site     │
   │ optional WARP egress │              │ optional WARP egress │
   └──────────┬───────────┘              └──────────┬───────────┘
              │                                     │
              │   daily sync over TLS 443           │
              └──────────────────┬──────────────────┘
                                 ▼
                    ┌───────────────────────────┐
                    │ MN — management server     │
                    │ node registry, health      │
                    │ prober, subscriptions      │
                    │ never carries your traffic │
                    └───────────────────────────┘
```

Three properties are worth noticing:

- **The MN is not a proxy.** No user traffic passes through it. If it goes down, existing
  connections keep working; only new membership changes stop.
- **Credentials are per pair.** Node A can talk to node B with a credential that is valid for that
  pair alone. Any client A hands out for B works for B and nowhere else.
- **Proxied traffic does not have to leave from your own address.** With WARP enabled (the default)
  the node's proxy traffic egresses through Cloudflare's network, so an application that asks "what
  address am I coming from?" sees a shared Cloudflare address instead of a server it can look up.
  Direct traffic — SSH, the decoy site, certificate renewal — never enters that tunnel.

## Who it is for

You are a small group of people who already run, or are willing to run, a Debian box each — a €3 VPS
is enough. You want a proxy that is not a commercial service and not a single point of failure, and
you are comfortable with a terminal.

It is **not** a commercial VPN product, not a control panel for reselling, and not a mesh of strangers:
membership is by invite, and every node is owned by a person the operator knows.

## What you need before you start

| | Management server | Each node |
|---|---|---|
| Machine | Debian 13 (trixie), amd64 or arm64 | Debian 13 (trixie), amd64 or arm64 |
| Resources | 2 vCPU, 2 GB RAM, 10 GB disk | 1 vCPU, 1 GB RAM, 10 GB disk |
| Access | root over SSH | root over SSH |
| Network | a public IPv4 or IPv6, ports **80** and **443** free | a public IPv4 or IPv6, ports **80** and **443** free |
| Name | one DNS name (`mn.example.com`) pointing at it | one DNS name per node (`node1.example.com`) |
| Certificate | issued automatically by Let's Encrypt | issued automatically by Let's Encrypt |

Port 80 is needed for the certificate challenge, 443 for the service itself. Both must be reachable
from the internet; a box behind CGNAT cannot be a node.

## The order of installation

**Yes — the management server comes first.** A node can be installed on its own (`uv init` needs no
server), but it cannot *join* anything until the MN exists: the invite code is issued by the server,
enrolment asks the server to verify the node, and the subscription URL that a phone uses is served by
the server.

```
1. MN          mn init → systemd unit → mn status shows "nodes: 0"
2. invite      mn invite create --note "node-a"   → ABCD-EFGH-IJKL
3. node        uv.sh (or uv init) → uv enroll --invite ABCD-EFGH-IJKL
4. approval    only in invite+approval mode: mn uv approve node-a.example.com
5. client      uv status → subscription URL → paste it into the phone app
```

Step 3 is where DNS matters: enrolment compares the address the request came from with the A/AAAA
record of the node's domain, so point the record at the box **before** enrolling. The certificate is
issued during `uv init`, which needs port 80 free and the record already in place.

Everything after step 5 is automatic. The node wakes once a day, checks itself, syncs its peer set,
and the server checks every node on its own schedule. You only touch it when something is wrong.

## What you get

| Feature | What it does for you |
|---|---|
| **Subscription URL** | one URL per node, containing every healthy node with a per-pair credential; works in any client that accepts a `vless://` subscription, plus a sing-box profile at `<url>/json` |
| **Disguised HTTPS node** | the node serves a generated site on 443; an unauthorised request gets that site, not a proxy error |
| **WARP egress** (default on) | proxied traffic leaves through Cloudflare's network, so the address is shared rather than yours; can be switched to direct, or restricted to selected domains |
| **Health checks** | the server probes every node through the tunnel, checks its certificate and decoy, and moves a node out of the mesh only on a genuine failure |
| **Your own devices** | `uv device add` creates local clients for your phone, laptop, router — they live only on your node and are never shared |
| **Node migration** | a node can move to a new domain, or be rebuilt on a fresh box, without losing its place in the mesh |
| **Operator surface** | `mn status` dashboard, a loopback-only `/metrics` endpoint, and per-node history of checks |
| **Backup and restore** | one command on the server; a node's state is a single file |

## Words used in this guide

| Term | Meaning |
|---|---|
| **UV**, node | *termit-uv*, the binary on a participant's server: installer, agent and CLI |
| **MN** | *termit-mn*, the management server: registry, prober, subscriptions, admin CLI |
| **peer** | another node in the mesh, as seen from one node |
| **pair credential** | the UUID/path pair valid between exactly two nodes |
| **subscription** | the URL your client fetches to get the current peer list |
| **invite code** | the one-time (or limited-use) code that lets a node enrol |
| **state** | `pending`, `active`, `unavailable`, `blocked`, `removed` — what the server thinks of a node |
| **decoy** | the generated website the node serves to anyone without the right path |

## Where to go next

- Installing your first node: [Using a node](using-uv.md)
- Running the server: [Running the server](using-mn.md)
- Would rather pick from a list: [The interactive menu](menu.md)
- Something is broken: [When something goes wrong](troubleshooting.md)
- По-русски: [Как устроен Termit](ru/overview.md)
