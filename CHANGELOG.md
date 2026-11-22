What each release changed, written for the people who run Termit. Newest first;
`0.1.1` was prepared but never released — its work is part of `0.2.0`. The GitHub release for a version
carries every section back to the last release: the versions in between were handed out only as
pre-releases, and whoever installs the release skipped them.

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