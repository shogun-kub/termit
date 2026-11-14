# Changelog

What each release changed, written for the people who run Termit. Newest first;
`0.1.1` was prepared but never released — its work is part of `0.2.0`.

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
