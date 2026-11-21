# The interactive menu

[English](menu.md) · [Русский](ru/menu.md)

Both binaries have a menu. It exists because the commands are numerous and the flags are not
memorable: `termit-uv renew-cert --force`, `termit-mn uv block <node> --reason <text>`. On a terminal you can pick
from a list instead, and the menu asks the questions the flags would answer.

The important part of the design is what the menu is *not*: it is not a second implementation of
anything. Every entry builds a command line and runs the same command a script would run, in the same
process, through the same code. So a menu entry and its flags cannot drift apart, and anything the
menu does you can also do — and quote in a bug report — as a command.

## Opening it

```bash
termit-uv                  # on a terminal: the menu
termit-mn                  # on a terminal: the menu
termit-uv menu             # explicitly, even when output is redirected
termit-mn menu
```

With no arguments and a terminal on both stdin and stdout, the bare binary opens the menu. Anywhere
else — in a script, in a cron job, with stdout piped — it stays what it always was: the usage text and
exit status 2. That is deliberate. A command that turns into a prompt when someone mistypes it inside
a provisioning script is a hang, not a convenience.

```
┌──────────────────────────────────────────────────────────────────────┐
│  Termit — node                                                       │
│  termit-uv v0.4.1 (commit …, built …, api 1)                         │
│                                                                      │
│    domain        node-a.example.com                                  │
│    identity      3f9c1a2b                                            │
│    peer path     e9438a65                                            │
│    ✔ installed                                                       │
│                                                                      │
│    1   status               identity, certificate, peers, sync, WARP │
│    2   doctor               check the host and the node, change…     │
│    …                                                                 │
│    q   quit                                                          │
│                                                                      │
│  choose [1-21, q]:                                                   │
└──────────────────────────────────────────────────────────────────────┘
```

Answer with the number or with the label — `3` and `install or converge` are the same answer, because
the label is what you are looking at. An empty line takes the default shown in brackets, `q` quits, and
an answer that matches nothing re-asks instead of exiting: a menu that dies on a typo is a menu nobody
trusts. The questions a step asks follow the same rule: a value the command cannot work without is asked
again when it is left empty, and a yes/no answer that is neither is asked again too, so a typo never
answers for you. Closed stdin (a pipe that ran out) ends the session cleanly with status 0.

## Node menu (`termit-uv`)

| Item | What it does | The command it runs |
|---|---|---|
| status | domain, identity fingerprints, certificate, peers, last and next sync, WARP | `termit-uv status` |
| doctor | host and node diagnostics; changes nothing | `termit-uv doctor` |
| connection diagnostics | samples the TCP counters: is the node dropping, or the path? | `termit-uv net` |
| install or converge | first run asks for domain, email and WARP; later runs only re-check | `termit-uv init …` |
| setup wizard | the whole first installation as questions; remembers the answers between runs | `termit-uv wizard` |
| client options | the private page with every client link, and its address | `termit-uv options` |
| join the mesh | asks for the server URL and an invite code | `termit-uv enroll …` |
| lift a block | asks the server to verify this node and let it back in | `termit-uv unblock` |
| move to another domain | relocates the node to a new domain (Scenario C) | `termit-uv migrate --relocate …` |
| sync now | fetch the peer set now instead of waiting for the daily sync | `termit-uv sync` |
| check drift | report whether the running core matches the server | `termit-uv sync --check` |
| devices | list, add or remove this node's own devices | `termit-uv device …` |
| print vless URI | the URI of this node's own device | `termit-uv uri` |
| egress (WARP) | show, switch between `all`, `off` and `selected`, or rotate the device | `termit-uv warp …` |
| QUIC transport | turn the second transport on or off; the answer defaults to what the node already does | `termit-uv init --hysteria` / `--no-hysteria` |
| firewall | the host table: status, on, off, confirm, refresh, restore | `termit-uv firewall …` |
| bans | what the guard banned, and your own permanent blocks | `termit-uv bans …` |
| guard | whether the ban engine runs, and what it banned | `termit-uv guard status` |
| certificate | renew now; optionally force a reissue | `termit-uv renew-cert [--force]` |
| decoy site | regenerate the site, optionally with another archetype | `termit-uv renew-site …` |
| uninstall | asks twice, names what `--purge` takes | `termit-uv uninstall [--purge]` |
| help | every command and flag | `termit-uv help` |

## Server menu (`termit-mn`)

| Item | What it does | The command it runs |
|---|---|---|
| status | server, database, prober, node table | `termit-mn status` |
| nodes | list, show, approve, reject, block, unblock, remove, reissue, agent key, forget | `termit-mn uv …` |
| invites | create, list, revoke codes | `termit-mn invite …` |
| check now | the whole mesh, or one node | `termit-mn check now --all` / `termit-mn check now <node>` |
| policy | show thresholds, or change one | `termit-mn policy …` |
| subscription | rotate a node's subscription token | `termit-mn sub rotate <node>` |
| admin token | replace the admin token you are using | `termit-mn admin rotate` |
| backup | write a consistent snapshot | `termit-mn backup` |
| restore from a snapshot | put the database and certificates back; asks for the archive and says the server must be stopped | `termit-mn restore --from … --force` |
| firewall | the host table: status, on, off, confirm, refresh, restore | `termit-mn firewall …` |
| bans | what the guard banned, and your own permanent blocks | `termit-mn bans …` |
| guard | whether the ban engine runs, and what it banned | `termit-mn guard status` |
| help | every command and flag | `termit-mn help` |

The server menu is a front end for the admin CLI, so it needs what the admin CLI needs: a running
server and the admin token. Give the token to the menu itself and every admin command it runs inherits it:

```bash
termit-mn menu --token-file /etc/termit/mn/admin.token
```

The header names the credential the session runs on (`admin token from --token-file …`, from
`TERMIT_MN_ADMIN_TOKEN`, or from `TERMIT_MN_ADMIN_TOKEN_FILE`), says plainly when there is none, and the
admin actions then refuse to run in the CLI's own words. `--config` and `--socket` work the same way.
The local commands — `backup`, `restore`, `firewall`, `bans`, `guard` — read the host rather than the
API, take none of those flags, and the menu leaves them out of what it passes.

The node menu takes `--config` for the same reason: `termit-uv menu --config /path/to/config.yaml`
inspects and acts on that node, not on the default one.

Starting the server is deliberately absent. `termit-mn serve` belongs to the systemd unit, where its log is
collected; a menu that starts a long-running foreground server would hide that log behind a menu
screen. The same reasoning keeps four other commands out: `termit-uv agent` and `guard run`, which are
loops the units own; `firewall rollback`, which is the rollback timer's own command and says so in its
help; and `version`, which is in the header of every screen. `termit-mn init` stays out too — the menu
talks to a running server, and there is no server yet when that command is the one you need. Everything
else the binary accepts has a menu entry, and `help` prints the full list — and since every entry runs
the command line it prints, an entry cannot do anything the CLI cannot.

## Colour

Colour is used when stdout is a terminal. `NO_COLOR=1` (any value) or `TERM=dumb` turns it off, and
redirected output is never coloured — a saved report reads as plain text. With colour off the screen is
not cleared either, so a captured session reads as a sequence of sections instead of one page written
over itself.

## When not to use it

- **Unattended installs.** `uv.sh`, `termit-uv init` and `termit-mn init` take flags for a reason: a prompt in an
  install path cannot be answered by an automated run, and a hang halfway through a provisioned node is
  worse than an error. The menu is for a human at a terminal who would rather not recall a flag.
- **Scripts and audits.** Every menu action prints the command line it runs, precisely so it can be
  copied into a script or a ticket. Use the command, not the menu.
- **Anything over a flaky connection.** The menu holds no state between screens, so a dropped SSH
  session loses nothing — but a long action (install, certificate renewal) is better run under
  `systemd-run` or `tmux` where the output survives.
