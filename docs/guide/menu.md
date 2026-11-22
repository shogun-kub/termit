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
│    serves        TLS/443 only                                        │
│    egress        all via wireguard                                   │
│    server        v0.7.0                                              │
│    peers         3 (1 degraded)                                      │
│    sync          2h ago                                              │
│    ✔ installed                                                       │
│                                                                      │
│    1   State                what this node is, and whether it is…   │
│    2   Network              counters, WARP, the second transport     │
│    …                                                                 │
│    8   Help                 every command and flag                   │
│    q   quit                                                          │
│                                                                      │
│  choose [1-8, q]:                                                    │
└──────────────────────────────────────────────────────────────────────┘
```

Answer with the number or with the label — `1` and `State` are the same answer, because
the label is what you are looking at. An empty line takes the default shown in brackets, `q` quits, and
an answer that matches nothing re-asks instead of exiting: a menu that dies on a typo is a menu nobody
trusts. The questions a step asks follow the same rule: a value the command cannot work without is asked
again when it is left empty, and a yes/no answer that is neither is asked again too, so a typo never
answers for you. Closed stdin (a pipe that ran out) ends the session cleanly with status 0.

The server's menu asks which node an action is about by showing the mesh: a domain is what you remember,
an id is not. Answer with the number, with a domain, with an id, or with any unique part of a domain —
`then5` finds `then5.bookinet.ru`. A part that matches several nodes asks again with the list, `0`
cancels, and when the server cannot be reached the question says so and takes the domain you type. A
question that names something already in existence — a node, an invite code — treats an empty answer as
cancel and says so in its label; a question that sets a new value (a domain being installed) still asks
again.

Both menus are one screen of at most ten choices per level: a group opens its own screen, the title
repeats which group it is, and `0` goes back up. Nothing to remember: the number you see is the number you
type.

The header above the choices is the node's card: domain, identity fingerprints, the transports it serves,
where proxied traffic leaves, the server's build (marked when this node is behind it), how much of the mesh
it sees and when it last synced. It is drawn from local state on every screen, so it costs nothing; the
`State → status` screen has the rest.

## Node menu (`termit-uv`)

Eight choices, each a group that opens its own screen; `0` goes back up. Every leaf prints the command it
runs.

**State** — *what this node is, and whether it is healthy*

- `status` — domain, identity fingerprints, certificate, peers, last and next sync, WARP
- `doctor` — host and node diagnostics; changes nothing

**Network** — *counters, WARP, the second transport*

- `net` — sample the TCP counters: is the node dropping, or the path?
- `warp` — show the egress, switch between `all`, `off` and `selected`, or rotate the device
- `transport` — turn Hysteria2 on UDP/443 on or off; the answer defaults to what the node already does

**Clients** — *links, the client page, devices*

- `options` — the private page with every client link, and its address
- `uri` — the URI of this node's own device
- `devices` — list, add or remove this node's own devices

**Mesh** — *sync, enrollment, lifting a block*

- `sync` — fetch the peer set now instead of waiting for the daily sync
- `sync --check` — report whether the running core matches the server
- `enroll` — join a mesh: the server URL and an invite code
- `unblock` — ask the server to verify this node and let it back in

**Defence** — *firewall, bans, the guard*

- `firewall` — the host table: status, on, off, confirm, refresh, restore
- `bans` — what the guard banned, and your own permanent blocks
- `guard status` — whether the ban engine runs, and what it banned

**Site** — *certificate and the masking site*

- `renew-cert` — renew now; optionally force a reissue
- `renew-site` — regenerate the site, optionally with another archetype

**Node** — *install, relocate, uninstall*

- `init` — install or converge; the first run asks for domain, email and WARP
- `wizard` — the whole first installation as questions; remembers the answers between runs
- `migrate --relocate` — move the node to another domain (Scenario C)
- `uninstall` — asks twice, names what `--purge` takes

**Help** — every command and flag.

## Server menu (`termit-mn`)

Seven choices; `0` goes back up.

**State** — *server, check, thresholds, the guard*

- `status` — server, database, prober, node table
- `check now --all` / `check now <node>` — the whole mesh, or one node
- `policy` — show thresholds, or change one
- `guard status` — whether the ban engine runs, and what it banned

**Nodes** — *the mesh, one verb at a time*

- `uv list` — every node with its domain, state and last contact
- `uv show`, `uv approve`, `uv reject`, `uv block`, `uv unblock`, `uv rm`, `uv reissue`, `uv agentkey`,
  `uv forget` — each asks which node, from a list of the mesh rather than an id, and the two that break
  something (`reissue`, `forget`) confirm first

**Invites** — *codes for new nodes*

- `invite list`, `invite create` (with a note), `invite revoke`
- `invite policy` — the rule a new node meets: a code, a code and an approval, or nothing at all; a change
  takes effect when the server restarts, and the command says so

**Subscription** — *rotate a node's subscription token*

- `sub rotate` — asks which node and, optionally, the server's public name

**Defence** — *firewall and bans*

- `firewall` — the host table: status, on, off, confirm, refresh, restore
- `bans` — what the guard banned, and your own permanent blocks

**Service** — *token, backups, restore*

- `admin rotate` — replace the admin token you are using
- `backup` — write a consistent snapshot
- `restore` — put the database and certificates back; asks for the archive and says the server must be stopped

**Help** — every command and flag.
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
