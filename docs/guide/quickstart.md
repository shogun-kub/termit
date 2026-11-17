# Quickstart

Two machines and about fifteen minutes: a server that keeps the registry and a node that carries the
traffic. The long version — every flag, every failure — is in [How Termit works](overview.md),
[Running the server](using-mn.md) and [Using a node](using-uv.md); this page is the shortest path to a
phone that works.

## Before you start

| | Server | Node |
|---|---|---|
| System | Debian 13, root over SSH | Debian 13, root over SSH |
| DNS | `mn.example.com` points at it | `node-a.example.com` points at it |
| Ports | 80 and 443 free, reachable from outside | 80 and 443 free, reachable from outside |
| Size | 2 vCPU, 2 GB RAM | 1 vCPU, 1 GB RAM |

The node joins the server, not the other way round — that is why the server comes first. Both
certificates are issued by Let's Encrypt during the install, so the DNS records must already resolve
when you start.

Substitute your own names for `mn.example.com` and `node-a.example.com` throughout.

## 1. The server

Download the release, verify it against the checksums it ships with, and install the binary:

```bash
RELEASE=v0.4.0
curl -fsSLO "https://github.com/shogun-kub/termit/releases/download/$RELEASE/termit-mn_${RELEASE}_linux_amd64"
curl -fsSLO "https://github.com/shogun-kub/termit/releases/download/$RELEASE/SHA256SUMS"
sha256sum -c --ignore-missing SHA256SUMS
install -m 0755 "termit-mn_${RELEASE}_linux_amd64" /usr/bin/termit-mn
```

Create the configuration, the database and the first admin token. The token goes straight into a file
with mode `0600`, so the only copy is not in your scrollback:

```bash
install -d -m 0750 /etc/termit/mn
termit-mn init --domain mn.example.com --email you@example.com \
  --admin-token-out /etc/termit/mn/admin.token
```

`init` prints the **API prefix** it picked, for example `/6rfjdqfn5nno`. Write it down: a node is given
`https://mn.example.com/6rfjdqfn5nno`, the full URL, not the bare host. The prefix is not a secret — it
is the random path the API lives on, so a scanner that asks for `/api/v1` finds the masking site instead.

Install the unit and start the server:

```bash
curl -fsSLO "https://github.com/shogun-kub/termit/releases/download/$RELEASE/termit-mn.service"
install -m 0644 termit-mn.service /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now termit-mn
```

Check that it is alive, then mint an invite code for the node:

```bash
export TERMIT_MN_ADMIN_TOKEN_FILE=/etc/termit/mn/admin.token
termit-mn status
termit-mn invite create --note node-a
```

The invite code is shown once — `ABCD-EFGH-IJKL` in the examples.

## 2. The node, with the wizard

Install the binary the same way:

```bash
RELEASE=v0.4.0
curl -fsSLO "https://github.com/shogun-kub/termit/releases/download/$RELEASE/termit-uv_${RELEASE}_linux_amd64"
curl -fsSLO "https://github.com/shogun-kub/termit/releases/download/$RELEASE/SHA256SUMS"
sha256sum -c --ignore-missing SHA256SUMS
install -m 0755 "termit-uv_${RELEASE}_linux_amd64" /usr/bin/termit-uv
```

Then let it ask the questions:

```bash
termit-uv wizard
```

| The wizard asks | Answer for this walkthrough |
|---|---|
| language | `en` or `ru` — everything after this is in it |
| the node's domain | `node-a.example.com` |
| contact address for ACME | `you@example.com` |
| which masking site | `portal` (or `product`) — what a stranger sees at `https://node-a.example.com/` |
| WARP | `all` (everything proxied leaves through Cloudflare), `selected`, or `off` |
| host firewall and ban engine | `yes` if this box is the node and nothing else — see [Hardening a node](hardening.md) |
| invite code | `ABCD-EFGH-IJKL` (empty installs a standalone node) |
| management server | `https://mn.example.com/6rfjdqfn5nno` — with the prefix |

Every answer is remembered, so an interrupted run continues where it stopped and `Enter` accepts what is
already there. The wizard then installs Xray, writes the core's configuration, issues the certificate,
renders the masking site, starts the services, enrols the node and, with WARP on, proves that the tunnel
carries traffic.

A one-command alternative, without questions, is in [Using a node](using-uv.md#install):

```bash
sh uv.sh --version "$RELEASE" --domain node-a.example.com --email you@example.com \
  --mn https://mn.example.com/6rfjdqfn5nno --invite ABCD-EFGH-IJKL
```

## 3. Approve the node

The default enrolment mode is `invite+approval`: the node registers itself and waits. Approve it:

```bash
termit-mn uv list                        # it shows up as pending
termit-mn uv approve node-a.example.com
```

(`termit-mn` with no arguments opens the same operations as a numbered menu, in Russian or English.)

## 4. Connect a phone

```bash
termit-uv status
```

The `subscription:` line is a URL for your client, and it is the only credential you paste. Open it in
HAPP, Hiddify, v2rayNG or any client that speaks VLESS + XHTTP + TLS; the profile is rendered by the
node and points at the button you just approved. The same page also lists the node's peers, so a second
device or a second node appears there without any extra step.

## 5. Keep these

| What | Where | Why |
|---|---|---|
| admin token | `/etc/termit/mn/admin.token` | every `termit-mn` command needs it; `termit-mn admin rotate` replaces it |
| master key | `/etc/termit/mn/master.key` | without it the database cannot derive a single pair credential — it cannot be reset |
| database | `/var/lib/termit/mn/termit.db` | the registry: nodes, peers, users, invites |

Back the three up together, and nowhere near the server they belong to.

## Where to go next

- [Using a node](using-uv.md) and [Running the server](using-mn.md) — every command, day to day.
- [The interactive menu](menu.md) — the same operations from a numbered menu.
- [Hardening a node](hardening.md) — the firewall, the ban engine and the way back.
- [`docs/OPERATIONS.md`](../OPERATIONS.md) — upgrade, backup, restore, recovery, monitoring.
- [When something goes wrong](troubleshooting.md) — the failures you are likely to meet.
