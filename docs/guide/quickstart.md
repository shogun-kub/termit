# Quickstart

This page takes you from two empty servers to a working phone, in about fifteen minutes. It is written
for someone who has never set up a proxy before: every command is followed by what it does, and every
question the installer asks is explained.

You need two machines. One is the **server**: it keeps the list of who is in the network and never
carries traffic. The other is the **node**: it is the machine your phone actually connects to. Do the
server first — the node cannot join a server that does not exist yet.

If anything is unclear, the long versions are [Using a node](using-uv.md) and
[Running the server](using-mn.md).

## What you need

| | Server | Node |
|---|---|---|
| System | Debian 13, you are `root` over SSH | Debian 13, you are `root` over SSH |
| Name | `mn.example.com` already points at it | `node-a.example.com` already points at it |
| Network | ports 80 and 443 open to the internet | ports 80 and 443 open to the internet |
| Size | 2 CPU, 2 GB RAM | 1 CPU, 1 GB RAM |

"Already points at it" means the DNS record exists **before** you start. Both machines get a free
certificate from Let's Encrypt during the install, and Let's Encrypt only gives one to a name that
already resolves. The node checks this before it asks: a name that does not point at the machine stops
the install, because five failed attempts block that name for an hour.

Wherever you see `mn.example.com` or `node-a.example.com`, use your own names.

## The order of work

1. Set up the server.
2. Create an invite code on the server.
3. Set up the node — one command, then the installer asks questions.
4. Approve the node on the server.
5. Copy one link into your phone.

## 1. The server

Log in to the server as `root` and run these commands. They download the newest release with `wget`
(`curl -fsSLO <same URL>` works too; a minimal Debian image ships neither, and the first snippet
line installs one), check the file against its published checksum, install the server and its
systemd unit, give it the probe worker it needs, create the configuration, start the service and
print its status. `--version vX.Y.Z` installs that release instead of the newest one, where `vX.Y.Z`
is a release tag — `v0.7.0`, for example.

```bash
# only when the image has neither curl nor wget
apt-get update && apt-get install -y wget
wget -q "https://github.com/shogun-kub/termit/releases/latest/download/mn.sh" && sh mn.sh --domain mn.example.com --email you@example.com --admin-token-out /etc/termit/mn/admin.token
```

What to put instead of the placeholders:

| | |
|---|---|
| `--domain` | the server's name, the one that already points at this machine |
| `--email` | your address; Let's Encrypt uses it only to warn you about an expiring certificate |

Two values from the output matter:

- **`api prefix:`** — a short random word like `/6rfjdqfn5nno`. Write it down. The node will need the
  full address `https://mn.example.com/6rfjdqfn5nno`, prefix included.
- **`admin token`** — the password for every `termit-mn` command. `--admin-token-out` puts it in
  `/etc/termit/mn/admin.token`, so it does not sit in your terminal history.

Check that it is alive:

```bash
export TERMIT_MN_ADMIN_TOKEN_FILE=/etc/termit/mn/admin.token
termit-mn status
```

You should see `server:` with your domain, `api:` with the prefix, `prober: ready`, and an `uptime:` that
is not `0s`. If you see an error instead, the server is not running yet: `systemctl status termit-mn` and
`journalctl -u termit-mn -n 50`.

## 2. Create an invite code

An invite is a one-time ticket that lets exactly one node join. Create one per node:

```bash
termit-mn invite create --note node-a
```

The code (for example `ABCD-EFGH-IJKL`) is printed **once**. Copy it now — you cannot look it up later.

## 3. The node

Log in to the node as `root` and run these commands. They download the installer with `wget` (`curl
-fsSLO <same URL>` works too; a minimal Debian image ships neither), verify the release, install the node
program, and then ask you questions. Without `--version` it installs the newest release;
`sh uv.sh --version vX.Y.Z --wizard` pins one instead.

```bash
# only when the image has neither curl nor wget
apt-get update && apt-get install -y wget
wget -q "https://github.com/shogun-kub/termit/releases/latest/download/uv.sh" && sh uv.sh --wizard
```

The questions, in the order they come, and what to answer:

| The installer asks | Type this | Choose something else when |
|---|---|---|
| `Language / Язык` | `en` or `ru` — the rest of the questions are in that language | never |
| `Domain (a DNS name that points at this machine) — the whole name: subdomain and domain together` | `node-a.example.com` — paste the whole name, subdomain and domain together | — this one is required |
| `Contact address for the certificate authority (it receives expiry notices; optional)` | `you@example.com` — your own mailbox | — it is the address Let's Encrypt warns when the certificate is about to expire; leave it empty and no warning comes |
| `ACME directory (empty = the public one; if unsure, just press Enter)` | `Enter` — Let's Encrypt | a test lab or a company certificate authority: its directory URL, e.g. `https://ca.example.net/acme/dir`. You are then asked whether to skip verifying its certificate |
| `Masking site` | press `Enter` (`portal`) | `product` gives a different-looking ordinary page at your node's address |
| `Egress (all, selected, off)` | `all` — everything proxied leaves through Cloudflare | `off` if you do not want traffic to touch Cloudflare, or if your hoster blocks it; `selected` if only some sites should |
| `Also serve Hysteria2 on UDP/443 beside the TLS profile (yes/no) [y/N]` | `n` or `Enter` — one TLS profile is enough to start | `y` if TCP/TLS is throttled where your people are, or if they need voice and video to work well: the node then also answers on UDP/443, which the firewall must allow |
| `Harden this node (yes/no) [y/N]` | `y` if this machine is only a Termit node | `n` or `Enter` if other services you need run on it (the firewall would cut them off), or you already have your own firewall |
| `Invite code from the server operator` | the code from step 2 | press `Enter` to skip and stay a standalone node for now |
| `Management server URL` | `https://mn.example.com/6rfjdqfn5nno` — the full address, with the prefix from step 1 | — required if you typed an invite |
| `Install this node now?` | `y`, or `Enter` | `n` keeps your answers and starts nothing; run `termit-uv wizard` again when you are ready |

Yes/no questions take `y` or `n`, and the Russian `д` and `н` work too. `Enter` is the default answer: the capital letter in `[y/N]` says which one.

Then the installer works on its own for a minute or two: it installs the proxy core, gets the
certificate, builds the decoy page, starts the services and joins your server. The last lines tell you
what happened.

Every answer is remembered. If you disconnect in the middle, run `termit-uv wizard` again and it
continues where it stopped; if the node got installed but the registration did not go through, it retries
that step with the code it kept.

## 4. Approve the node

Back on the server, the node is waiting for you (this is what the default `invite+approval` mode
means):

```bash
termit-mn uv list
termit-mn uv approve node-a.example.com
```

After this the node shows as `active` — go on to step 5.

## 5. Connect your phone

On the node, print the link:

```bash
termit-uv status
```

Copy the line that starts with `subscribe:` and open it in HAPP, or any client that speaks VLESS with
XHTTP. The app reads the link and adds the connection by itself; nothing else to paste, no password.

## If something does not work

| What you see | What it means | What to do |
|---|---|---|
| `invalid invite` or `code is spent` | the code was already used, or it expired | create a new one on the server: `termit-mn invite create --note node-a` |
| `no such host` or the node cannot reach the server | the address, the prefix or DNS is wrong | compare your address with `termit-mn status`; correct it in `/etc/termit/uv/config.yaml` |
| the installer refuses to request a certificate | the name does not point at this node, or port 80 is closed | check with `getent hosts node-a.example.com` from another machine, fix the A record, then run `termit-uv init`; a proxy or NAT in front is declared with `acme.allow_remote_ip: true` |
| the installer stopped early | nothing is broken, half of it is done | run `termit-uv wizard` again — it keeps your answers |
| the node stays `pending` on the server | `invite+approval` is the default mode | step 4 |

Anything else: `termit-uv doctor` prints a report you can read, and
[When something goes wrong](troubleshooting.md) lists the usual failures.

## Keep three things

| What | Where | Why it matters |
|---|---|---|
| admin token | `/etc/termit/mn/admin.token` | every `termit-mn` command needs it; `termit-mn admin rotate` replaces it |
| master key | `/etc/termit/mn/master.key` | without it the database cannot build a single password — and it cannot be reset |
| database | `/var/lib/termit/mn/termit.db` | your network: the nodes, the links, the invites |

Copy all three somewhere safe and away from the server. A backup of the database without the master key
is a file you cannot use.

## Next steps

- [Using a node](using-uv.md) — every command, updating, changing settings, removing a node.
- [Running the server](using-mn.md) — invites, approvals, blocking, backups.
- [The interactive menu](menu.md) — the same operations as a numbered menu, if you prefer that to flags.
- [Hardening a node](hardening.md) — a firewall and an automatic ban engine for the proxy ports.
- [`docs/RUNBOOK.md`](../RUNBOOK.md) — something is wrong, or you are upgrading: what to do, in order.
- [`docs/OPERATIONS.md`](../OPERATIONS.md) — the reference: what runs where, sizes, WARP, certificates, clients.
