What each release changed, written for the people who run Termit. Newest first;
`0.1.1` was prepared but never released — its work is part of `0.2.0`. The GitHub release for a version
carries every section back to the last release: the versions in between were handed out only as
pre-releases, and whoever installs the release skipped them.

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

### Changed

- **An empty answer to a question that names something already in existence cancels.** A node, an invite
  code: the prompt says so, and the action runs nothing. Questions that set a new value still ask again.

### Fixed

- **The egress probe waits for the MASQUE relay.** `init --warp-transport usque` used to probe while the
  relay was still starting, go out through the balancer's fallback and report the node's own address as the
  tunnel's exit. It now waits for the port and, if it never comes up, says it could not verify instead.

- **`init --hysteria` prints the firewall hint only when the table really lacks UDP/443.** It used to say
  the table was rendered without the port even when it was in it.

- **`warp.family` is documented as a `wireguard` setting.** The MASQUE sidecar is dual-stack and takes no
  family flag, so on `usque` a non-default family is refused at load time and the default means nothing;
  the operations guide, the config comment and both guides say so.

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