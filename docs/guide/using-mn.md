# Running the management server (termit-mn)

[English](using-mn.md) · [Русский](ru/using-mn.md)

The management server is the one box that keeps the mesh honest: it holds the membership list, checks
every node from the outside, and serves each owner a subscription. It never carries your traffic.

You need: a Debian 13 box (2 vCPU, 2 GB RAM, 10 GB disk), root, a DNS name pointing at it, and ports
80 and 443 free. One server is enough for a group of up to a few dozen nodes, and if it is down, existing
connections keep working. A second one can hold a copy of the registry and answer nodes whose home is
down — see [A second server](#a-second-server). That is replication, not clustering: every node still has
one home.

If you would rather pick from a list than remember flags, run `termit-mn` with no arguments on the server:
that is [the interactive menu](menu.md).

## Install

Download the installer and run it: it resolves the newest release, verifies its checksum, installs the
binary and its unit, installs the probe worker, creates the configuration, starts the service and prints
the status. `wget` is not guaranteed: Debian's minimal and netinst images ship neither it nor `curl`, so
install one first (`apt-get update && apt-get install -y wget`); `curl -fsSLO <same URL>` works too:

```bash
apt-get update && apt-get install -y wget   # only when the image has neither
wget -q https://github.com/shogun-kub/termit/releases/latest/download/mn.sh
sh mn.sh --domain mn.example.com --email you@example.com --admin-token-out /etc/termit/mn/admin.token
```

`--version vX.Y.Z` installs that release instead of the newest (`vX.Y.Z` is a release tag —
`v0.7.0`, for example); `--base-url` points the script at a mirror, a `file://` URL or a local
directory (where `--version` is required, because a mirror cannot answer "newest"). Re-running the
script is the upgrade, and it needs no flags: the domain is read from the installed configuration,
the newest release is resolved again, and the service is restarted. `--mode` is passed through to
`init`:

| Mode | What happens when a node enrols |
|---|---|
| `invite` | the invite code is enough |
| `invite+approval` (default) | the node waits as `pending` until you run `termit-mn uv approve` |
| `open` | no invite needed — only sensible on a private network |

Changing the mode later is `termit-mn invite policy --mode <mode>`, which edits the one value in
`config.yaml`, keeps your comments, and refuses `open` without `--force`. The mode is read at startup, so
the command ends by saying so: the running server keeps the old rule until `systemctl restart termit-mn`.
`termit-mn invite policy` without `--mode` prints the rule in force.

Upgrading the server itself is `termit-mn update`: without a flag it asks GitHub for the newest published
release and says whether this build is behind. `--apply` downloads it, checks the digest against the
release's `SHA256SUMS`, keeps the previous binary as `termit-mn.prev`, restarts the unit and puts the old
binary back if that restart fails. It is deliberately absent from `status` — the server answers questions
about the mesh, and a check that reached GitHub on every status call would be its only network dependency
besides its own nodes. `--release-base` points it at a mirror.

`termit-mn init` creates the configuration, the database, the master key and the first admin token, and
prints the secrets **once** (`--admin-token-out` sends the token to a file instead of printing it):

```
domain:       mn.example.com
listen:       :443
api prefix:   /6rfjdqfn5nno
masking site: portal (what a prober gets for every path the API does not claim)
config:       /etc/termit/mn/config.yaml
database:     /var/lib/termit/mn/termit.db (schema 9)
master key:   /etc/termit/mn/master.key (generated now)

admin token (shown once, store it now):
  <token>

first invite code (shown once, 1 use(s)):
  ABCD-EFGH-IJKL
```

Two things to note. The **api prefix** is where the node API lives: `init` picks a random path, so a
scanner probing `/api/v1` on your server finds nothing, and every node is given the full URL —
`https://mn.example.com/6rfjdqfn5nno` — rather than the bare host. It is not a secret, and
[the API reference](../MN-API.md) says what is behind it. `--api-prefix /api/v1` keeps the old,
discoverable path if you want it.

The **masking site** is what a visitor gets instead: an ordinary-looking page at `/`, its own 404 for
anything else, and nothing that mentions Termit. A host that answered `404 page not found` for every
path would be a fingerprint in itself. `--decoy-template product` picks the other archetype (the nodes
serve the same two), and `decoy.template` in the configuration changes it later. The page is generated
at startup and held in memory; there is nothing to edit on disk.

Two things to keep, and keep together: the **admin token** (every `mn` command needs it) and the
**master key** (`/etc/termit/mn/master.key`). Without the master key the database cannot derive a
single pair credential — it is not a password you can reset, it is the salt of the whole registry.
Back them up with the database, and nowhere near it.

Put the token in the environment, so it stays out of your shell history:

```bash
export TERMIT_MN_ADMIN_TOKEN='<token>'
```

Or keep it in a file — that is what a timer, a cron job or an Ansible task needs, because none of them
has a shell to export from. The CLI reads it only when asked, and refuses a file it cannot vouch for:

```bash
# write it once, readable by nobody else
install -m 0600 /dev/null /etc/termit/mn/admin.token
printf '%s\n' '<token>' > /etc/termit/mn/admin.token

termit-mn status --token-file /etc/termit/mn/admin.token
export TERMIT_MN_ADMIN_TOKEN_FILE=/etc/termit/mn/admin.token   # or name it once per shell
```

The file must be mode `0600` and owned by the user running the command; anything else is refused with
the `chmod` that fixes it. A token file readable by the group or the world is a leaked credential that
looks like a tidy setup.

`termit-mn init --admin-token-out FILE` writes the token straight into a file with mode `0600` instead
of printing it, so a provisioned server never has its only copy in somebody's scrollback:

```bash
termit-mn init --domain mn.example.com --email you@example.com \
  --admin-token-out /etc/termit/mn/admin.token
```

If that write fails, `init` prints the token anyway and exits non-zero: the token exists in the database
by then, and a token nobody saw would leave a server nobody can administer.

If the token leaks, rotate it. The command replaces the credential it authenticated with — its own, not
somebody else's — and hands back the new one once:

```bash
termit-mn admin rotate --token-out /etc/termit/mn/admin.token
```

Every script, timer and playbook that still carries the old token stops working at that moment. That is
what rotation is for, and it is also the thing to check before running it. Rotating needs the current
token: if that is what you have lost, the recovery is in
[the runbook](../RUNBOOK.md#the-admin-token-is-lost).

## First start

The script installs the unit and starts it. By hand the same steps are:

```bash
install -m 0644 termit-mn.service /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now termit-mn
journalctl -u termit-mn -n 30 --no-pager
```

The same release carries the server's hardening units (`termit-mn-firewall*`, `termit-mn-guard`,
`termit-mn-blocklist*`). They are installed only if you decide to harden the server, and the firewall
refuses to run without them: [Hardening a node](hardening.md) has the rest.

The first start **issues the certificate before it serves TLS**, and refuses to start if that fails:
a management API that silently falls back to HTTP is worse than one that does not come up. Expect
a minute or two, then:

```
level=INFO msg="issued a certificate" domain=mn.example.com not_after=…
level=INFO msg="serving TLS" domain=mn.example.com listen=:443 not_after=…
level=INFO msg="admin socket ready" path=/run/termit/mn/admin.sock
```

Verify:

```bash
curl -sS https://mn.example.com/6rfjdqfn5nno/healthz   # {"status":"ok", …}
termit-mn status                                       # prints the prefix as well
```

`termit-mn status` is the screen to open first, every time:

```
server:   mn.example.com
listen:   :443
api:      /6rfjdqfn5nno
decoy:    portal
version:  termit-mn vX.Y.Z (commit …, built …, api 1, profile v1)
uptime:   3h12m (started 2026-11-10 11:18)
database: /var/lib/termit/mn/termit.db (412.0 KiB, schema 9)
prober:   ready (/usr/local/lib/termit/xray)
queue:    0 due, next 2026-11-11 12:34 UTC
cert:     2027-02-08T12:06:44Z (in 89 days)
mesh:     mn_6f3c0a5e1b2d4c8f (master key: yes, epoch 2)
  peer mn2      https://mn2.example.com/6rfjdqfn5nno — up to date (41 entries)
nodes:    3 (3 active)
check:    every 24h, times below are UTC

DOMAIN             STATE   FAILED  LAST CHECK         NEXT CHECK         LAST SEEN
node1.example.com  active  0       2026-11-10 13:29   2026-11-11 12:34   2026-11-10 13:31
```

Two lines catch a mesh that looks fine and is not: `prober: unavailable` means no check can produce a
verdict (every node stays as it was, nothing is blocked), and a growing `queue: N due` means the
scheduler is falling behind. Neither is a node failure.

The server renews its own certificate: it issues at startup, re-checks every six hours, and swaps the
new file in without a restart. `cert:` above shows what it is actually serving.

## Invite nodes

```bash
termit-mn invite create --note "node-a for Anna" --uses 1
termit-mn invite create --uses 5 --expires-hours 48      # a batch with a deadline
termit-mn invite list
termit-mn invite revoke ABCD-EFGH-IJKL
```

An invite code is a secret with a limited number of uses; pass it over a channel you trust, and
revoke it if it leaks. Each use consumes one slot.

`invite create` prints what to forward — the command that starts the wizard, and the two answers the
person on the other side will be asked for besides their own node's domain:

```
send this to them (the wizard asks for their own domain; these are the other two answers):

  sh uv.sh --wizard
  server: https://mn.example.com/6rfjdqfn5nno
  invite: ABCD-EFGH-IJKL

for: node-a for Anna
```

## Watch and manage nodes

| Goal | Command |
|---|---|
| list nodes | `termit-mn uv list` |
| inspect one node | `termit-mn uv show node1.example.com` — state, history, last probe and self-check, and where its traffic actually leaves from |
| admit a pending node | `termit-mn uv approve node1.example.com` |
| reject a pending node | `termit-mn uv reject node1.example.com` |
| block a node yourself | `termit-mn uv block node1.example.com --reason "abuse report"` |
| undo your block | `termit-mn uv unblock node1.example.com` |
| take a node out of the mesh | `termit-mn uv rm node1.example.com` |
| erase the record | `termit-mn uv forget node1.example.com` (after removal only) |
| read a node's agent key | `termit-mn uv agentkey node1.example.com` |
| replace a node's agent key | `termit-mn uv reissue node1.example.com` |
| check now | `termit-mn check now node1.example.com`, or `termit-mn check now --all` |
| tune the schedule | `termit-mn policy show`, `termit-mn policy set check_interval 12h` |
| make the grace longer or shorter | `termit-mn policy set block_after_failed_cycles 4` — how many failed checks in a row block a node (default 2, i.e. one grace check) |
| rotate a subscription token | `termit-mn sub rotate node1.example.com --domain mn.example.com` |

What the states mean:

| State | Meaning | Leaves it by |
|---|---|---|
| `pending` | enrolled, not admitted yet (`invite+approval`) | `termit-mn uv approve` |
| `active` | checked and working | — |
| `unavailable` | a check ran and genuinely failed (certificate, decoy, tunnel) | passing checks |
| `blocked` | the configured number of failed checks in a row (`policy.block_after_failed_cycles`, default 2), or an operator decision | `termit-mn uv unblock`, or the node asking with `termit-uv unblock` |
| `removed` | out of the mesh; credentials stop working | re-enrolment (new record) |

The state machine only moves on a verdict. A check that could not run — prober down, DNS broken on
the server, timeout — is `unknown` and changes nothing. That is why a node can look stale in
`termit-mn uv show` and still be fine.

`block` and `unblock` are your word against the prober's: a passing check does not undo your block,
which is what keeps a flapping check from flapping the mesh.

### What `uv show` says about egress

The server does not take the node's word for its egress, and it does not guess: it fetches Cloudflare's
trace through the node's own tunnel on every probe cycle and records the address the request arrived from
and whether WARP carried it. `uv show` prints that beside what the node says its configuration asks for:

```
egress:  warp via 104.28.1.2 (measured 2026-11-28T12:00:00Z)
egress:  direct via 203.0.113.9 (measured 2026-11-28T12:00:00Z), but the node is configured for warp
```

The second line is the state worth acting on: the tunnel is down and fail-open sent the traffic out
directly, so the address the owner wanted to hide is exposed. A node that was never measured prints no
egress line at all — unknown is not direct. The target is Cloudflare's trace endpoint, fixed in the
prober: with `warp.mode: selected` the answer is about that domain, not about every domain a client
reaches.

## Subscriptions

Every node has one token. The endpoints are:

| URL | Contents |
|---|---|
| `https://mn.example.com/sub/<token>` | base64 of the URIs, one per line — the "subscription URL" a client wants; a node that serves the QUIC transport adds its `hy2://` beside the `vless://` |
| `https://mn.example.com/sub/<token>/json` | the same peers as a sing-box profile, with a selector, `direct` for private ranges and a `hysteria2` outbound when the node serves one |

Both are stable for a given peer set and carry an `ETag`, so a caching client costs one `304`. A node
shows its own URL in `termit-uv status`. Rotating the token invalidates the old URL immediately; it does not
touch the node's own local devices, which never leave the box.

A node that is `pending` or `removed` gets no list: its URL answers `404`, the same answer an unknown token
gets. A `blocked` node keeps its URL, but its peers have dropped its credential, so nothing works through
the mesh until an unblock.

## Backup and restore

```bash
termit-mn backup                                        # <data_dir>/backups/termit-<timestamp>.tar.gz
termit-mn backup --out /root/mn-backup.tar.gz
```

The archive holds the database and the master key. Keep it encrypted, and keep a copy off the server:
whoever holds both can derive every pair credential in the mesh.

```bash
termit-mn restore --from /root/mn-backup.tar.gz --force  # server stopped first
```

Restore is an offline operation: stop `termit-mn`, restore, start it. A restored database is only
useful with the same master key — if you have the database but not the key, the nodes will have to
re-enrol.

## A second server

A second `termit-mn` can hold a copy of the registry, so a node whose home is down is still answered: it
gets the last state the home published, marked read-only, and its report is queued until the home returns.
This is not clustering and not a load balancer: every node has exactly one home, and only that home writes
its row.

1. **Give both servers the same master key**, once, out of band: the key file of the first copied to the
   second, `0600`. A host that holds the key can derive every node's credentials, so it belongs on a host
   you would let take over. `serve` also reads `TERMIT_MN_MASTER`, so a standby can be handed its key
   without writing it to disk, and `termit-mn status` prints which source this process used.
2. **Name the other server on each side** in `config.yaml`: `mesh.peers` with a name, a base URL that
   carries the peer's API prefix, and an optional `leaf_sha256` pin, plus `pull_every` and `lag_max`. Then
   restart both. Each pulls the other every `pull_every`; the first pull takes a snapshot of the whole
   registry.
3. **Read `mesh:` in `termit-mn status`**: this server's id, the epoch, and per peer whether it answered
   and how far behind it is.

Moving a node between the two is explicit: `termit-mn home handoff --uv <id|domain> --to <peer>` gives it
a new agent key and one more home term, and prints the two commands the node runs. A home that never comes
back is `termit-mn home adopt --from <peer>`, which refuses while the old home answers; nodes enrolled
after this server's last pull are unknown here and have to enroll again. `termit-mn epoch rotate`
re-derives every pair credential, and refuses unless every configured peer answers.

The whole runbook — the key, the name, the failure cases — is [`OPERATIONS.md`](../OPERATIONS.md) §3.4.

## Monitoring

`/metrics` is available on loopback only, in Prometheus format, and never names a node:

```
termit_mn_up 1
termit_mn_nodes{state="active"} 3
termit_mn_check_queue_due 0
termit_mn_prober_up 1
termit_mn_checks_total{result="ok"} 42
termit_mn_certificate_expiry_timestamp_seconds 1801439850
```

Alert on: `up` missing, `certificate_expiry_timestamp_seconds` under a week away, `check_queue_due`
growing, `prober_up 0`, and a rising `checks_total{result="failed"}`. Node-level detail stays in
`termit-mn uv show` and the logs — the endpoint deliberately has no inventory.

Logs go to the journal, structured, with node identifiers and never with credentials:

```bash
journalctl -u termit-mn -f
```

## Command reference

| Command | What it does |
|---|---|
| `termit-mn init` | create config, database, master key, admin token and the first invite; `--admin-token-out FILE` writes the token to a file instead of printing it; `--locale ru\|en` picks the menu language |
| `termit-mn serve` | run the server (`--http` for development only, never on a public address) |
| `termit-mn invite create\|list\|revoke\|policy` | manage invite codes, and the enrollment rule a new node meets |
| `termit-mn uv list\|show\|approve\|reject\|block\|unblock\|rm\|forget\|reissue\|agentkey` | manage nodes |
| `termit-mn check now <node>\|--all` | run health checks immediately |
| `termit-mn home handoff --uv <node> --to <peer>` | move one node to another server: new agent key, one more home term, the two commands for the node |
| `termit-mn home adopt --from <peer> [--uv <node>]` | take over the nodes of a home that is gone; refuses while it answers |
| `termit-mn epoch rotate` | re-derive every pair credential in the mesh; refuses unless every configured peer answers |
| `termit-mn uv forget --force <node>` | drop this server's copy of a row another server homes; nothing is journaled |
| `termit-mn policy show\|set` | read or change scheduler thresholds |
| `termit-mn sub rotate <node>` | issue a new subscription token |
| `termit-mn admin rotate` | replace the admin token you authenticated with; `--token-out FILE` writes it |
| `termit-mn status` | server, nodes and schedule |
| `termit-mn backup`, `termit-mn restore` | snapshot and restore |
| `termit-mn update` | install the newest published release; `--check` (the default) reports, `--apply` replaces the binary and restarts the unit |
| `termit-mn firewall` | the server's own table: `status`, `on`, `off`, `confirm`, `refresh`, `restore` |
| `termit-mn bans`, `termit-mn guard` | the ban engine on the server |
| `termit-mn version` | build and protocol version |

Admin commands talk to the running server over a unix socket (`/run/termit/mn/admin.sock`) and
authenticate with `TERMIT_MN_ADMIN_TOKEN`, `TERMIT_MN_ADMIN_TOKEN_FILE`, `--token` or `--token-file`.
They never open the database behind the server's back.

The firewall, bans and guard are the same opt-in machinery the nodes have, with the server's own table
name and ports: [Hardening a node](hardening.md) is the page for it.

## Rules worth keeping

- **Never edit the database by hand.** It is the registry; admin commands are the interface.
- **Back up the master key with the database, and keep both encrypted.** Nothing can be recovered
  without the key.
- **One server per database.** Two instances on one database corrupt it, and the second cannot issue
  certificates for the same name anyway. A second *server* is a replica on its own database, see
  [A second server](#a-second-server).
- **Keep `termit-mn status`' prober line green.** A dead prober is not a broken mesh, but it is a blind one.
- **Protect the admin token like the master key.** It is created by `termit-mn init` and stored hashed;
  `termit-mn admin rotate` replaces it, and the old one stops working immediately. Keep it in a file with
  mode `0600` rather than in a shell history, and keep it out of backups you share.

Next: [Using a node](using-uv.md) · [When something goes wrong](troubleshooting.md)
