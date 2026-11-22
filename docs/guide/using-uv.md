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

Download the installer, then run it — never pipe a URL into a shell. `wget` is not guaranteed: Debian's
minimal and netinst images ship neither it nor `curl`, so install one first (`apt-get update &&
apt-get install -y wget`). `curl -fsSLO <same URL>` works too:

```bash
apt-get update && apt-get install -y wget   # only when the image has neither

wget -q https://github.com/shogun-kub/termit/releases/latest/download/uv.sh

sh uv.sh \
  --domain node1.example.com --email you@example.com \
  --mn https://mn.example.com/6rfjdqfn5nno --invite ABCD-EFGH-IJKL
```

Without `--version` the script installs the newest published release; `--version v0.4.1` pins the one
you name, and a `v<version>p<n>` build is how a tester installs a pre-release.

`--mn` must be the URL the server printed at `termit-mn init`, **including the API prefix**:
`https://mn.example.com/6rfjdqfn5nno`, not the bare host. The prefix is where the API lives, and a node
given only the host reaches nothing — it will say so rather than reporting a bad invite.

The script verifies the release checksum against `SHA256SUMS`, installs `termit-uv`, runs
`termit-uv init` (which does the actual work) and then `termit-uv enroll` with your invite. `--mn`
reaches both: `init --mn` writes it to the configuration as `mn.url`, which is what the agent reads on
every sync, and `enroll` uses it for the registration itself. Running `termit-uv enroll --mn URL`
yourself later also records the URL when the configuration has none.

`--wizard` replaces the flags with questions: the script installs the binary and hands the terminal to `termit-uv wizard`, which asks for the domain, the email, the server, the invite and the rest.

Useful variations:

| Goal | Flag |
|---|---|
| install and converge, join later | `--no-enroll` |
| let it ask instead of passing flags | `--wizard` — installs the binary, then runs `termit-uv wizard` |
| see what would happen, change nothing | `--dry-run` |
| a mirror or a local directory instead of GitHub | `--base-url https://mirror.example.com/termit` |
| verify against a digest you have | `--sha256 <hex>` (or `--checksum-file SHA256SUMS`) |
| your own build, not a release | install the binary yourself and run `termit-uv init` |
| opt out of WARP | `--no-warp`, later `termit-uv warp mode off` |
| change how WARP carries traffic | `termit-uv init --warp-transport wireguard` (inside the core, TCP and UDP), `usque` (the MASQUE sidecar, TCP and UDP, for a box whose `warp-cli` cannot carry UDP) or `proxy` (the `warp-cli` sidecar, TCP only) |
| choose which family leaves through WARP | `termit-uv init --warp-family ipv4` (the default; destinations of the other family are refused) or `ipv6` — the core's own WireGuard transport only: the MASQUE sidecar is dual-stack and ignores the key |

The script refuses to install bytes it cannot verify. If it stops, the message names the file and the
expected digest.

### Without the script

Two commands, and the same result:

```bash
install -m 0755 termit-uv /usr/bin/termit-uv
termit-uv init --domain node1.example.com --email you@example.com
```

## What `termit-uv init` does

Fourteen steps, each idempotent: a step whose effect is already there does nothing, so re-running
is the way to repair a partial install. With the QUIC transport enabled two more appear —
`hysteria-binary` and `hysteria-config` — and without it `hysteria-off`, which does nothing on a node that
never had it.

| Step | What it does |
|---|---|
| `preflight` | checks root, Debian version, systemd, architecture, clock, free ports, DNS, the ACME directory, `/dev/net/tun`, disk, memory and `apt` — one line per check, with a hint when something is off |
| `user` | creates the unprivileged `termit` user |
| `dirs` | `/etc/termit/uv`, `/var/lib/termit/uv`, `/var/www/termit` |
| `packages` | installs `nginx`, `ca-certificates`, `curl` |
| `warp` | registers the node's WARP identity for the tunnel; with `--warp-transport proxy` it installs and connects `cloudflare-warp` instead; skipped when WARP is off |
| `xray-binary` | downloads the pinned proxy core, verifies its SHA-256, installs it |
| `identity` | generates the node's UUID, request path and decoy seed — **once, never again** |
| `decoy` | generates the decoy site |
| `xray-config` | writes the core configuration and validates it with `xray -test` |
| `web` | writes the nginx vhost and validates it with `nginx -t` |
| `units` | writes and enables the systemd units and the renewal timer |
| `certificate` | gets the free certificate from Let's Encrypt by answering its check on port 80 |
| `start` | starts the core, nginx and the agent, and verifies they stay up |
| `warp-egress` | fetches the Cloudflare trace through the node's own tunnel and says where it came out; a warning, never a failure |

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

Both are also on the owner's private page (`termit-uv options`): it lists the node's own device, the local
devices and the mesh, and it re-renders itself when a device is added or removed and on every sync — the
agent's daily cycle included, so a peer that joins or leaves the mesh reaches the page without a command.
Every link it shows carries both dialects of the fragmentation switch — Happ's `fragment=…` and
`sb.tls.fragment=1`/`sb.tls.record_fragment=1`, which `podkop-engine` and the patched sing-box builds read
from the URI — so the same link works in either client without being edited by hand. That is not universal:
**Throne** and the Nekoray family it comes from parse neither, because they keep TLS fragmentation in their
own settings — in Throne for Android it is *Edit profile → “TLS Client Hello fragment”* (a tri-state, default
“Keep default”, beside “TLS record fragment”), or, once for everything, *Settings → Presets → “TLS Client
Hello fragment”* with its implementation and size; on the desktop build the same three live in the profile
under “TLS Camouflage Settings”. The JSON form of the subscription carries it as `"fragment": true` and
`"record_fragment": true`, so a client that imports the profile instead of the link gets it without a click. Each row also opens a
QR code under the copy button: point the phone's camera at it and the client imports the link, which is the
one gesture a phone has and the reason the page exists. The code is drawn by the node itself — the page
references no external host — and it is the same secret as the text, so a screenshot of it is as much of a
leak as a copied link.

```bash
termit-uv status
# subscribe:  https://mn.example.com/sub/AbCdEf...
```

Paste that URL into the client's "subscription URL" field. The core must speak XHTTP: Happ, v2rayNG,
v2rayN, NekoRay (all on Xray) or `podkop-engine` on OpenWrt. Stock sing-box has no XHTTP — it silently
falls back to plain VLESS, the node answers `400`, and the client reports `unknown version: 72` (`72` is
`H`, the first byte of `HTTP/1.1`). For sing-box builds patched with XHTTP, add `/json`: the same peer
list as a profile with a selector, a `direct` outbound and private ranges kept direct.

The client fetches the list again on its own schedule; when a peer comes or goes, one refresh is all
it takes. The URL is a secret: it carries your credentials. If it leaks, rotate it on the server
(`termit-mn sub rotate node1.example.com --domain mn.example.com`) and the old one stops working.

## A second transport over QUIC (optional)

The node can serve its certificate on UDP/443 too, with Hysteria2 — a protocol built for lossy mobile
networks. Voice calls and video feel the difference first; ordinary browsing does not need it, and nothing
is installed until you ask.

```bash
termit-uv init --hysteria      # install and serve it; the menu has "QUIC transport"
```

The flag writes the block the transport needs (`hysteria: {}`) and adds `443` to
`security.firewall.udp_ports`: a UDP port that is not in that list falls into the core's scan counter, and
the guard would ban the owner's own QUIC traffic. An already-applied table is not touched by `init` — the
firewall is applied only by `firewall on` — so re-apply it to publish the port:

```bash
termit-uv firewall on && termit-uv firewall confirm
```

`init` then installs the pinned binary, writes `/etc/termit/uv/hysteria.yaml`, and starts
`termit-hysteria` as its own unprivileged user. It ends with a sync, because what a node serves reaches the
server in a sync request and nowhere else. From that point:

- `termit-uv uri`, `termit-uv device add` and `termit-uv device list` print the `hy2://` link beside the
  `vless://` one, labelled `(QUIC)`, and so does the page from `termit-uv options`;
- the subscription from your server carries the same second link, and the sing-box profile gets a
  `hysteria2` outbound;
- the transport is announced by the sync `init` ends with; `termit-uv sync` does the same by hand, and
  nothing on the server needs configuring.

What it costs: about 50 MB of memory, and its unit caps it at 256 MB; 4–9 % of one core per 100 Mbit/s
carried. The pinned release is also named in one advisory, GO-2026-5288, whose record in the Go database is
unreviewed and describes no fixed version — the advisory behind it (GHSA-9fw6-xgg2-mq9q) lists 2.8.1 as the
last affected release and 2.8.2 as the fix, while this node runs 2.14.0 and never enables the sniffing the
proof of concept needs. The 256 MB cap and `Restart=always` still stand, so a blow-up in the transport cannot
take the TLS profile or anything else on the node with it.

To turn it off, run `termit-uv init --no-hysteria`: the unit is stopped,
disabled and removed with its configuration and the pinned binary, the port closes, and the next sync
drops the second link from the subscription.

## Day to day

```bash
termit-uv status               # identity, certificate, peers, last sync, WARP state
termit-uv doctor               # host and node health, with a hint per failing check
termit-uv doctor --json        # the same, machine-readable
termit-uv net                  # sample the TCP counters: is the node dropping, or the path?
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

The agent syncs at the server-given time plus a small per-node jitter, checks itself (the node's own
view of the internet), applies the peer set and goes back to sleep. Between those cycles it wakes on the
interval the server gives it — fifteen minutes by default, `mesh_poll` in the server's configuration —
and reads the server's `healthz`, which carries a digest of the mesh: when the mesh has changed (a node
joined, left, was blocked or moved) it syncs at once, so a new node appears everywhere within one
interval. You do not need a timer of your own; the systemd units are installed by `termit-uv init`.

## Command reference

| Command | What it does | Main flags |
|---|---|---|
| `termit-uv init` | install or converge this node | `--domain`, `--email`, `--locale ru\|en`, `--happ-fragment VALUE`, `--no-warp`, `--warp MODE`, `--warp-selected LIST`, `--dry-run`, `--only`, `--skip`, `--acme-ca`, `--xray-version`, `--xray-download-base`, `--relocate`, `--agent-key-file` |
| `termit-uv doctor` | diagnose the host and the node, change nothing | `--json` |
| `termit-uv net` | tell a lossy path from a broken node, by sampling the TCP counters | `--seconds 60`, `--json` |
| `termit-uv status` | identity, certificate, peers, sync, WARP | `--json` |
| `termit-uv enroll` | register with the server | `--invite`, `--mn`, `--force`, `--no-sync` |
| `termit-uv sync` | reconcile peers now | `--check`, `--dry-run`, `-v` (print the peer set) |
| `termit-uv agent` | run the daily loop in the foreground | `--once` |
| `termit-uv device` | local devices on this node | `list`, `add --label NAME [--uuid ...]`, `remove NAME` |
| `termit-uv uri` | print a `vless://` URI for this node | `--label` |
| `termit-uv options` | render the private page of client links and print its address | `--rotate`, `--quiet` |
| `termit-uv wizard` | walk through a first installation, remembering the answers; asks for the language first | `--fresh` |
| `termit-uv warp` / `termit-uv warp status` | report egress | `--json` |
| `termit-uv warp mode` | change egress | `off`, `all`, `selected --selected a.example,b.example` |
| `termit-uv unblock` | ask the server to re-verify and unblock | `--domain`, `--vless-url`, `--no-sync` |
| `termit-uv migrate` | move this node to its new domain | `--relocate`, `--domain`, `--no-sync` |
| `termit-uv renew-cert` | renew the certificate now | `--force` (reissue even if not due) |
| `termit-uv renew-site` | regenerate the decoy site | `--template product\|portal`, `--seed`, `--dry-run` |
| `termit-uv uninstall` | remove what Termit created | `--purge` (also config, state, certificates, user), `--dry-run` |
| `termit-uv firewall` | the host firewall: `status`, `on`, `off`, `confirm`, `refresh` | `--ssh-from`, `--default-deny`, `--dry-run` |
| `termit-uv bans` | what the ban engine decided: `list`, `offenders`, `block IP`, `unban IP`, `unblock IP` | |
| `termit-uv guard` | the ban engine: `status`, `run --once` | |
| `termit-uv version` | build and protocol version | |

Every command accepts `--config <path>` and `-v`/`--verbose`. The hardening half — `firewall`, `bans`
and `guard` — stays off until you ask: [Hardening a node](hardening.md) is their page.

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
| `/etc/nginx/sites-enabled/termit.conf` | the generated vhost, a link to `sites-available/termit.conf` |

Units: `termit-xray.service`, `termit-uv-agent.service`, `termit-uv-renew.timer` (certificate) and
`termit-uv-renew.service`. `systemctl status termit-uv-agent` and `journalctl -u termit-uv-agent`
answer most questions.

Next: [Running the server](using-mn.md) · [When something goes wrong](troubleshooting.md)
