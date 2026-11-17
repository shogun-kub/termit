# Runbook

[English](RUNBOOK.md) · [Русский](RUNBOOK.ru.md)

What to do when something is wrong: every command runs as `root`, and a link to
[`OPERATIONS.md`](OPERATIONS.md) is there when you want to know why. Start at §1 and follow the row that
matches what you see.

## 1. First five minutes

| Symptom | Likely cause | What to do |
|---|---|---|
| `termit-uv init` stops at `preflight` with `ports` failed | something already listens on 80/443 | `ss -H -ltnp`, stop it, re-run `termit-uv init` |
| `dns` warn at preflight | the record does not resolve here yet | point the A/AAAA record at the box; the install does not need it, enrollment does |
| `termit-uv enroll` → `dns_mismatch` | the domain resolves elsewhere, or the request came through a proxy | fix DNS; the check compares against the TCP peer |
| `termit-uv enroll` → `rate_limited` | ten failed attempts in fifteen minutes from one address | wait, then retry; the window is per address |
| `termit-uv enroll` → `domain_taken` | a record with that domain exists | `termit-mn uv show <domain>`; remove or relocate it first |
| node stuck `pending` | `invite+approval` mode | `termit-mn uv approve <node>` |
| node `unavailable` after a passing manual check | the prober cannot reach it | `termit-mn uv show` for the class; check `prober.xray_binary` and the server's own egress |
| `termit-mn status` says `prober: unavailable — every check reports unknown` | no worker could be built | read the reason on the line; fix `prober.xray_binary`, restart `termit-mn`; nodes stay as they were, nothing is blocked |
| `termit-mn status` shows a growing `queue: N due` | the scheduler is not completing passes | `journalctl -u termit-mn` for scheduler errors; a stuck probe worker shows up here before any node changes state |
| node `blocked` right after an upgrade | the certificate was replaced and the server still pinned the old leaf | the next sync reports the new leaf; `termit-uv sync` then `termit-uv unblock` if it stayed blocked |
| `termit-uv doctor` → `services` failed | a unit is down | `systemctl status <unit>`; `termit-uv init` starts them again |
| `termit-uv doctor` → `certificate` warn/fail | renewal is late or broken | `termit-uv renew-cert`; the journal of `termit-uv-renew.service` says why |
| `termit-uv status` shows `warp: … direct` | WARP is down and fail-open is doing its job | `termit-uv warp status` names the reason; traffic still works |
| `termit-uv warp status` shows `not-installed` | there is no WARP identity: `--no-warp` was used, or the state was removed | `termit-uv init` (or `termit-uv warp mode all`) registers and puts it in the tunnel; the command exits 0 in this state |
| `termit-uv warp mode` → `start-limit-hit` | too many restarts in five minutes | `systemctl reset-failed termit-xray termit-uv-agent` |
| `termit-uv sync --check` exits 4 | the core's client set differs from the server's | `termit-uv sync` |
| `mn` commands: `cannot reach the server at …` | `termit-mn` is not running, or the socket moved | `systemctl status termit-mn`; check `admin_socket` in the config |
| `termit-mn serve` refuses to start: `cert: listen on :80` | something holds `:80` (nginx on the same host) | free the port, or set `acme.challenge_listen` to a free one and forward it |
| `termit-mn serve` refuses: `cannot read the master key` | the key file is missing or unreadable | restore it from a backup; without it the registry is useless |
| SQLite `database is locked` | the database is on a slow or network filesystem, or a second process opened it | keep it on local disk; admin commands go through the server, never direct |
| subscription `404` | the token was rotated or mistyped | `termit-uv status` shows the current URL; `termit-mn sub rotate` issues a new one |

When nothing obvious is wrong, the order is `termit-uv doctor` on the node, `termit-mn uv show` on the
server, then both journals. A check that says `unknown` is a failure of the observer, not of the node — say
so in the report instead of "fixing" the node.

## 2. A node is `blocked` and is fine

```bash
termit-uv unblock                    # re-verifies DNS and the tunnel from the node's own address, then syncs
```

If it fails, it prints the server's reason: `dns_mismatch` means the record does not point at the box;
`check_failed` means the tunnel did not answer through the decoy. Fix that and run it again. The states
themselves are in [`OPERATIONS.md`](OPERATIONS.md) §9.1.

## 3. The management server is gone

Restore from backup (§10 below). With no backup the mesh cannot be rebuilt without re-enrolling every node:
pair credentials derive from the master key, and the master key cannot be recovered from a node.

## 4. The admin token leaked

Rotate it: the command replaces the credential it authenticated with, and the old one stops working the
moment the new one is issued:

```bash
termit-mn admin rotate --token-out /etc/termit/mn/admin.token
```

It needs the current token — a rotation you could start without one would be a takeover, not a rotation —
and only the authenticated admin's own row is touched. Before you run it, list what still carries the old
token: every timer, cron entry and playbook that does will fail at its next run with `the admin token is not
valid`. `--token-out` writes the new token `0600`; without it the token is printed once and nowhere else, so
losing that output loses the rotation.

### The admin token is lost

Rotation needs the current token, so this is the one case with no online recovery: the server stores only a
digest, deliberately. Take a backup of the database, then remove the `admin` row and let `init` mint a fresh
credential:

```bash
apt-get install -y sqlite3                        # not part of a minimal Debian
sqlite3 /var/lib/termit/mn/termit.db 'delete from admin;'
systemctl restart termit-mn
termit-mn init --config /etc/termit/mn/config.yaml --admin-token-out /etc/termit/mn/admin.token
```

`init` on an installed server keeps every other decision — domain, listen, master key, the nodes — and only
adds the missing admin. Treat it as an incident: whoever held the old token had the registry, and the digest
in the database no longer matches it.

## 5. A node was rebuilt on the same host

```bash
termit-uv init --domain node1.example.net --relocate --agent-key-file /root/agent-key
termit-uv migrate --relocate
```

The second command tells the server about the new self UUID and certificate; without it the server keeps
probing the old identity and the node stays `blocked` or `unavailable`.

### A node moved to a new host

```bash
termit-uv init --domain node5.example.net --relocate --agent-key-file /root/agent-key
termit-uv migrate --relocate --domain node5.example.net
```

The old record is reused, so peers keep the same pair credential and only the domain and path change in
their lists at the next sync. The new domain must resolve to the new box and the box must serve the profile
it claims, or the server refuses the move and leaves the record as it was.

## 6. The clock drifted

ACME and TLS fail first. `timedatectl status`; fix `systemd-timesyncd` or `chrony`, then `termit-uv
renew-cert --force` if an order was half-finished.

### The node reports drift

```bash
termit-uv sync --check        # exit 4 and a diff
termit-uv sync                # converge now
```

Drift means the core's client set differs from the server's — what a manual edit or a missed sync looks
like. The next cycle would fix it anyway; `termit-uv sync` does it now.

## 7. A node answers, but some connections time out

A client or the prober connects only part of the time, the node's logs are clean, and `ss -s` shows
`syn-recv` entries on the node. Termit reports no error because Termit is not wrong: the packets are being
dropped between two networks. Work through these in order; each one removes a whole class of causes.

1. **Control from the same host.** Ten connections to an unrelated well-known address (a resolver, a large
   site). If those fail too, the local uplink is the problem and the node is not implicated.
2. **Port sweep.** Twelve attempts each to `:443`, `:80`, `:22` and a closed port. If every port loses the
   same fraction, the drop is keyed on the destination, not the port — no port-based filter.
3. **Rate.** Measure after two idle minutes, then right after a burst of 40. The same loss at rest is not
   a rate limit or a shaper reacting to you.
4. **The node's counters**, twice 60 s apart while a stream of attempts runs from the other side:

   ```bash
   nstat -az | grep -E 'TcpExtListenDrops|TcpExtTCPSynRetrans|TcpExtTCPTimeouts'
   ```

   - `ListenDrops` grows → the node's listen queue dropped the SYN (backlog or a local firewall).
   - `TCPSynRetrans` grows while `ListenDrops` stays at zero → the node received the SYN and retransmitted
     SYN-ACKs, so it is the **answer** that is lost on the way back.
   - nothing grows → the SYNs never reached the node.

   `termit-uv net` does this for you: it samples the same counters for `--seconds` (default 30) and prints
   the verdict with the raw deltas next to it, ready for a provider ticket. It opens no sockets, contacts no
   host, and reports `skip` rather than `ok` when the sample saw no traffic — a quiet node and a healthy one
   look identical. The kernel's counters include loopback, so the nginx→Xray hop is counted too; that hop
   never loses handshakes, which makes the reported loss a conservative estimate rather than an inflated one.
5. **Node hygiene**, which rules the host out rather than the path: `nft list ruleset` (any drop or rate
   rule), the conntrack table if the module is loaded, `net.ipv4.tcp_syncookies` and `tcp_max_syn_backlog`.
   An empty ruleset, no conntrack and `syncookies=1` mean the kernel is not the one dropping packets.

Measured this way on a node losing a third of all new connections (2026-11): 12/12 from the same host to
three unrelated destinations; on the node 8/12 to `:443`, 10/12 to `:80`, 8/12 to `:22`, and a closed port
answering `RST` 9/12; the same loss after two idle minutes; under load our side retransmitted 36 SYNs for 9
failed flows while the node answered `ListenDrops +0`, `TCPSynRetrans +61`, `TCPTimeouts +42`; idling, the
node completed 35 handshakes a minute and retransmitted 5 SYN-ACKs. The provider's path was at fault in both
directions — the kernel, the firewall and Termit were innocent. Take those numbers to the provider or move
the node: no Termit setting fixes a lossy uplink.

## 8. You cannot reach the host after `firewall on`

Wait five minutes before anything else: `firewall on` arms a rollback timer *before* it applies, and the
timer removes the table without needing SSH. If you are still in, `termit-uv firewall confirm` keeps it, and
`termit-uv firewall off` removes it from any state; [`OPERATIONS.md`](OPERATIONS.md) §13.2 and
§13.3 have the rules.

## 9. Upgrade
Nodes and the server are separate binaries with separate records: upgrade the server first, then the
nodes. Within `api_version: 1` the wire protocol is additive, so a mixed fleet keeps working.

On the MN:

```bash
systemctl stop termit-mn
install -m 0755 termit-mn.new /usr/bin/termit-mn
systemctl start termit-mn
journalctl -u termit-mn -n 20 --no-pager
termit-mn status
```

The database migrates on start, forward only. **Take a backup before an upgrade**, and keep the previous
binary beside the new one: it will not read a newer schema.

On a node the bootstrap script is the short path — it replaces the binary and converges:

```bash
sh uv.sh --version v0.4.1 --base-url https://github.com/shogun-kub/termit/releases/download --no-enroll
```

By hand it is the same two steps:

```bash
install -m 0755 termit-uv.new /usr/bin/termit-uv
termit-uv init                       # converges every step; no credential is regenerated
termit-uv doctor
```

`termit-uv init` is the upgrade path for everything the node generates — units, vhost, core config, and the
pinned Xray release when `xray.version` moved — and it never rewrites the identity, the certificate, the
decoy seed or the credentials. Restart the agent afterwards so it picks up a configuration it read at start:

```bash
systemctl restart termit-uv-agent
```

A restart in the middle of a burst of restarts can hit systemd's start limit; `termit-uv init`,
`termit-uv warp mode` and the agent's own restart run `systemctl reset-failed` first, so this is handled. If
you restart by hand and see `start-limit-hit`, run `systemctl reset-failed termit-xray termit-uv-agent` and
start again.

## 10. Backup and restore
### 10.1 The management server (backup and restore)

`termit-mn backup` writes an archive containing the database, the configuration, the server's certificate
**and the master key**:

```bash
termit-mn backup                                    # → /var/lib/termit/mn/backups/termit-<ts>.tar.gz
termit-mn backup --out /root/mn-backup.tar.gz
```

The archive holds the master key, which means it can re-derive **every pair credential in the mesh**.
Treat it exactly like the key: encrypt it at rest, store it away from the server, and do not leave it
in a world-readable directory. A backup without the master key restores the registry but cannot
produce working credentials.

Restore, on a fresh machine or after a disaster:

```bash
install -m 0755 termit-mn /usr/bin/termit-mn
termit-mn restore --from /root/mn-backup.tar.gz        # --force to replace an existing database
install -m 0644 deploy/systemd/termit-mn.service /etc/systemd/system/
systemctl daemon-reload && systemctl enable --now termit-mn
termit-mn status
```

`termit-mn restore` validates the archive before writing anything. The restored server keeps every node's
credentials, so nodes keep syncing without re-enrolling. If the address of the server itself changes,
the nodes' `mn.url` changes too (`termit-uv init` writes it, or edit `mn.url` and restart the agent).

**A restore rolls the admin token back with the database.** The archive carries the digest the backup was
taken with, so whichever token an `admin rotate` issued since then stops working — that is a credential in
the registry, and the registry is the backup. Keep the token of the backup with the backup, and rotate
after a restore if you no longer have it (§4).

Back up on a schedule you can defend: daily is normal, and the database is small.

### 10.2 A node

A node's state file is its identity: the self UUID, the XHTTP path, the decoy seed, the agent key and
the subscription token. Losing it means rebuilding the node (Scenario C) rather than restoring it.

```bash
tar -C / -czf /root/node1-state.tar.gz \
    etc/termit/uv var/lib/termit/uv            # configuration, identity, certificate, journal
```

Restoring that archive onto the same machine brings the node back byte for byte, including its
certificate, which is why it is worth keeping: a restore does not wait for an ACME order. Do not copy
it to another machine and keep using it — two boxes with one identity fight over the record; use
`termit-uv init --relocate` for that case.

## 11. Uninstall
On a node:

```bash
termit-uv uninstall --dry-run     # what would be removed
termit-uv uninstall               # units, vhost, core, generated files; keeps config and state
termit-uv uninstall --purge       # also config, state, certificates and the service user
```

What it does: removes the host firewall table if one is applied, disconnects the WARP tunnel, stops and
disables the units, removes every path the install manifest recorded, restores the distribution's
default nginx vhost, and reloads nginx. The table goes first, and on purpose: a default-deny table with no
command left to remove it is a host nobody can repair without a console. What it deliberately keeps:
**packages** (nginx may serve other sites; remove `cloudflare-warp` yourself if you want it gone), and —
without `--purge` — the configuration and the state, so a reinstall keeps the same identity.

nginx itself keeps running, because it may serve other sites: the stock vhost takes `:80` back, while
`:443` stays open with a vhost that no longer exists until nginx is restarted — `systemctl restart nginx`
releases it. `systemctl daemon-reload` is worth running too: the hardening drop-in is gone from disk but
systemd has it cached, and it says so.

On the MN:

```bash
systemctl disable --now termit-mn
rm -f /etc/systemd/system/termit-mn.service /etc/systemd/system/termit-mn-*.service \
      /etc/systemd/system/termit-mn-*.timer
rm -rf /etc/termit/mn /var/lib/termit/mn
systemctl daemon-reload
```

The wildcard covers the four unit names the release ships beside the server's own — `termit-mn-firewall`,
`-firewall-rollback`, `-guard` and `-blocklist`, six files with the two timers
([`OPERATIONS.md`](OPERATIONS.md) §13.6) — which a server that ever ran `termit-mn firewall on` has
installed. Leaving them behind leaves a boot unit that does nothing and a timer that keeps trying.

There is no uninstall command for the server: it owns two directories, the socket's own directory and one
unit, and deleting a registry is not something an automated command should decide to do.

