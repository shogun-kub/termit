# The interactive menu

[English](menu.md) · [Русский](ru/menu.md)

Both binaries have a menu. It exists because the commands are numerous and the flags are not
memorable: `uv renew-cert --force`, `mn uv block <node> --reason <text>`. On a terminal you can pick
from a list instead, and the menu asks the questions the flags would answer.

The important part of the design is what the menu is *not*: it is not a second implementation of
anything. Every entry builds a command line and runs the same command a script would run, in the same
process, through the same code. So a menu entry and its flags cannot drift apart, and anything the
menu does you can also do — and quote in a bug report — as a command.

## Opening it

```bash
uv                  # on a terminal: the menu
mn                  # on a terminal: the menu
uv menu             # explicitly, even when output is redirected
mn menu
```

With no arguments and a terminal on both stdin and stdout, the bare binary opens the menu. Anywhere
else — in a script, in a cron job, with stdout piped — it stays what it always was: the usage text and
exit status 2. That is deliberate. A command that turns into a prompt when someone mistypes it inside
a provisioning script is a hang, not a convenience.

```
┌──────────────────────────────────────────────────────────────────────┐
│  Termit — node                                                       │
│  termit-uv v0.1.0 (commit 8f8cb7c, built 2026-11-09, api 1)          │
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
│  choose [1-13, q]:                                                   │
└──────────────────────────────────────────────────────────────────────┘
```

Answer with the number or with the label — `3` and `install or converge` are the same answer, because
the label is what you are looking at. An empty line takes the default shown in brackets, `q` quits, and
an answer that matches nothing re-asks instead of exiting: a menu that dies on a typo is a menu nobody
trusts. Closed stdin (a pipe that ran out) ends the session cleanly with status 0.

## Node menu (`termit-uv`)

| Item | What it does | The command it runs |
|---|---|---|
| status | domain, identity fingerprints, certificate, peers, last and next sync, WARP | `uv status` |
| doctor | host and node diagnostics; changes nothing | `uv doctor` |
| install or converge | first run asks for domain, email and WARP; later runs only re-check | `uv init …` |
| join the mesh | asks for the server URL and an invite code | `uv enroll …` |
| sync now | fetch the peer set now instead of waiting for the daily sync | `uv sync` |
| check drift | report whether the running core matches the server | `uv sync --check` |
| devices | list, add or remove this node's own devices | `uv device …` |
| print vless URI | the URI of this node's own device | `uv uri` |
| egress (WARP) | show, or switch between `all`, `off` and `selected` | `uv warp …` |
| certificate | renew now; optionally force a reissue | `uv renew-cert [--force]` |
| decoy site | regenerate the site, optionally with another archetype | `uv renew-site …` |
| uninstall | asks twice, names what `--purge` takes | `uv uninstall [--purge]` |
| help | every command and flag | `uv help` |

## Server menu (`termit-mn`)

| Item | What it does | The command it runs |
|---|---|---|
| status | server, database, prober, node table | `mn status` |
| nodes | list, show, approve, reject, block, unblock, remove | `mn uv …` |
| invites | create, list, revoke codes | `mn invite …` |
| check now | the whole mesh, or one node | `mn check --all` / `mn check <node>` |
| policy | show thresholds, or change one | `mn policy …` |
| subscription | rotate a node's subscription token | `mn sub rotate <node>` |
| backup | write a consistent snapshot | `mn backup` |
| help | every command and flag | `mn help` |

The server menu is a front end for the admin CLI, so it needs what the admin CLI needs: a running
server and the admin token. The header says whether the token is in the environment; if it is not, the
admin actions refuse to run and the CLI says so in its own words.

Starting the server is deliberately absent. `mn serve` belongs to the systemd unit, where its log is
collected; a menu that starts a long-running foreground server would hide that log behind a menu
screen.

## Colour

Colour is used when stdout is a terminal. `NO_COLOR=1` (any value) or `TERM=dumb` turns it off, and
redirected output is never coloured — a saved report reads as plain text. With colour off the screen is
not cleared either, so a captured session reads as a sequence of sections instead of one page written
over itself.

## When not to use it

- **Unattended installs.** `uv.sh`, `uv init` and `mn init` take flags for a reason: a prompt in an
  install path cannot be answered by an automated run, and a hang halfway through a provisioned node is
  worse than an error. The menu is for a human at a terminal who would rather not recall a flag.
- **Scripts and audits.** Every menu action prints the command line it runs, precisely so it can be
  copied into a script or a ticket. Use the command, not the menu.
- **Anything over a flaky connection.** The menu holds no state between screens, so a dropped SSH
  session loses nothing — but a long action (install, certificate renewal) is better run under
  `systemd-run` or `tmux` where the output survives.
