# Hardening a node: the firewall, bans and the blocklist

[English](hardening.md) · [Русский](ru/hardening.md)

The tunnel works without any of this. This page is about the other half of a public machine: what it
answers to everything that is *not* your clients. Termit ships a host firewall and never turns it on —
`init`, `sync`, the agent and the timers leave it alone. You decide, once, and the decision is a command.

It is worth it if your node's address is public and you would rather be a closed door than a port
scanner's practice target. It is not worth it if you are unsure: an opt-in firewall you have not
confirmed yet is also a way to lock yourself out, which is why the first section is about the way back.

Nothing on this page changes routing or how traffic leaves the box. Proxied traffic still goes through
the proxy core and WARP; direct traffic stays direct.

## What the table contains

One nftables table, `inet termit_uv`, in the order the input chain reads it:

| Rule | Why |
|---|---|
| `iif lo accept` | loopback is not a decision |
| `ct state established,related accept` | a flow already running is not re-judged |
| `ct state invalid drop` | malformed state is never legitimate |
| `@blocked4` / `@blocked6` drop | the downloaded list, below |
| `@banned4` / `@banned6` drop | what the ban engine decided |
| ICMPv6 neighbour discovery, packet-too-big, unreachable, time-exceeded | without these IPv6 stops working |
| `tcp dport { 22, 80, 443 } accept` | what a node publishes |
| a scan meter, then `counter drop` | everything else is counted per source, then refused |

Two properties matter more than the rules:

- **It is one atomic transaction.** The whole ruleset goes to `nft -f -`, which commits it or nothing. A
  failure leaves the previous rules in force; re-applying the same rules replaces rather than merges.
- **Bans live in sets, not in rules.** Adding a banned address never leaves the chain half-written.

## Turning it on without locking yourself out

```bash
termit-uv firewall on --dry-run     # print the rules; change nothing
termit-uv firewall on               # arm the timer, apply, write the record
termit-uv firewall confirm          # within five minutes
```

`on` arms a five-minute rollback timer **before** it touches the kernel. If you cannot reach the box to
confirm — because the rule you just added refused your own SSH — the timer's oneshot deletes the table
and the host is where it was. No SSH is needed for that: it runs locally.

So after `on`, check the two things you care about before you close the session: SSH still answers, and
`https://node1.example.com/` still serves. Then confirm.

If you reach SSH from a fixed address, say so and port 22 is refused from everywhere else — the networks
are compiled into the table, so the restriction survives a rebuild:

```bash
termit-uv firewall on --ssh-from 203.0.113.0/24,2001:db8::/32
termit-uv firewall off              # the way back, from any state, asks nothing
```

To make it the default, put the same networks in `security.firewall.ssh_from` in the configuration, or
run `termit-uv init --harden`, which writes the firewall and ban settings without applying anything.

## What `status` says

```
firewall:   on, not confirmed — the table is removed in 4m55s unless you run `termit-uv firewall confirm`
firewall:   on, confirmed 3m ago
firewall:   off since 1s ago
```

If the configuration asks for a table and none is applied, `status` says that too and names the command.
A table applied but not recorded — somebody used `nft` by hand — is reported as such, and `off` then `on`
puts it back in writing.

## After a reboot

A table is a kernel object, so a reboot empties it. The record of what you applied does not, and the boot
unit `termit-uv-firewall.service` finishes the job before the network comes up:

| The record says | At the next boot |
|---|---|
| the apply was **confirmed** | the table comes back, `ssh_from` and `default_deny` included, no timer |
| the apply was still **waiting**, its five minutes not yet spent | the table comes back and the timer guards what is left of the window |
| the apply was **never confirmed** and the window is gone | nothing |
| you ran `firewall off` | nothing |

`termit-uv firewall restore --dry-run` answers what a reboot would do without rebooting.

## The ban engine

With `security.guard.enabled: true` — or `termit-uv init --harden` — `init` installs and starts
`termit-uv-guard.service`. It watches the kernel's scan set and the web server's log and bans the sources
it finds:

| Signal | First ban | Repeat |
|---|---|---|
| port scan | 1 hour | 24 hours |
| failed requests (4xx from one source) | 5 minutes | doubling to 1 hour |
| `termit-uv bans block <ip>` | permanent | until you lift it |

A repeat only escalates inside `security.guard.ladder_window`, measured **from the end of the previous
ban**: coming back an hour later earns the longer ban, coming back next week starts from the bottom.
When the ladder has nothing harder to offer, the source is marked an *offender* — nothing automatic bans
it forever, and `termit-uv bans offenders` is where you decide.

The guard never applies or removes the table. It only fills and empties sets, so it is safe to leave
running: with no table applied it reads what it can and does nothing.

```bash
termit-uv guard status              # on or off, and what it last decided
termit-uv bans list                 # what is banned now, and why
termit-uv bans offenders            # sources the ladder ran out of rungs for
termit-uv bans block 203.0.113.7    # permanent, your decision
termit-uv bans unban 203.0.113.7    # lift the ban, keep the offence on record
termit-uv bans unblock 203.0.113.7  # lift a block you made
```

Decisions are in `/var/lib/termit/uv/bans.json` and survive a reboot; the kernel's sets do not, and the
guard refills them on its next pass. `unban` is not amnesia: the next offence earns the longer ban.

**Never banned, whichever signal fires:** loopback, the private and link-local ranges, the management
server (resolved from `mn.url`, cached for ten minutes) and your own `ssh_from` networks. `bans block`
refuses them too — banning those cuts you off. A wrong key answers `404`, which is a 4xx and therefore
counts: if a client of yours bans itself, `bans unban` is the answer.

## The blocklist

A list of networks you never want to hear from — a hosting provider, a scanner's range — comes in over a
URL and is refreshed on a timer:

```yaml
security:
  firewall:
    blocklist_url: https://example.net/termit/blocklist.txt
    blocklist_refresh: 24h
```

`http(s)` and `file` URLs only. One network per line, `#` starts a comment, a bare address means its
`/32` or `/128`; the file is normalised and sorted, so reordering a source does not change it, and the
body is capped at 4 MiB.

```bash
termit-uv firewall refresh                          # fetch, validate, put in force
termit-uv firewall refresh --dry-run                # fetch and validate only
termit-uv firewall refresh --url file:///root/list.txt   # try a list before committing to it
```

The list in force is `/var/lib/termit/uv/blocklist.txt`, and the kernel's sets are rebuilt from that file
rather than from the network — which is what repairs a reboot. A download that fails, a body that does
not parse and a list with no entries are all refused, leaving the list in force alone: an empty blocklist
silently unblocks everything, which is worse than an outdated one.

## On the management server

The server carries the same table under its own name, `inet termit_mn`, publishing 22, 80 and 443 — port
80 stays open for every node's certificate renewal, not only while the server's own order is in flight.
The commands are the same with a different binary: `termit-mn firewall`, `termit-mn bans`,
`termit-mn guard`.

Two differences: the server has no web access log to count 4xx answers (its admin API is rate-limited
instead, and the guard counts scans only), and its units are plain files in `deploy/systemd` rather than
generated ones. That is why `termit-mn firewall on` refuses until the rollback timer is installed: an
apply with no way back is worse than a refused command. `docs/OPERATIONS.md` §18.6 has the install lines.

## What it does not do

- It does not rate-limit proxied traffic. Your clients are not throttled.
- It does not touch routing, NAT or WARP. Only inbound connections are judged.
- It does not invent a table. Lose `firewall.json` and the configuration's wish is not enough — `firewall
  on` is the way back; the boot unit only restores what was applied.
- It does not hide the node from a targeted observer. It refuses scans and probes; that is a different
  claim, and [`THREAT_MODEL.md`](../THREAT_MODEL.md) is precise about it.

Next: [Using a node](using-uv.md) · [Running the server](using-mn.md) ·
[When something goes wrong](troubleshooting.md)
