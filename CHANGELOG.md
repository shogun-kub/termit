# Changelog

What each release changed, written for the people who run Termit. Newest first;
`0.1.1` was prepared but never released — its work is part of `0.2.0`.

## 0.3.2 — unreleased

The owner's page catches up with what the node knows, and starts listing the devices it was already
holding.

### Added

- **The page lists local devices.** A phone added with `termit-uv device add` now appears on the page
  with its link, so the owner copies it from the browser instead of scrolling a terminal. Adding or
  removing a device re-renders the page.

### Fixed

- **The agent's daily cycle left the page describing yesterday's mesh.** `termit-uv sync` refreshed the
  page and the cycle that runs on its own did not, so a peer added or removed on the server reached the
  core and not the page until an operator ran something. The cycle now renders the page after it applies
  the peer set, and a failing render stays a warning: the proxy is already correct.

## 0.3.1 — 2026-11-15

One fix, for the half of the boot unit that only a real reboot could find: an apply that was still
waiting.

### Fixed

- **The boot unit could not restore an apply that was still inside its five-minute window.** Restoring
  that one arms the rollback timer, and arming writes the timer's units — which the unit's
  `ProtectSystem=strict` sandbox made read-only. On a reboot in that window the unit failed with
  `read-only file system` and the table did not come back. A confirmed apply was never affected: it arms
  nothing. Both boot units now allow writes to the unit directory, and the lab check covers the pending
  path instead of only the confirmed one.

## 0.3.0 — 2026-11-15

The release that makes Termit pleasant to install and able to defend its own host: a wizard that asks
instead of eleven flags, an owner surface in Russian or English, client links that carry what was measured
about frozen handshakes — and an opt-in firewall, ban engine and blocklist for both the node and the
management server, which no install and no timer ever switches on by itself.

### Added

- **`termit-uv wizard`** — the first installation as questions instead of flags: language, domain, contact
  address, masking site, egress, hardening, invite code. Each answer is kept in `wizard.yaml` beside the
  configuration, so an interrupted run continues where it stopped and Enter accepts what is there
  (`--fresh` forgets it). It is a front end, not a second installer: it runs the same `init` and `enroll`
  with the same flags, so an operator who prefers flags can do exactly what it does. A node that is
  installed but behind this build gets named as such instead of being asked for a domain it already has.
- **A page with every link** in the decoy site's tree — every local device, every mesh peer and both
  subscription forms, each with a copy button. The path is a capability like a subscription token:
  generated once, kept in the state file, replaced by `--rotate`, and printed by `status`. Re-rendering
  never creates a page that was never asked for, and with the server unreachable the page says so and
  shows the local device.
- **Russian and English.** `locale` in the configuration, `init --locale`, Russian by default; the wizard
  asks first because everything after it is written in the language it picks. The console switches only
  where the environment is UTF-8, because on a C or latin-1 terminal every Cyrillic byte is a mojibake;
  the page of client links keeps the chosen language either way. `termit-mn menu` speaks the same two.
- **Fragmentation, where a client can read it.** `subscription.fragment` and
  `subscription.record_fragment` for the sing-box profile, and `subscription.happ_fragment` /
  `happ_fragment` for the plain link list — the `fragment=` parameter Happ reads from the URI itself. A
  fresh server writes `record_fragment: true` and `100-200,1-3,tlshello,6`, because on the paths this
  project is deployed against the client's plain ClientHello is what pays: Happ's own ping against these
  nodes went from 769 ms to 446–464 ms. Node links carry the node's own value, so a page of links no
  longer mixes two answers to the same question.
- **The host firewall** (`termit-uv firewall status|on|off|confirm|rollback|refresh|restore`). One
  nftables table, rendered from a typed model, applied with `nft -f -` in one transaction: SSH and the
  component's own ports, anything else inbound dropped (`--default-deny=false` to allow it), `--ssh-from`
  to pin SSH to networks. Four golden rulesets, all accepted by nftables 1.1.3. Nothing else in Termit
  applies or removes it: not `init`, not `sync`, not the agent, not a timer.
- **The way back, twice.** `on` arms a five-minute rollback timer *before* it touches the kernel; without
  `confirm` the table removes itself, so a mistake made over the connection it cut costs five minutes.
  `off` restores the previous `nft list ruleset` byte for byte.
- **A ban engine** (`termit-uv guard`, `termit-uv bans list|offenders|block|unban|unblock`). Two signals:
  the kernel's own meter for scans (5 attempts in a minute, remembered for 15) and the web server's 4xx
  answers (10 in five minutes). A scan earns an hour and a repeat inside the ladder window a day; failed
  requests earn five minutes and double to an hour. The window is measured from the end of the previous
  ban, because a window as long as the ban would make every repeat look like a first offence. When the
  ladder runs out of rungs the source is listed as an offender instead of being blocked forever.
- **A blocklist** (`security.firewall.blocklist_url`, `termit-uv firewall refresh`, a daily timer). One
  URL, http(s) or `file`, a 4 MiB cap and `If-Modified-Since`; parsed, normalised, sorted and written
  atomically, then loaded into the kernel's sets in one batch. A source that is broken, empty or
  unreachable leaves the list in force alone — an empty blocklist unblocks everything and looks like
  success.
- **BitTorrent blocking** (`security.bittorrent_block`, off by default): the core sniffs the first bytes
  of every connection and blackholes the handshakes, with `routeOnly: true` so the client's destination
  survives. The spike proved the pinned core classifies the handshake.
- **The same table on the management server** (`termit-mn firewall|bans|guard`), as `inet termit_mn` with
  ports 22, 80 and 443 — 80 stays open for every node's renewal, not only its own. The commands are one
  implementation shared by both binaries, so they cannot drift. Its units are plain files rather than
  rendered by the binary, so the release now ships them beside `termit-mn`.
- **`init --harden`** and the wizard's unchecked "harden this node" question: they write the configuration
  and install the ban engine, and stop there. The table itself is still `firewall on`.
- **The table comes back after a reboot.** `termit-uv-firewall.service` (and the server's copy) runs
  before `network-pre.target` and restores what the record says was applied: a confirmed table comes back
  with no timer, an apply still inside its five-minute window comes back with the timer armed, and a table
  that was never confirmed — or that the operator took down — stays gone.

### Changed

- **`status` says what an operator needs between the steps**: a `bans:` line, and a `firewall:` line that
  appears only when the configuration asks for a table or one was applied once, and reads both the record
  and the kernel.
- **`uninstall` removes the table** before it removes the binaries, because a default-deny table with no
  command left to remove it is a host nobody can repair without a console.
- **The design contract changed, deliberately.** `docs/DESCRIPTION.md` §11 invariant 1 used to forbid host
  firewall changes outright; it now says the table exists only after `firewall on`, the guard and the
  blocklist refresh only fill its sets, and the boot unit only puts back a confirmed apply.
- **A new `security:` block** in both configurations, off by default, with the thresholds, the ladder, the
  blocklist and `ssh_from`; `default_deny` is a pointer so an absent key can be told apart from an
  explicit `false`.
- **`termit-uv init --harden=false`** undoes the configuration the flag wrote.

### Fixed

- **`bans block` could cut a node off from its management server.** The never-ban set was built from
  `ssh_from` alone, so the server's address could be blocked by hand even though the guard refuses to ban
  it automatically. Both now use one set.
- **A scan set the guard could not read.** With the agent's `RestrictAddressFamilies` copied over, every
  cycle failed with `Unable to initialize Netlink socket` — nft talks to the kernel through netlink. The
  guard, the blocklist and the boot units allow `AF_NETLINK`, and say why.
- **`bans unban` used to be undone by the meter.** The kernel's scan set remembers for minutes, so the
  next cycle re-banned the source; `unban` clears the scan element too.
- **The menu's language no longer depends on the machine that runs the tests** — a UTF-8 console used to
  turn a fixed-language expectation into a Russian menu and a red CI job.
- **The disk-full drill tops up instead of assuming it can fill the disk**, or says why it cannot.

### Compatibility

- API version 1 and profile v1 are unchanged: a 0.2.1 server and a 0.3.0 node interoperate, and so do the
  reverse. New configuration keys default to off, and a node that never runs `firewall on` behaves exactly
  as it did.
- **Run `termit-uv init` once after upgrading** (and `termit-mn init`, or install the server's units from
  `deploy/systemd`): that is what writes `termit-uv-firewall.service`, the guard and blocklist units. It
  converges — no credential, path or certificate is regenerated.
- **On the management server, `firewall on` refuses until the rollback timer is installed.** Its units are
  plain files in `deploy/systemd`, not rendered by the binary. An apply with no way back is worse than a
  refused command.
- **The boot unit appears for every node**, and with no record it does nothing: a node whose operator never
  ran `firewall on` is unchanged after a reboot.

## 0.2.1 — 2026-11-14

The release that lets a node live behind a proxy and tells an operator when the path, not the node, is
to blame: the server learns the client address from a PROXY header, the client profile gets the settings
that were measured on real networks, and the node can now say which side drops the packets.

### Added

- **`termit-uv net`** — samples the kernel's TCP counters and separates "this node dropped the
  connection" from "the answer never reached the peer". It reads `/proc` only: no sockets, no host
  contacted. `--seconds` sets the sample, `--json` prints the report, and a sample that saw no traffic
  reports `skip` instead of claiming `ok`.
- **`trusted_proxies` on the server, with the PROXY protocol.** When the peer address is in the list the
  server takes the client address from the header and ignores it otherwise, so the list is the whole
  security argument. This is what lets the server run behind a TLS-passthrough proxy: enrollment keeps
  its DNS proof and the rate limits keep their per-client key.
- **The client matrix** in `docs/OPERATIONS.md` §8.1: the profile is XHTTP, so Xray-based clients and
  OpenWrt's `podkop-engine` work, stock sing-box does not, and `unknown version: 72` means "wrong
  client", not "broken node".
- **A diagnosis recipe for a lossy path** in `docs/OPERATIONS.md` §16.1, which `termit-uv net` automates.
- **`go run ./deploy/clean`** — drops the release artifacts and the scratch directory, with the caches
  split by what losing them costs.

### Changed

- **Client URIs carry `fp=firefox` and an XHTTP `extra` block** (`xPaddingBytes` and `xmux`). Measured,
  not assumed: padding makes request sizes vary, and connection reuse collapsed 20 proxied connections
  into one TCP connection where the previous profile used 5–8. No `alpn` is offered any more — the
  transport is HTTP/1.1 on the wire through nginx.
- **The sing-box profile (`/json`)** sends a uTLS fingerprint and no longer asks for `h2`.
- **The decoy site's responses** carry `Vary: Accept-Encoding`, cache assets for 24 hours, and answer a
  request on the proxy path without a valid credential with the site's own 404 page instead of Xray's
  `400` with a header that named the transport.
- **`termit-uv doctor`** names each listening port once (`:80 and :443`, not `:80 and :80 and :443 and
  :443`).
- **The sync response example** in `docs/DESCRIPTION.md` now matches what the API returns.

### Fixed

- **Enrollment behind a TLS-passthrough proxy** saw the proxy's address and refused the node with
  `dns_mismatch`. With `trusted_proxies` set, the node's own address is used: the DNS proof passes, the
  rate limits are per client, and `termit-mn uv show` prints the real IP.
- **The toolchain pin moves to Go 1.27.4.** Go 1.27.3 carries `GO-2026-6859`, a race in how `net/http`
  answers a request rejected by `MaxBytesReader`; every HTTP server in this tree reaches it. Reaching the
  fix is a patch-level toolchain change, so no code of ours moved — but the published binaries are built
  with it, and `govulncheck` gates on the toolchain that built them.
- **Debian 12 installs.** apt 2.6 cannot read the ASCII-armored Cloudflare key that `signed-by` points at
  and answers `NO_PUBKEY`; nginx 1.22 has no `http2` directive and refuses the vhost outright. Both are
  handled now — the key is converted to the binary form, and the vhost takes whichever dialect the
  host's nginx speaks. The preflight still reports Debian 12 as older than the supported release.

### Compatibility

- API version 1 and profile v1 are unchanged: a 0.2.0 server and a 0.2.1 node interoperate, and so do the
  reverse. An existing URI keeps working — it simply has no padding and no connection reuse.
- A server behind a proxy must list the proxy in `trusted_proxies`; without it the header is ignored,
  exactly as before. A trusted peer that sends no header keeps its own address, so the server can be
  upgraded before the proxy is reconfigured.

## 0.2.0 — 2026-11-10

The release that makes the management server harder to find and the interactive menus usable: an API
under a path of its own, a masking site of its own, a token you can rotate without editing units.

### Added

- **Configurable API prefix.** `termit-mn init` randomises `api_prefix`, prints it, and `termit-mn status`
  shows it on the `api:` line. The prefix is part of `mn.url`, so every node enrolled with the printed
  URL reaches the API at once; `--api-prefix` sets one by hand. `/sub/<token>` and `/metrics` keep their
  paths — clients and scrapers do not have to learn the prefix.
- **A masking site on the management server.** The same generator the nodes use (`internal/decoy`), served
  from memory, selected with `decoy.template` (default `portal`), seeded once and stored in the database.
  Unknown paths get the site's own 404, so a probe sees a plausible site rather than a bare server.
- **Admin token rotation.** `termit-mn admin rotate` mints a new token, prints it once, or writes it to a
  `0600` file with `--token-out`. Rotating replaces the token the caller authenticated with, so it is never
  a way to take over someone else's access. The old token stops working immediately — every timer and
  script that carries it has to be updated.
- **Admin token from a file.** `--token-file <path>` on every admin command and on the server menu;
  `TERMIT_MN_ADMIN_TOKEN_FILE` does the same from the environment. A token file is one secret to protect
  instead of a variable in every shell.
- **Interactive menus.** `termit-uv menu` and `termit-mn menu`, and the bare binaries on a terminal. Each
  item runs the command line a script would run, on a shared front end (`internal/termui`) with prompts,
  confirmations and colour.
- **`docs/MN-API.md`** — the server's API reference: paths and the prefix, request and response shapes,
  problem codes, the masking site, `/metrics`, and the admin socket's operations.

### Changed

- **`uv.sh --mn` now reaches `init` as well as `enroll`.** The URL is written to `mn.url`, which the agent
  reads on every cycle; before, the documented install enrolled a node that then could not sync.
  `mn.url` is validated too: an absolute `http(s)` URL with a host, no query, no fragment.
- **`termit-mn uv list` reports the agent's real build.** The agent version it showed was the API version
  (`1`); it is now `termit-uv v0.2.0 (<commit>)`, and the same string identifies the node at enrollment.
- **`termit-mn status`** names the API prefix and the masking site, and prints `(runtime override)` only
  when a value really differs from the configuration file.
- **The management server's `/`** answers with the masking site instead of a 404. A configuration written
  by `0.1.0` has no `decoy.template` and gets `portal`.
- The binaries name themselves on the command line (`termit-uv …`, `termit-mn …`) in usage, help and
  errors, instead of the old short names.

### Fixed

- **A stamped release could not enroll.** The agent sent its full banner as `agent_version` — 70+ bytes
  against a protocol limit of 64 — so every `enroll` from a real release failed with
  `agent_version is longer than 64 characters`. It is now `<component> <version> (<commit8>)`, capped at
  the limit. The lab only ever ran unstamped `dev` builds, which fit.
- **Flags after a positional argument were not parsed** by `termit-mn uv show <node> --token-file …` and
  `invite revoke <id> --token-file …`: both reported that the node argument was missing.
- **The menus dropped their own flags.** `termit-mn menu --token-file …` warned that no admin token was
  configured while holding one, and then every admin action refused to run; `termit-uv menu --config …`
  inspected and acted on the default node. Both menus now parse their flags, hand them to every command
  they run, and refuse at startup when the flags contradict each other.
- A captured menu session no longer leaves a half-written pause line in the transcript.

### Build, test, release

- CI runs on `dev` and `release/**` as well as `main`; nothing is published from `dev` or `main`.
- A manual pre-release door: `workflow_dispatch` on a `release/<version>` branch builds the same six
  artifacts and may publish them as `pre-v<version>-<run>`.
- `AGENTS.md` now records the branch-and-release process.
- The lab suite leaves the server under systemd after a drill and survives one slow probe; the
  subscription fetch trusts the CA the node trusts, whether that is `mn.ca_file` or the system store.

### Upgrading from 0.1.0

- **No re-enrolment.** A `0.1.0` configuration has no `api_prefix`, so the server keeps serving
  `/api/v1` — the path the deployed nodes already use. `init --api-prefix` refuses to move a prefix that
  is installed; changing it means re-enrolling every node.
- **Restart the server, then let the nodes sync.** They report their build on the next cycle; nothing has
  to be run by hand. `termit-mn uv list` starts showing real versions after that.
- **`/` starts serving the masking site** when the server comes back. Set `decoy.template` before the
  restart if you want a different archetype.
- Everything else — the prober, ACME, the certificate store, the database schema — is unchanged; the
  schema is still version 5.
