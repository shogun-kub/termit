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
already resolves.

Wherever you see `mn.example.com` or `node-a.example.com`, use your own names.

## The order of work

1. Set up the server.
2. Create an invite code on the server.
3. Set up the node — one command, then the installer asks questions.
4. Approve the node on the server.
5. Copy one link into your phone.

## 1. The server

Log in to the server as `root` and paste this block. It downloads the version, checks the file against
its published checksum, and installs it.

```bash
RELEASE=v0.4.1
curl -fsSLO "https://github.com/shogun-kub/termit/releases/download/$RELEASE/termit-mn_${RELEASE}_linux_amd64"
curl -fsSLO "https://github.com/shogun-kub/termit/releases/download/$RELEASE/SHA256SUMS"
sha256sum -c --ignore-missing SHA256SUMS
install -m 0755 "termit-mn_${RELEASE}_linux_amd64" /usr/bin/termit-mn
```

Now create the configuration. This one line writes the settings, the database and your admin password,
and prints a few lines you will need:

```bash
install -d -m 0750 /etc/termit/mn
termit-mn init --domain mn.example.com --email you@example.com \
  --admin-token-out /etc/termit/mn/admin.token
```

What to put instead of the placeholders:

| | |
|---|---|
| `--domain` | the server's name, the one that already points at this machine |
| `--email` | your address; Let's Encrypt uses it only to warn you about an expiring certificate |

Two lines of that output matter:

- **`api prefix:`** — a short random word like `/6rfjdqfn5nno`. Write it down. The node will need the
  full address `https://mn.example.com/6rfjdqfn5nno`, prefix included.
- **`admin token`** — the password for every `termit-mn` command. It is written to
  `/etc/termit/mn/admin.token`, so it does not sit in your terminal history.

Start the server:

```bash
curl -fsSLO "https://github.com/shogun-kub/termit/releases/download/$RELEASE/termit-mn.service"
install -m 0644 termit-mn.service /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now termit-mn
```

Check that it is alive:

```bash
export TERMIT_MN_ADMIN_TOKEN_FILE=/etc/termit/mn/admin.token
termit-mn status
```

You should see `server:` with your domain, `api:` with the prefix, and an `uptime:` that is not
`0s` — that means it is running. If you see an error instead, the server is not running yet:
`systemctl status termit-mn` and `journalctl -u termit-mn -n 50`.

## 2. Create an invite code

An invite is a one-time ticket that lets exactly one node join. Create one per node:

```bash
termit-mn invite create --note node-a
```

The code (for example `ABCD-EFGH-IJKL`) is printed **once**. Copy it now — you cannot look it up later.

## 3. The node

Log in to the node as `root` and paste this single line. It downloads the installer, verifies the
release, installs the node program, and then asks you questions.

```bash
curl -fsSLO "https://github.com/shogun-kub/termit/releases/download/v0.4.1/uv.sh" && sh uv.sh --version v0.4.1 --wizard
```

The questions, in the order they come, and what to answer:

| The installer asks | Type this | Choose something else when |
|---|---|---|
| `Language / Язык` | `en` or `ru` — the rest of the questions are in that language | never |
| `Domain (a DNS name that points at this machine)` | `node-a.example.com` | — this one is required |
| `Contact address for the certificate authority` | `you@example.com` | — you get a warning if the certificate is about to expire |
| `Masking site` | press `Enter` (`portal`) | `product` gives a different-looking ordinary page at your node's address |
| `Egress (all, selected, off)` | `all` — everything proxied leaves through Cloudflare | `off` if you do not want traffic to touch Cloudflare, or if your hoster blocks it; `selected` if only some sites should |
| `Harden this node (yes/no) [y/N]` | `y` if this machine is only a Termit node | `n` or `Enter` if other services you need run on it (the firewall would cut them off), or you already have your own firewall |
| `Invite code from the server operator` | the code from step 2 | press `Enter` to skip and stay a standalone node for now |
| `Management server URL` | `https://mn.example.com/6rfjdqfn5nno` — the full address, with the prefix from step 1 | — required if you typed an invite |
| `Install this node now?` | `y`, or `Enter` | `n` keeps your answers and starts nothing; run `termit-uv wizard` again when you are ready |

Yes/no questions take `y` or `n`, and the Russian `д` and `н` work too. `Enter` is the default answer: the capital letter in `[y/N]` says which one.

Then the installer works on its own for a minute or two: it installs the proxy core, gets the
certificate, builds the decoy page, starts the services and joins your server. The last lines tell you
what happened.

Every answer is remembered. If you disconnect in the middle, run `termit-uv wizard` again and it
continues where it stopped.

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
| the certificate step fails | the node's DNS record does not resolve yet, or port 80 is closed | check with `getent hosts node-a.example.com` from another machine, then run `termit-uv init` |
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
