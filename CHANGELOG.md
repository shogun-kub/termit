What each release changed, written for the people who run Termit. Newest first;
`0.1.1` was prepared but never released — its work is part of `0.2.0`. The GitHub release for a version
carries every section back to the last release: the versions in between were handed out only as
pre-releases, and whoever installs the release skipped them.

## Unreleased

### Added

- **A node can be moved to another management server.** `termit-mn home handoff --uv <id or domain> --to
  <peer>` moves one node inside the mesh: the target must be within `mesh.lag_max` journal entries of the old
  home (or `--force` says you accept that a node enrolled since its last pull has to re-enroll), it takes the
  row over with a new agent key and a `home_term + 1` that orders every later merge, and the command prints
  the key once. The old home records the same home and term without journaling — the change is announced by
  the new home's journal. An event that still claims an older term is skipped with the cursor advancing, so a
  server returning from a partition cannot undo a handoff it missed. Schema 9 adds `home_term`.

- **A node whose home server is down keeps working through a replica.** A second management server in the
  mesh answers such a node with the last desired state it holds, marked `read_only` in the response and in
  `termit-uv sync`'s output, and keeps the node's report. Its puller posts that report to the home, which
  applies it exactly as if the node had synced there — and ignores a report older than what it already
  knows, so a queue from an outage cannot rewind a row. The report is refused for a node the receiver does
  not home. Schema 8 adds `mesh_report`.

- **A second management server can hold a copy of the registry.** `mesh:` in `config.yaml` names the peers;
  each pulls the other's journal over HTTPS (a snapshot first, the journal after, a snapshot again across a
  pruned gap) and applies it in one transaction, so a node enrolled on one server is in the other's
  subscriptions within `pull_every`. The credential is an HMAC of the master key, derived rather than
  stored, and the two replication endpoints are read-only. `termit-mn status` shows each peer, how far
  behind it is, and whether this server holds the key. A replica refuses any write to a row another server
  homes, and its scheduler no longer probes those rows. Not yet: a node syncing to a replica while its home
  is down, `home handoff`, and the quorum gate on a credential epoch — see
  [`docs/OPERATIONS.md`](docs/OPERATIONS.md) §3.4 and [`docs/MN-API.md`](docs/MN-API.md) §3.5.

- **The menu reaches two commands it could not before.** `termit-mn` gains "check for an update"
  (`update --check`): asking whether a newer release exists no longer means agreeing to install it.
  `termit-uv` gains "run one agent cycle" (`agent --once`): a self-check and a sync without waiting for
  the timer. Every other command the help lists was already reachable; the exceptions that remain (the
  commands a systemd unit runs, `version`, and the menu itself) are listed with their reasons in
  `docs/guide/menu.md`, and a test fails when a documented command is in neither place.

- **The server backs its registry up before it updates.** `termit-mn update --apply` writes a `termit-mn
  backup` archive before it replaces the binary, prints the path, and refuses to go on if it cannot. A
  schema migration moves the database forward and the previous binary then refuses a file it did not
  write, so the archive — not `termit-mn.prev` — is what a failed upgrade rolls back to. `mn.sh` does the
  same on an upgrade.

- **The server can declare the oldest node build it supports.** `min_agent_version: v0.7.0` in the
  server's configuration travels in every sync answer; a node below it prints one line in `termit-uv
  status` and a warning in the `doctor` report, and keeps serving. Nothing is refused: an old build is a
  node to upgrade, not a node to drop, and a refusal keyed on the server's word can strand a healthy box.
  The value is read at startup, like the enrollment rule, and a server that names no floor sends nothing.

- **The WARP tunnel can restart on a schedule.** `warp.restart_every: 6h` writes `RuntimeMaxSec=` into
  the unit that carries the tunnel — the core's on `wireguard`, where a restart re-establishes the
  WireGuard handshake and every proxied connection reconnects; the MASQUE sidecar's on `usque`. A tunnel
  that has run for hours ages: the first request after an idle period can take seconds, and a fresh
  process bounds the damage. Off by default; refused on `proxy`.

### Fixed

- **The WARP health probe speaks the tunnel's family.** It fetched a dual-stack name, which resolves to
  IPv6 on most hosts; a tunnel that carries only IPv4 could not carry it, the probe failed, and the
  balancer read that as "WARP is down" and sent every proxied flow direct — the node looked up while
  revealing its own address. The probe now fetches the family's address literal (`1.1.1.1` or its IPv6
  twin), and `warp status` and `doctor` fetch the same one.

## 0.7.0 — 2026-11-27

### Added

- **`termit-uv upgrade` takes the release the server names.** Without `--to` the target is the version the
  management server reported at the last sync — not `latest`, and never a pre-release: a node follows its
  server. The binary is checked against the release's `SHA256SUMS`, written beside the running one, swapped
  in with the previous file kept as `termit-uv.prev`, and the new binary converges the node; a converge
  that fails puts the previous binary back. `--check` says what a run would do, `--release-base` points at a
  mirror, and the menu offers the entry only when there is something to install.

- **`termit-mn update` does the same for the server.** `--check` (the default) follows the
  `releases/latest` redirect and says whether this build is behind; `--apply` downloads, verifies, swaps,
  restarts the unit and restores the previous binary if the restart fails. Neither the check nor its
  result appears in `status`: a status call must not reach GitHub.

- **Both menus are a tree.** Eight groups on the node (State, Network, Clients, Mesh, Defence, Site, Node,
  Help) and seven on the server, at most ten choices per screen, the group named in the title and `0` to go
  back up. The four tables that were already menus (WARP, devices, firewall, bans) and the node verbs became
  real levels, so every command is two keystrokes away and nothing is buried.

- **The server's menu picks a node instead of asking for its id.** The mesh is drawn as a list; a number, a
  domain, an id or any unique part of a domain answers the question, two matches ask again rather than
  guess, `0` cancels, and when the server cannot be reached the question falls back to a typed domain.

- **The wizard asks about Hysteria2.** One yes/no between egress and hardening: the answer is remembered in
  the draft, reaches `init` as `--hysteria`, and is repeated in the summary with the port it opens. The
  default stays TLS-only.

- **`termit-mn invite create` prints what to forward.** The wizard command, the server's URL and the code,
  and the note as the person's name: the other side answers two questions and assembles nothing by hand.

- **`termit-mn invite policy` changes the enrollment rule.** `invite`, `invite+approval` or `open`, written
  by editing one value in the YAML so the operator's comments survive, loaded again before the file is
  replaced, and followed by the sentence that matters: the running server keeps the old rule until it
  restarts. `open` needs `--force`; the menu asks a second time.

- **`termit-uv doctor` reports three groups and a verdict.** Host, node and tunnel, each with its own tally,
  and a closing line that says problems found, ok with warnings or ok. The JSON carries the group too.

- **The menu header is the node's card.** Besides domain and fingerprints it names the transports the node
  serves, where proxied traffic leaves, the server's build (marked when this node is behind it), how much of
  the mesh it sees with the degraded count, and when it last synced. All of it local: a probe on every
  keystroke is not a header.

- **The client page says what its links are for.** The header carries the node's egress line, and a link
  from the second transport is marked `(QUIC)` with the note that it is the one to pick for voice and video.

- **A certificate is not requested for a name that does not point at this host.** The `dns` check compares
  the A record with this machine's addresses and now fails instead of warning, so `init` stops before it
  spends a Let's Encrypt attempt the CA can never complete, and `renew-cert` refuses for the same reason —
  five failed authorizations block the name for an hour. A node behind a proxy or a NAT port-forward
  declares that once with `acme.allow_remote_ip: true` and is told, in the warning, that reaching the node
  through that address is now its promise. `termit-uv doctor` reports the same check. An answer that comes
  from the host's own `/etc/hosts` (`127.0.1.1`) counts as this machine, so a node whose host name is its
  domain is not refused its own name.

- **The tunnel is measured, not just named.** `termit-uv warp status` prints how long the trace through the
  tunnel took, and `doctor` says the same and warns when it takes more than two seconds: an exit that answers
  in seconds is unusable to every client even while the egress check is green.

- **`termit-mn uv show` says which transport the probe took.** A node that serves the TLS profile and
  Hysteria2 is probed on one leg; the view lists both declared transports and marks the one that was tested,
  so "ok" can no longer stand for a transport nobody checked.

- **`doctor` compares the table in force with the configuration.** A node whose applied table was rendered
  before a UDP port was added, or before `ssh_from` was narrowed, gets a warning naming the fields and the
  command that fixes it, instead of a clean report.

### Changed

- **The page of client links exists without being asked for.** `init`, `sync`, the agent's cycle and a
  device change mint its random address the first time and keep it afterwards, instead of waiting for
  `termit-uv options`: a node that never ran that command had links but no page, and `status` had no
  `options:` line to print. `termit-uv options --rotate` still moves the address. The mesh heading counts
  machines, not rows: a peer that serves a second transport is one node, and which transports it serves is
  its owner's choice.

- **Three wizard questions say what the answer is for.** The domain asks for the whole name — subdomain
  and domain together; the contact address says it is the mailbox that receives expiry notices; the ACME
  directory says to just press Enter when the answer is not known.

- **An empty answer to a question that names something already in existence cancels.** A node, an invite
  code: the prompt says so, and the action runs nothing. Questions that set a new value still ask again.

### Fixed

- **The page of client links is readable by nginx.** A page minted by a process with a restrictive
  umask left its directory `0700`, and nginx answered `403` to the owner's own address — which is what
  the lab found on the first node that minted one itself. The directory is now set public explicitly,
  whatever umask the writer runs under, and a test holds it there.

- **The egress probe waits for the MASQUE relay.** `init --warp-transport usque` used to probe while the
  relay was still starting, go out through the balancer's fallback and report the node's own address as the
  tunnel's exit. It now waits for the port and, if it never comes up, says it could not verify instead.

- **`init --hysteria` prints the firewall hint only when the table really lacks UDP/443.** It used to say
  the table was rendered without the port even when it was in it.

- **`warp.family` is documented as a `wireguard` setting.** The MASQUE sidecar is dual-stack and takes no
  family flag, so on `usque` a non-default family is refused at load time and the default means nothing;
  the operations guide, the config comment and both guides say so.

- **The firewall table and the configuration no longer drift apart unseen.** `firewall status` names the
  fields that moved — `ports`, `udp_ports`, `ssh_from`, `default_deny` — and prints the applied UDP ports;
  `firewall restore`, the boot unit, writes the same warning to the journal when the record it restores is
  older than the configuration; and `init` prints the QUIC hint on every run, not only when `--hysteria` was
  passed. The table is still applied only by `firewall on`: a host firewall that re-renders itself at boot
  is a change of exposure nobody confirmed.

- **`termit-uv sync` records the server's build.** The command reconciled the peers, the subscription
  and the probe schedule but left `server_version` where the agent's last cycle put it, so `upgrade` and
  the menu's update offer named the old release for up to a day after the server was upgraded. The
  production pass found it; the command now writes the field the agent writes.

## 0.6.0 — 2026-11-22

### Added

- **The MASQUE sidecar: `warp.transport: usque`.** The third way to reach WARP is a pinned MASQUE client
  (`usque` 4.2.1, digest-checked) that runs as its own unit, user and state, and exposes a loopback SOCKS5
  listener that carries **UDP as well as TCP**. The core dials it through `termit-usque-relay.service`, a
  loopback relay: usque refuses a UDP datagram that does not come from the port of the connection that asked
  for the association, and the core's SOCKS client does not reuse it. The core's document sends both
  protocols to the WARP hop, so the proxy transport's split — UDP to `direct`, because warp-cli has no UDP
  association — does not apply; `warp status` says which transport carried the flow and whether UDP went
  through the tunnel, and `warp rotate` registers a new device.

- **The node learns which build its server runs.** The sync answer carries `server_version`, the node
  remembers it beside the last sync and `termit-uv status` prints it as `server:`. Nothing follows from it yet
  — the menu offer and the update command are `0.7.0` — but the field is what they will read, and it costs one
  line in the answer. An older server sends nothing and an older node ignores it, so the mesh can mix builds.

### Fixed

- **The page shows a peer's label as words, not as escapes.** The label travels in the URI fragment, where a
  space is `%20`, and the page printed it verbatim: `then5.bookinet.ru%20(QUIC)`. The fragment is decoded for
  display now — which also restores the `(degraded)` marker, because that suffix arrives encoded too, so a
  degraded peer was not being marked on the page at all.

## 0.5.2 — 2026-11-22