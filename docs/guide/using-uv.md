# Using a node (termit-uv)

[English](using-uv.md) · [Русский](ru/using-uv.md)

Everything that happens on the machine you own: install it, join the mesh, connect a phone, and the
commands you will actually type. If you have not read [how Termit works](overview.md), start there —
one page, and it explains what a node and the server are.

You need: a Debian 13 box, root access, a DNS name pointing at it, ports 80 and 443 free, and an
invite code from whoever runs the management server ([running the server](using-mn.md)).

If you would rather pick from a list than remember flags, run `termit-uv` with no arguments on the node: that
is [the interactive menu](menu.md), and every entry prints the command it runs.

## Before you touch the box

1. **Point DNS at it.** Create an `A` (and `AAAA` if you have IPv6) record for `node1.example.com`.
   Certificate issuance and enrolment both depend on it, and enrolment compares the record with the
   address your request came from — so do this first, then wait for the record to resolve.
2. **Free the ports.** A fresh Debian has nothing on 80/443; a box that already serves a site must be
   emptied of it first. `ss -lntp | grep -E ':(80|443) '` tells you.
3. **Know what WARP does.** WARP is on by default: proxied traffic leaves through Cloudflare's
   network. If you would rather egress from your own address, install with `--no-warp`. Nothing else
   in the guide changes.

## Install

Download the installer for a release, then run it — never pipe a URL into a shell:

```bash
curl -fsSLO https://github.com/shogun-kub/termit/releases/download/v0.1.0/uv.sh

sh uv.sh --version v0.1.0 \
  --domain node1.example.com --email you@example.com \
  --mn https://mn.example.com/6rfjdqfn5nno --invite ABCD-EFGH-IJKL
```

`--mn` must be the URL the server printed at `termit-mn init`, **including the API prefix**:
`https://mn.example.com/6rfjdqfn5nno`, not the bare host. The prefix is where the API lives, and a node
given only the host reaches nothing — it will say so rather than reporting a bad invite.

The script verifies the release checksum against `SHA256SUMS`, installs `termit-uv`, runs
`termit-uv init` (which does the actual work) and then `termit-uv enroll` with your invite.

Useful variations:

| Goal | Flag |
|---|---|
| install and converge, join later | `--no-enroll` |
| see what would happen, change nothing | `--dry-run` |
| a mirror or a local directory instead of GitHub | `--base-url https://mirror.example.com/termit` |
| verify against a digest you have | `--sha256 <hex>` (or `--checksum-file SHA256SUMS`) |
| your own build, not a release | install the binary yourself and run `termit-uv init` |
| opt out of WARP | `--no-warp`, later `termit-uv warp mode off` |

The script never guesses a version, and refuses to install bytes it cannot verify. If it stops, the
message names the file and the expected digest.

### Without the script

Two commands, and the same result:

```bash
install -m 0755 termit-uv /usr/bin/termit-uv
termit-uv init --domain node1.example.com --email you@example.com
```

## What `termit-uv init` does

Thirteen steps, each idempotent: a step whose effect is already there does nothing, so re-running is
the way to repair a partial install.

| Step | What it does |
|---|---|
| `preflight` | checks root, Debian version, systemd, architecture, clock, free ports, DNS, the ACME directory, `/dev/net/tun`, disk, memory and `apt` — one line per check, with a hint when something is off |
| `user` | creates the unprivileged `termit` user |
| `dirs` | `/etc/termit/uv`, `/var/lib/termit/uv`, `/var/www/termit` |
| `packages` | installs `nginx`, `ca-certificates`, `curl` |
| `warp` | installs, registers and connects `cloudflare-warp`; skipped when WARP is off, explained when the host cannot run it |
| `xray-binary` | downloads the pinned proxy core, verifies its SHA-256, installs it |
| `identity` | generates the node's UUID, request path and decoy seed — **once, never again** |
| `decoy` | generates the decoy site |
| `xray-config` | writes the core configuration and validates it with `xray -test` |
| `web` | writes the nginx vhost and validates it with `nginx -t` |
| `units` | writes and enables the systemd units and the renewal timer |
| `certificate` | issues the TLS certificate over HTTP-01 |
| `start` | starts the core, nginx and the agent, and verifies they stay up |

On success it prints a summary and the identity fingerprint. A failure names the step and the command
that failed; `--only <step>` re-runs just that one.

## Join the mesh

```bash
termit-uv enroll --invite ABCD-EFGH-IJKL                  # --mn URL if the config does not have it
termit-uv status                                          # enrolled? peers? certificate?
```

`termit-uv enroll` stores an agent key and a subscription token in the state file (`0600`, shown once) and
runs the first sync. If the server is in `invite+approval` mode the node stays `pending` until the
operator approves it — that is normal, not an error:

```bash
termit-mn uv approve node1.example.com        # on the management server
```

## Connect a phone or laptop

There are two different things, and it matters which one you use:

| | Subscription | A local device |
|---|---|---|
| What it contains | this node **plus every healthy peer** | only this node |
| Where it lives | on the management server | only on this node |
| How you get it | `termit-uv status` prints the URL | `termit-uv device add --label phone` prints a `vless://` URI |
| Use it for | your own client, when you want the whole mesh | a device that should use one node, or a client that does not support subscriptions |

```bash
termit-uv status
# subscribe:  https://mn.example.com/sub/AbCdEf...
```

Paste that URL into the client's "subscription URL" field (v2rayN, Nekoray, sing-box, Shadowrocket,
v2rayNG — anything that understands a `vless://` subscription). For sing-box, add `/json` to the URL
to get a complete profile with a selector, a `direct` outbound and private ranges kept direct.

The client fetches the list again on its own schedule; when a peer comes or goes, one refresh is all
it takes. The URL is a secret: it carries your credentials. If it leaks, rotate it on the server
(`termit-mn sub rotate node1.example.com --domain mn.example.com`) and the old one stops working.

## Day to day

```bash
termit-uv status               # identity, certificate, peers, last sync, WARP state
termit-uv doctor               # host and node health, with a hint per failing check
termit-uv doctor --json        # the same, machine-readable
termit-uv sync                 # reconcile the core with the server's peer set now
termit-uv sync --check         # exit 4 if the node drifted from the server's set, change nothing
termit-uv agent --once         # one full cycle in the foreground, for a drill
termit-uv unblock              # ask the server to re-verify this node and lift a block
```

`termit-uv status` in full, so you know what you are looking at:

```
domain:       node1.example.com
identity:     a10ceae7
proxy path:   e9438a65
decoy:        portal (seed 5927413a)
core:         xray 26.11.9 at /usr/lib/termit/bin/xray
certificate:  expires 2027-02-08T12:06:44Z (in 89 days)
management:   enrolled 2026-11-10T06:16:10Z
subscribe:    https://mn.example.com/sub/AbCdEf...
peers:        2 (node2.example.com, node3.example.com)
last sync:    ok 2026-11-10T14:51:11Z
next sync:    2026-11-11T14:55:13Z
warp:         on (via 104.28.1.1 (AMS))
self-check:   ok 2026-11-10T11:17:04Z
```

The agent wakes once a day at the server-given time plus a small per-node jitter, checks itself
(the node's own view of the internet), syncs, and goes back to sleep. You do not need a timer of your
own; the systemd units are installed by `termit-uv init`.

## Command reference

| Command | What it does | Main flags |
|---|---|---|
| `termit-uv init` | install or converge this node | `--domain`, `--email`, `--no-warp`, `--warp MODE`, `--warp-selected LIST`, `--dry-run`, `--only`, `--skip`, `--acme-ca`, `--xray-version`, `--xray-download-base`, `--relocate`, `--agent-key-file` |
| `termit-uv doctor` | diagnose the host and the node, change nothing | `--json` |
| `termit-uv status` | identity, certificate, peers, sync, WARP | `--json` |
| `termit-uv enroll` | register with the server | `--invite`, `--mn`, `--force`, `--no-sync` |
| `termit-uv sync` | reconcile peers now | `--check`, `--dry-run`, `-v` (print the peer set) |
| `termit-uv agent` | run the daily loop in the foreground | `--once` |
| `termit-uv device` | local devices on this node | `list`, `add --label NAME [--uuid ...]`, `remove NAME` |
| `termit-uv uri` | print a `vless://` URI for this node | `--label` |
| `termit-uv warp` / `termit-uv warp status` | report egress | `--json` |
| `termit-uv warp mode` | change egress | `off`, `all`, `selected --selected a.example,b.example` |
| `termit-uv unblock` | ask the server to re-verify and unblock | `--domain`, `--vless-url`, `--no-sync` |
| `termit-uv migrate` | move this node to its new domain | `--relocate`, `--domain`, `--no-sync` |
| `termit-uv renew-cert` | renew the certificate now | `--force` (reissue even if not due) |
| `termit-uv renew-site` | regenerate the decoy site | `--template product\|portal`, `--seed`, `--dry-run` |
| `termit-uv uninstall` | remove what Termit created | `--purge` (also config, state, certificates, user), `--dry-run` |
| `termit-uv version` | build and protocol version | |

Every command accepts `--config <path>` and `-v`/`--verbose`.

## Recipes

**Move the node to a new domain.** Add the new DNS record, then on the node:

```bash
termit-uv init --domain node2.example.com --relocate --agent-key-file /root/agent.key
termit-uv migrate --relocate --domain node2.example.com
```

`termit-uv init --relocate` keeps the agent key (it is the node's identity to the server), mints a fresh
identity for the new name and does not enrol; `termit-uv migrate --relocate` asks the server to verify the
new address and swap the record. Peers pick the change up on their next sync.

**Rebuild a box that died.** On the server, `termit-mn uv agentkey node1.example.com` prints the agent key
(the node is not disturbed). On the new box:

```bash
termit-uv init --domain node1.example.com --agent-key-file /root/agent.key --relocate
termit-uv migrate --relocate
```

**Replace the decoy site.**

```bash
termit-uv renew-site --template product        # product or portal
```

**Change egress.**

```bash
termit-uv warp mode off                        # direct from your own address
termit-uv warp mode all                        # back to WARP (default)
termit-uv warp mode selected --selected example.com,api.example.org
```

**Uninstall.** `termit-uv uninstall` removes the units, the core and the nginx vhost, and stops services;
`--purge` also removes the configuration, state, certificates and the `termit` user. Packages it
installed are left alone, and no other nginx site is touched. The server keeps the record until
`termit-mn uv rm` — tell the operator, or peers will list a node that no longer exists until checks fail.

## Where things live

| Path | Contents |
|---|---|
| `/etc/termit/uv/config.yaml` | your configuration — the only file you edit |
| `/var/lib/termit/uv/state.json` | identity, secrets, subscription token (`0600`) |
| `/var/lib/termit/uv/` | journal of install runs |
| `/usr/lib/termit/bin/xray` | the proxy core |
| `/var/www/termit/` | the decoy site and the ACME challenge directory |
| `/etc/nginx/sites-enabled/termit` | the generated vhost |

Units: `termit-xray.service`, `termit-uv-agent.service`, `termit-uv-renew.timer` (certificate) and
`termit-uv-renew.service`. `systemctl status termit-uv-agent` and `journalctl -u termit-uv-agent`
answer most questions.

Next: [Running the server](using-mn.md) · [When something goes wrong](troubleshooting.md)
