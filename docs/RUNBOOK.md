# Runbook

[English](RUNBOOK.md) · [Русский](RUNBOOK.ru.md)

What to do when something is wrong: every command runs as `root`, and a link to
[`OPERATIONS.md`](OPERATIONS.md) is there when you want to know why. Start at §1 and follow the row that
matches what you see.

## 1. First five minutes

| Symptom | Likely cause | What to do |
|---|---|---|
| `termit-uv init` stops at `preflight` with `ports` failed | something already listens on 80/443 | `ss -H -ltnp`, stop it, re-run `termit-uv init` |
| `dns` warned at preflight | the record does not point at this box yet | point the A/AAAA record at this server: the certificate is refused until it does. A verified proxy or NAT in front declares itself with `acme.allow_remote_ip: true` |
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
| `termit-uv upgrade` → `the server has not reported its version yet` | the node has not synced since this build learned to remember it | `termit-uv sync`, then `termit-uv upgrade`; or name the release with `--to vX.Y.Z` |
| `termit-uv upgrade` → `did not converge the node` | the new binary could not converge this host | the previous binary is back in place and nothing else changed; fix what the step printed, then run it again |
| `termit-mn update --apply` → `the previous binary is back in place; restart the server to run it` | the new build did not survive the restart | `systemctl restart termit-mn`, read the journal, then `termit-mn status` |

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

**If a replica exists, the nodes keep working while you read this.** A second `termit-mn` with the same
master key and `mesh.peers` set answers each node with the last state it holds, marked `read_only`, and
queues the report for the home. Two ways back, in this order:

1. **The machine comes back.** Nothing to rescue: the replica has already handed the queued reports to it,
   and the nodes never changed their `mn.url`. Check `mesh:` in `termit-mn status` on both.
2. **The machine is gone for good.** Point the name at the replica, then `termit-mn home adopt --from
   <peer>` on it: the replica takes the rows and keeps the agent keys, so no node is visited. Nodes
   enrolled after the replica's last pull are unknown there and enroll again. A node that was moved to a
   third server is `home handoff --uv <node> --to <peer>` once that server answers.

The full runbook — the key, the name, the node recovery — is `OPERATIONS.md` §3.4.

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

On the MN, the binary can replace itself. `--check` is the default and installs nothing:

```bash
termit-mn update --check      # follow releases/latest and say whether this build is behind
termit-mn update --apply      # download, verify against SHA256SUMS, swap, restart
journalctl -u termit-mn -n 20 --no-pager
termit-mn status
```

`--apply` keeps the running binary as `/usr/bin/termit-mn.prev` and puts it back if
`systemctl restart termit-mn` fails, so a failed update leaves the previous binary in place; the
database migrates on start, forward only, and the previous binary will not read a newer schema.
`termit-mn update --apply` and `mn.sh` take a backup of the registry themselves, print its path and
refuse to go on if it fails; replacing the binary by hand means taking one first (§3.5.1) — that is
the only way back if the new build is wrong rather than broken.
Re-running `mn.sh` is still the way to move the probe worker or the unit, which `termit-mn update` does
not touch.

On a node the short path follows the server rather than the release feed:

```bash
termit-uv upgrade --check    # what a run would install: the server's version, nothing downloaded
termit-uv upgrade            # install it, keep termit-uv.prev, converge with the new binary
termit-uv doctor
```

`termit-uv upgrade` installs the version the management server reported at the last sync — never
`latest` — verifies it, writes it beside the running binary and lets **it** run `init`, because the
new release is the one that knows what it needs. A converge that fails restores the previous binary
by itself. If it says the server has not reported its version, run `termit-uv sync` first (or name
the release: `--to vX.Y.Z`). The bootstrap script is the other short path — it installs the newest
release, or the one `--version` names, and runs the same `init` (`vX.Y.Z` is a release tag —
`v0.7.0`, for example):

```bash
sh uv.sh --no-enroll                     # the newest release
sh uv.sh --version vX.Y.Z --no-enroll    # or exactly this one
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
rm -rf /etc/termit/mn /var/lib/termit/mn /usr/local/lib/termit
systemctl daemon-reload
```

`/usr/local/lib/termit` is the probe worker `mn.sh` installs; without it a reinstall is a server that
cannot check a single node.

The wildcard covers the four unit names the release ships beside the server's own — `termit-mn-firewall`,
`-firewall-rollback`, `-guard` and `-blocklist`, six files with the two timers
([`OPERATIONS.md`](OPERATIONS.md) §13.6) — which a server that ever ran `termit-mn firewall on` has
installed. Leaving them behind leaves a boot unit that does nothing and a timer that keeps trying.

There is no uninstall command for the server: it owns two data directories, the socket's own directory,
the probe worker in `/usr/local/lib/termit` and its units, and deleting a registry is not something an
automated command should decide to do.


## 12. The QUIC transport does not come up

The link from the subscription or from the node's own page fails, while the TLS profile on the same node
works. Work through these in order; each one removes a class of causes.

1. **Is it running, and on which port?** `systemctl status termit-hysteria` and `ss -lunp | grep ':443'`.
   A unit that is not `active` has a reason in `journalctl -u termit-hysteria -n 50`: usually a missing
   binary (`termit-uv init` installs it) or a config that stopped parsing after a hand edit.
2. **Is it the unit or the box?** Restart it and watch the journal. A start that is killed within seconds
   by the kernel is the memory cap doing its job — `journalctl -k | grep 'Killed process'` names it. That
   is GO-2026-5288 bounded: the transport comes back, the node's other services never notice.
3. **Can the node read its own key?** `ls -l /var/lib/termit/uv/certs`: `privkey.pem` must be
   `-rw-r----- root termit-hy` inside `drwxr-x--- root termit-hy`. Anything else is a node installed by an
   older build; `termit-uv init` fixes the modes, `chown` alone does not.
4. **Is the firewall carrying it?** With the table on, `nft list chain inet termit_uv input | grep 'udp
   dport 443'` must show the accept, and `security.firewall.udp_ports` must list `443`. `termit-uv init`
   refuses to enable the transport without it, so a node with the transport on and no rule was edited by
   hand.
5. **Does the node accept that credential?** `grep -c '^    u' /etc/termit/uv/hysteria.yaml` is the number
   of accepted users: the node's own device, its local devices, every peer, and the server's probe. A link
   whose user name is not in that file points at a node that has not synced since its user set changed.
6. **Is the client the broken part?** Check that the client's TLS stack works at all before blaming the
   node: `hysteria client -c client.yaml` with the same link on the same machine, then a request through
   its SOCKS port. If HTTPS fails with an error from the client's own TLS library while the tunnel answers
   plain HTTP, the tunnel is fine and the client is not. This is not hypothetical — a Windows `curl` built
   against Schannel failed every HTTPS request through a healthy transport.
7. **UDP, or the path?** If the handshake never completes and plain HTTP over the same tunnel works,
   suspect the network: some paths drop or throttle UDP/443. `tcpdump -ni any 'udp port 443'` on the node
   shows whether the client's packets arrive at all; none arriving is a path or firewall problem, not a
   Termit one.
8. **Has a client been banned for its own QUIC?** With the table on and no `udp dport 443` accept in it, a
   participant's QUIC probes land in the scan meter, and the guard bans the source — the ban then drops
   everything from that address, VLESS included. `termit-uv bans list` names it as `scan`; `termit-uv bans
   unban <ip>` lifts it, and `termit-uv firewall on` + `confirm` removes the cause. An address banned this
   way is not a scanner, it is a client the table did not let in.

## 13. A reboot restored an older firewall table

`termit-uv firewall restore` — the boot unit — puts back the record the last confirmed `on` wrote, and that
record does not follow the configuration. A port added afterwards (`security.firewall.udp_ports: [443]`,
when Hysteria2 was turned on, for instance) is missing from the kernel until the next `firewall on`; and a
missing UDP port is not merely closed: its packets reach the scan meter.

The drift is said out loud now: `termit-uv firewall status` prints the applied UDP ports and names the fields
that moved (`udp_ports`, `ports`, `ssh_from`, `default_deny`), `termit-uv doctor` warns about the same, and
`termit-uv firewall restore` writes it to the boot journal. The table is still applied only by `firewall on`:

    termit-uv firewall on
    termit-uv firewall confirm

The record that `on` writes is what the next reboot restores, so the warning goes away with it.