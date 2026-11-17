# Termit operations

How Termit is put together and operated: what runs where, installation, the daily cycle, egress,
certificates, clients, monitoring. The design is in [`DESCRIPTION.md`](DESCRIPTION.md), the wire contract in
[`PROTOCOL.md`](PROTOCOL.md), the adversaries in [`THREAT_MODEL.md`](THREAT_MODEL.md).

Procedures done under pressure — upgrading, backup and restore, recovery, uninstall, the triage table — are
in [RUNBOOK.md](RUNBOOK.md). This document is the reference: what runs where, how it is set up, and why it
behaves the way it does.

If you are installing Termit for the first time, start with the guide instead — it is written for the
person who owns one node: [how Termit works](guide/overview.md), [a node](guide/using-uv.md), [the
server](guide/using-mn.md), [troubleshooting](guide/troubleshooting.md). The same pages exist in
Russian under [`guide/ru/`](guide/ru/overview.md). This document goes further, and assumes you know
what a TLS certificate and an ACME challenge are.

Conventions used below:

- Run everything as `root` (or with `sudo`); nothing in this document works as an unprivileged user.
- `mn.example.com` is the management server, `node1.example.net` a node. Replace them with your names.
- The binaries are `termit-mn` and `termit-uv`; both accept `-v` for info-level logging.
- Exit codes are the same in both binaries: `0` success, `1` failure, `2` usage error. `termit-uv sync
  --check` adds `4` for "the node differs from the server".

---

## 1. What runs where

| Host | Unit | Listens on | Owns |
|---|---|---|---|
| MN | `termit-mn.service` | `:443` (TLS), `:80` while an ACME order is in flight | SQLite registry, master key, its own certificate, admin socket, probe worker |
| UV | nginx (distribution unit) | `:80`, `:443` | TLS, the decoy site, the ACME webroot, the one proxy location |
| UV | `termit-xray.service` | `127.0.0.1:8443`, API on `127.0.0.1:10085` | the proxy core |
| UV | `termit-uv-agent.service` | outbound only | the daily cycle: self-check, sync, core reconciliation |
| UV | `termit-uv-renew.timer` | — | the daily certificate check |
| UV | `termit-uv-guard.service` | — | the ban engine, when `security.guard.enabled` (§13.4) |
| UV | `termit-uv-blocklist.timer` | — | the network blocklist refresh, when a URL is configured (§13.5) |
| UV | `termit-uv-firewall.service` | — | puts a confirmed table back at boot (§13.7) |
| UV | the tunnel: a WireGuard outbound inside the core, or `warp-cli` on `warp.transport: proxy` | `127.0.0.1:40000` in proxy mode only | where proxied traffic leaves, when WARP is on |
| MN | `termit-mn-guard.service`, `termit-mn-blocklist.timer`, `termit-mn-firewall.service` | — | the same three, shipped with the release (§13.6) |

The table itself is applied by `firewall on` and never by a timer; the boot unit only puts back what was
applied and confirmed, and does nothing when there is no record (§13.7).

Termit never touches host routing, and the one firewall it can create is a single opt-in table that exists
only after `termit-uv firewall on` — so every other service on the box (SSH included) keeps working through
every code path. That is a contract, not a default.

### 1.1 Paths

| Path | What |
|---|---|
| `/etc/termit/mn/config.yaml`, `/etc/termit/mn/master.key` | server configuration and the master key (`0600`) |
| `/var/lib/termit/mn/termit.db`, `…/certs/` | registry, ledger, the server's own certificate |
| `/run/termit/mn/admin.sock` | the admin socket (`0600`, directory `0750`) |
| `/etc/termit/uv/config.yaml` | node configuration (public, `0644`: it holds no secret) |
| `/var/lib/termit/uv/state.json` | node identity, seeds, credentials, schedule (`0600`) |
| `/var/lib/termit/uv/certs/` | the node's `fullchain.pem` (`0644`) and `privkey.pem` (`0600`) |
| `/var/lib/termit/uv/install-journal.json` | the last install run, step by step |
| `/var/www/termit/site/`, `/var/www/termit/acme/` | the decoy tree and the ACME webroot (nginx reads them) |
| `/usr/lib/termit/bin/xray` | the pinned proxy core on a node |
| `/etc/nginx/sites-available/termit.conf` | the generated vhost |
| `/etc/systemd/system/termit-*.service`, `…/termit-uv-renew.timer` | the generated units |

Every file Termit writes is `0600` (secrets), `0644` (public) or a `0750` directory. Configuration
paths are configurable, but there is no reason to move them; an operator who does gets to keep both
tree and layout consistent.

## 2. Sizing and prerequisites

The reference deployment is one cheap VPS per node and one small server for the management plane, and
the implementation is built to that budget:

| Host | vCPU | RAM | Disk | Notes |
|---|---|---|---|---|
| UV | 1 | 1 GB | 10 GB | Debian 13 (trixie), amd64 or arm64 |
| MN | 2 | 2 GB | 10 GB | single binary + SQLite + probe worker |

Per host, before you start:

- **Debian 13** and systemd. Preflight refuses anything older than Debian 12, warns on 12 (the target
  is 13, trixie) and refuses a host without systemd.
- **A name that resolves to this machine.** Both sides validate it: the MN checks that the node's
  domain resolves to the address the enrollment request came from, and both use HTTP-01, so `:80`
  must be reachable from the internet for the first issuance. Point the A/AAAA record at the box
  before installing, not after.
- **Ports 80 and 443 free.** On a node, nginx takes them; if something already serves them, `termit-uv init`
  stops at preflight and says which listener is in the way. On the MN they must be free too.
- **A synchronised clock.** ACME proof and the TLS handshake both fail on a drifting clock; preflight
  warns when `timedatectl` says the clock is not synchronised.
- **`/dev/net/tun` for the `proxy` transport.** An unprivileged LXC container usually has none.
  That is not a failed install: the node records the refusal, keeps the mode you asked for, and sends
  traffic directly (§6). The default `wireguard` transport runs inside the core and needs no device.
- **Outbound access** to the ACME directory, to the node's management server, to `github.com` for the
  pinned Xray release (or your mirror), and — when WARP is on — to Cloudflare's package repository and
  WARP endpoint. Nothing else.

The MN's probe worker needs an `xray-core` binary of its own — `prober.xray_binary` in
`/etc/termit/mn/config.yaml` — because that is what opens a real connection to a node. It is not
optional: without it `termit-mn status` says `prober: unavailable`, and enrollment refuses with
`check_failed: the prober could not check this node; retry`. §3 has the commands, and it belongs after
`termit-mn init` because it edits the configuration `init` writes. A node's own installation gets that
binary as a step; the server does not have that step.

## 3. Install the management server

```bash
install -m 0755 termit-mn /usr/bin/termit-mn

termit-mn init --domain mn.example.com \
        --email you@example.com \
        --mode invite          # invite | invite+approval (default) | open
```

`termit-mn init` creates the configuration, the database, the master key, the first admin token and the first
invite code, and prints them **once**:

```
domain:       mn.example.com
listen:       :443
api prefix:   /6rfjdqfn5nno
masking site: portal (what a prober gets for every path the API does not claim)
config:       /etc/termit/mn/config.yaml
database:     /var/lib/termit/mn/termit.db (schema 5)
master key:   /etc/termit/mn/master.key (generated now)
master key id: <fingerprint>

admin token (shown once, store it now):
  <token>

first invite code (shown once, 1 use(s)):
  ABCD-EFGH-IJKL

show a node the way in (the prefix is part of the URL; without it the node cannot reach the API):
  termit-uv enroll --mn https://mn.example.com/6rfjdqfn5nno --invite <code>

back up /etc/termit/mn/master.key together with the database: without it no pair credential can be derived.
```

Store both values now. The admin token is needed for every `termit-mn` command; put it in the environment
rather than in a shell history, or in a file for the jobs that have no shell:

```bash
export TERMIT_MN_ADMIN_TOKEN='<token>'
```

A timer, a cron job or a configuration-management task has no shell to export from, so the CLI can read
the token from a file instead. It reads only what it is told to read — there is no default path, because a
stale file in a well-known place would authenticate silently:

```bash
install -m 0600 /dev/null /etc/termit/mn/admin.token
printf '%s\n' '<token>' > /etc/termit/mn/admin.token
termit-mn status --token-file /etc/termit/mn/admin.token
export TERMIT_MN_ADMIN_TOKEN_FILE=/etc/termit/mn/admin.token

# what a backup timer looks like
termit-mn backup --out /var/backups/termit --token-file /etc/termit/mn/admin.token
```

Precedence, highest first: `--token`, `--token-file`, `TERMIT_MN_ADMIN_TOKEN`,
`TERMIT_MN_ADMIN_TOKEN_FILE`. An explicit flag beats the environment, and passing both `--token` and
`--token-file` is refused rather than guessed. The file must be a regular file, mode `0600` or narrower,
owned by the user running the command, and at most 4 KiB; anything else is refused with the fix in the
message, because a world-readable token file is a leaked credential that looks like a tidy setup.

`termit-mn init --admin-token-out FILE` writes the token to a file instead of printing it. That is how a
provisioned server keeps its only copy out of an operator's scrollback; if the write fails, `init` prints
the token and exits non-zero rather than leaving a server with a credential nobody has seen.

Then install the unit and start it:

```bash
install -m 0644 deploy/systemd/termit-mn.service /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now termit-mn
journalctl -u termit-mn -n 30 --no-pager
```

The first start **issues the certificate before it serves TLS**; it takes as long as the ACME order
does and refuses to start if it fails. That is deliberate: a management API that silently falls back
to HTTP is worse than one that does not come up. Watch for:

```
level=INFO msg="issued a certificate" domain=mn.example.com not_after=…
level=INFO msg="serving TLS" domain=mn.example.com listen=:443 not_after=…
level=INFO msg="admin socket ready" path=/run/termit/mn/admin.sock
```

Give it the probe worker now (§2 says why it is not optional) — the same pinned core the nodes run:

```bash
apt-get install -y unzip
curl -fsSLO https://github.com/XTLS/Xray-core/releases/download/v26.11.9/Xray-linux-64.zip
unzip -o Xray-linux-64.zip -d /usr/local/lib/termit
chmod 0755 /usr/local/lib/termit/xray
sed -i 's|^  xray_binary: ""|  xray_binary: /usr/local/lib/termit/xray|' /etc/termit/mn/config.yaml
systemctl restart termit-mn
```

Verify:

```bash
curl -sS https://mn.example.com/6rfjdqfn5nno/healthz | jq .   # {prefix}/healthz, see §3.1
termit-mn status
```

`termit-mn status` is the one screen to open first:

```
server:   mn.example.com
listen:   :443
api:      /6rfjdqfn5nno
decoy:    portal
version:  termit-mn v0.4.1 (commit …, built …, api 1, profile v1)
uptime:   3h12m (started 2026-11-10 11:18)
database: /var/lib/termit/mn/termit.db (412.0 KiB, schema 5)
prober:   ready (/usr/local/lib/termit/xray)
queue:    0 due, next 2026-11-11 12:34 UTC
cert:     2027-02-08T12:06:44Z (in 89 days)
nodes:    3 (3 active)
check:    every 24h, times below are UTC

DOMAIN           STATE   FAILED  LAST CHECK        NEXT CHECK        LAST SEEN
node1.example.net  active  0     2026-11-10 13:29  2026-11-11 12:34  2026-11-10 13:31
```

The `prober` and `queue` lines are the two that catch a mesh that looks fine and is not: `unavailable`
means no check can produce a verdict, and a growing `due` count means the scheduler is not keeping up.
Neither is a node failure, and neither changes a node's state.

### 3.1 The API prefix

`api_prefix` is the path the node API is served under. `init` picks a random one on a fresh install
(`--api-prefix` overrides it), so the server answers `404` to `/api/v1/enroll`, `/healthz` and every
other name a scanner tries, while DPI sees one more HTTPS host that serves nothing recognisable. The
prefix is an identifier, not a credential: it is in every node's configuration and in `termit-mn status`.

```yaml
api_prefix: /6rfjdqfn5nno
```

A configuration written before this option existed has no `api_prefix` and keeps serving the historical
`/api/v1`, which is what makes an upgrade a no-op for the nodes.

**Changing it on a running server is a migration, not a setting.** The prefix is half of every node's
`mn.url`, and a node that is not told about a change cannot reach the API at all. In order:

1. Edit `api_prefix` in `/etc/termit/mn/config.yaml`, then `systemctl restart termit-mn`.
2. On **every** node: update `mn.url` under `mn:` in `/etc/termit/uv/config.yaml` to the new URL, then
   `systemctl restart termit-uv-agent`.
3. `termit-uv sync --check` on each node, and `termit-mn uv list` on the server, until every node is
   `active` again.

Nodes already enrolled keep working across the change as soon as step 2 lands: no credential is derived
from the prefix. Do it with the mesh in front of you, not on a Friday.

### 3.2 The masking site

The server answers a visitor with an ordinary-looking page, the same generator the nodes use, so a
prober that finds `mn.example.com` sees a website instead of a machine answering `404 page not found`:

```yaml
decoy:
  template: portal      # portal or product
```

```bash
curl -sS https://mn.example.com/ | head -3         # the landing page
curl -sS -o /dev/null -w '%{http_code}\n' https://mn.example.com/nothing   # 404, the site's own page
```

It is generated at startup from the domain, a seed stored in the database and the template, and held in
memory: no directory, no files to keep in step. The seed is minted once by `termit-mn init` (or by the
first start after an upgrade) and never changes, so the site is stable across restarts. To change the
archetype, edit `decoy.template` and restart — that is the whole procedure. `termit-mn status` prints
the template on the `decoy:` line.

The site never shadows the API: the prefix, `/sub/…` and `/metrics` are matched first.

### 3.3 Behind a reverse proxy

The server terminates TLS itself, so a proxy in front of it must pass the connection through rather than
terminate it: the certificate, the ACME renewal and the masking site stay the server's. nginx does that
with the stream module, routing on the SNI before any TLS is parsed (`libnginx-mod-stream`), and it tells
the server who the client was in a PROXY protocol header:

```nginx
# /etc/nginx/stream.d/mn.conf, included from a stream{} block in nginx.conf
map $ssl_preread_server_name $mn_backend {
    mn.example.com       10.0.0.5:443;
    default              127.0.0.1:8443;   # the sites nginx terminates, moved off :443
}

server {
    listen              443;
    proxy_pass          $mn_backend;
    proxy_protocol      on;                # the server reads this from trusted_proxies
    ssl_preread         on;
}
```

and the server lists the proxy, using the address the proxy connects from:

```yaml
trusted_proxies: [10.0.0.5]
```

`trusted_proxies` is what makes that header believable: it is read only from an address on the list, so a
client cannot claim an address by sending one itself. Without it the server ignores forwarded protocols
on purpose — the enrollment DNS proof ("the domain must resolve to the address the request came from"),
the rate-limit key and the recorded last address are all made of the peer address. A node behind a
terminating proxy that the server does not trust fails enrollment with `dns_mismatch` while being
perfectly healthy: it is the proxy's address that does not resolve to the node.

Two things that are easy to get wrong, and both are silent:

- **Port 80 still has to reach the proxy.** The server renews over ACME HTTP-01, so the proxy forwards
  `/.well-known/acme-challenge/` for that name to the server's challenge listener
  (`acme.challenge_listen`, `:80` by default) and may redirect everything else:

  ```nginx
  server {
      listen 80;
      server_name mn.example.com;
      location /.well-known/acme-challenge/ { proxy_pass http://10.0.0.5:80; }
      location / { return 301 https://$host$request_uri; }
  }
  ```

- **`$remote_addr` needs the realip module.** `proxy_protocol` in a `listen` parses the header only into
  `$proxy_protocol_addr`; `$remote_addr` changes only with `set_real_ip_from 127.0.0.1; real_ip_header
  proxy_protocol;` in `http{}`. Without it every site behind the hop logs the loopback address and
  forwards it upstream.

A trusted address that sends no header keeps its own address, so the server can be upgraded before the
proxy is told to send one, and a proxy that is trusted for one name only still serves the rest.

## 4. Install a node

The bootstrap script verifies a release checksum, installs the binary and then lets `termit-uv init` do the
work. It never guesses a version and never installs an unverified binary — and it is a file you keep,
not something piped from a branch:

```bash
termit-mn invite create --note "node1" --uses 1        # on the MN; prints the code once

# on the fresh node, as root
curl -fsSLO https://github.com/shogun-kub/termit/releases/download/v0.4.1/uv.sh
sh uv.sh --version v0.4.1 \
  --domain node1.example.net --email you@example.com \
  --mn https://mn.example.com/6rfjdqfn5nno --invite ABCD-EFGH-IJKL
```

`deploy/install/uv.sh` is that script. Flags worth knowing:

| Flag | Effect |
|---|---|
| `--version` | the release to install; **required** — the installer never picks one for you |
| `--base-url` | release directory: an `https://` URL, a `file://` URL or a local path (a mirror, or a USB stick) |
| `--checksum-file` / `--sha256` | verify against a local `SHA256SUMS` or an explicit digest, for a private build |
| `--no-warp` / `--warp selected --warp-selected …` | the WARP decision (`all` is the default) |
| `--mn URL` | the management server, prefix included; written to `mn.url` by `init` and used by `enroll` |
| `--no-enroll` | install and converge, skip enrollment |
| `--wizard` | install the binary, then run `termit-uv wizard` instead of converging from the flags |
| `--dry-run` | verify the download, then run `termit-uv init --dry-run` without installing anything |

Re-running the script with a new `--version` **is** the upgrade: the binary is replaced and `termit-uv init`
converges the rest, keeping the identity, the Xray path and the certificate. The invite code is passed
on the command line and is visible in `ps` for the duration of the run; revoke it afterwards if that
matters (`termit-mn invite revoke`).

If you would rather be asked than remember flags, the wizard is the same installation with questions —
`sh uv.sh --version v0.4.1 --wizard` installs the binary and hands over to it:

```bash
termit-uv wizard
```

The first question is the language (`ru` or `en`, Russian by default) because the menu and the page of
client links are written in it; then the domain, the contact address, the masking site, the egress mode,
whether to harden the node (unchecked — see below), the invite code and the management server URL. It then
runs the same `init` with the same flags. Answers are kept in `wizard.yaml` beside the configuration, so an
interrupted run continues where it stopped and `Enter` accepts what is already there (`--fresh` forgets
them). On an installed node it asks nothing: it runs the pipeline in dry-run mode and, when every step
reports its effect present, prints the status and exits. An installed node behind this build is the third
case — the wizard names it and tells the operator to run `termit-uv init`, because converging is init's job
and doing it from a questionnaire would hide the upgrade. The last thing a fresh install prints is the
address of the [page with every link](#80-one-page-with-every-link).

Without the script, the two commands it wraps are:

```bash
install -m 0755 termit-uv /usr/bin/termit-uv
termit-uv init --domain node1.example.net --email you@example.com \
  --mn https://mn.example.com/6rfjdqfn5nno
termit-uv enroll --invite ABCD-EFGH-IJKL
```

`termit-uv init` runs one pipeline of steps, in this order, and every step is idempotent:

| Step | What it does |
|---|---|
| `preflight` | root, Debian version, systemd, architecture, clock, free ports, DNS, ACME reachability, `/dev/net/tun`, disk, memory, `apt` |
| `user` | creates the unprivileged `termit` user |
| `dirs` | `/etc/termit/uv`, `/var/lib/termit/uv`, `/var/www/termit` |
| `packages` | installs `nginx`, `ca-certificates`, `curl` |
| `warp` | registers the node's WARP identity for the in-core tunnel; with `--warp-transport proxy` installs and connects `cloudflare-warp` instead (skipped when WARP is off) |
| `xray-binary` | downloads the pinned release, verifies its SHA-256, installs it |
| `identity` | generates the node's UUID, XHTTP path and decoy seed **once** |
| `decoy` | generates the site |
| `xray-config` | writes the core config, checks it with `xray -test` |
| `web` | writes the vhost, checks it with `nginx -t` |
| `units` | writes and enables the three units and the timer |
| `certificate` | issues the certificate over HTTP-01 through the webroot |
| `start` | starts the core, nginx and the agent, and verifies they stay up |
| `warp-egress` | fetches the Cloudflare trace through the node's own tunnel and says where it came out; a warning, never a failure |

Useful flags: `--dry-run` (report, change nothing), `--only <steps>` / `--skip <steps>`,
`--no-warp` (opt out of WARP), `--warp selected --warp-selected a.example,b.example`,
`--xray-download-base` (a mirror, a `file://` URL or a local path), `--acme-ca` (a non-production
directory, for a lab), `--harden` (turn on the host firewall and the ban engine in the configuration).

`--harden` configures; it does not apply. It sets `security.firewall.enabled` and
`security.guard.enabled`, which installs the guard's unit and makes `status` say what is left to do, and
the table is still your `termit-uv firewall on` — the one command that undoes a mistake five minutes
later (§13.2). The wizard asks the same question, unchecked, and passes the same flag; no install puts a
table on a host while nobody is watching. To undo the configuration: `termit-uv init --harden=false`.

Preflight is the place to read when something is wrong; it prints one line per check with a hint:

```
  root      ok    running as root
  ports     ok    :80 and :443 are free
  dns       ok    node1.example.net → 203.0.113.7
  tun       ok    /dev/net/tun is missing, which the wireguard transport does not need
  disk      ok    8.6 GB free on /var/lib/termit/uv
```

Then enroll:

```bash
termit-mn invite create --note "node1" --uses 1     # on the MN, prints the code
termit-uv enroll --invite ABCD-EFGH-IJKL           # on the node
```

`termit-uv enroll` registers the node, stores the agent key and the subscription token in the state file
(`0600`, never printed again) and runs the first sync. In `invite+approval` mode the answer is
`state: pending`, and the node joins the mesh only after:

```bash
termit-mn uv approve node1.example.net
```

Check both ends:

```bash
termit-uv status            # identity, certificate, peers, warp line, last install steps
termit-uv doctor            # host checks plus state, services, certificate, decoy, core config, warp
termit-mn uv show node1.example.net   # state, last probe, last self-check, state history
```

## 5. The daily cycle, and how to watch it

The agent owns the node's clock. It wakes at the server-given `next_sync_after` plus a stable
per-node jitter (ten minutes at most), and runs:

1. a **local self-check** — the proxy port, the API port, the certificate window, WARP;
2. a **sync** — report the self-check and the current leaf fingerprint, receive the peer set;
3. **reconciliation** — the core's client set is updated, hot (Xray API) first, with a validated
   restart as fallback and rollback on failure.

A failed cycle does not retry every minute: the agent keeps the schedule it has, and the server's
probe decides the node's state. To run one cycle by hand:

```bash
termit-uv agent --once
termit-uv sync --check      # report drift, change nothing, exit 4 when the node differs
termit-uv sync              # reconcile now
```

What to watch:

```bash
systemctl status termit-uv-agent termit-xray nginx termit-uv-renew.timer
journalctl -u termit-uv-agent -u termit-xray -n 50 --no-pager
termit-uv status
termit-mn status                    # server, prober, queue, certificate, nodes
termit-mn uv show node1.example.net # state history and the last ten checks
termit-mn check now node1.example.net   # one cycle, on demand
```

`termit-mn uv show` ends with a check series, newest first, with both kinds in it:

```
checks (newest first):
  2026-11-10 14:30  probe  ok
  2026-11-10 13:32  probe  ok
  2026-11-10 11:17  self   ok
```

A probe that fails while the node's own `self` row passes is a path problem between the server and the
node; the two rows are why both kinds are kept.

Logs are structured and go to stderr, which means the journal. Identifiers are logged; credentials,
keys and UUIDs are not.

## 6. Egress: WARP

WARP is **on by default** (`warp.mode: all`). The reason is in the design: an application on a client's
device asks "what address am I coming from?", and a node that answers with its own address is a node
that can be found and blocked. Proxied traffic leaves through Cloudflare's network instead; the node's
own traffic (SSH, the decoy, ACME, the agent) never does.

```bash
termit-uv warp status             # mode, the transport's own state, and where a real fetch came out
termit-uv warp status --json
termit-uv warp mode off           # opt out: traffic exits from the node's address
termit-uv warp mode all           # the default
termit-uv warp mode selected --selected example.com,api.example.org
```

`termit-uv warp mode` rewrites the route, re-runs `xray -test`, restarts the core if it is running and
restarts the agent, because a running agent holds the configuration it read at start. `termit-uv status`
shows the result:

```
warp:         all, on (via 104.28.1.1 (AMS))
```

Values you may see for the state: `on` (traffic exits through WARP), `direct` (WARP is down and
fail-open sent traffic out from the node's address), `blocked` (`warp.fail_open: false`: traffic is
being refused rather than exposed), `off`, `not-installed` (there is no identity: the node was installed
with `--no-warp`), `unavailable` (the host cannot run the chosen transport at all — on `proxy`, no
`/dev/net/tun` or no package for the architecture) and `unknown` (the self-check could not tell).
`not-installed` and `unavailable` are states, not errors: `termit-uv warp status` reports them and
exits 0.

Fail-open is a routing decision inside the core, not a configuration rewrite: an `observatory` probes
WARP every 30 s through the tunnel, and a `balancer` with `fallbackTag: direct` sends new connections
direct while WARP does not answer. Recovery is equally automatic. With `fail_open: false` the route
names the WARP outbound directly, so the node fails closed instead — the right choice for an owner who
would rather lose connectivity than expose the address.

### 6.1 The transport: WireGuard inside the core, or the proxy

`warp.transport` decides where the tunnel lives. `wireguard` is a WireGuard outbound inside the core —
one process, no package, no loopback port — and it carries UDP as well as TCP. `proxy` is the older
path, still opt-in: `warp-cli` in proxy mode, a SOCKS5 listener carrying TCP only.

```bash
termit-uv init --warp-transport wireguard   # registers the node, writes the identity, applies it
termit-uv warp status                       # mode, transport, and where traffic really leaves
termit-uv warp rotate                       # a new WARP identity, deliberately
```

A configuration written before the key existed reads as `proxy`, and nothing migrates it silently: the
next `init --warp-transport wireguard` registers the node and applies the new route. Switching back is
the same flag with `proxy`, which reinstalls `cloudflare-warp` and restores the split route. The identity
is `/var/lib/termit/uv/warp.yaml`, `0600`, written once — a re-run of `init` reuses it, and `uninstall
--purge` removes it with the rest of the state.

**The tunnel carries one address family.** `warp.family` is `ipv4` (the default) or `ipv6`: the document
keeps the tunnel address of that family and gives the peer that family's default route, and destinations
of the other family are routed `direct` — the way they travelled before WARP existed. IPv4 is the default
because Cloudflare's IPv6 egress is what large services tend to mark as unsafe, and because a provider's
own IPv6 is not something the node can fix.

```bash
termit-uv init --warp-family ipv6   # exit through Cloudflare's IPv6 instead
termit-uv warp status               # the address Cloudflare sees has the family in use
```

**Registration is a warning, not a gate.** Cloudflare's API is unreachable from whole networks — in
Russia the range is routinely blocked — so a node that cannot register still installs: the step reports
the reason, the core is written with direct egress, and the run succeeds. `termit-uv warp status` and
`termit-uv doctor` say the exit is direct until `termit-uv init --only warp` succeeds on a network that
can reach Cloudflare.

**The install ends by checking the exit.** When WARP is on and the node is registered, the last step
(`warp-egress`) fetches Cloudflare's trace through the node's own tunnel and reads the address it came
from. A tunnel that is configured but carries nothing is the failure this project exists to avoid, and
registration succeeding does not prove the tunnel works: the API answers over HTTPS while the WireGuard
endpoint can be filtered. A wrong or failed answer is a warning naming the address, never a failed
install — the node is a working node, it is just exiting directly.

If the host cannot run WARP — no `/dev/net/tun` or no package for the architecture, both of which only the
`proxy` transport needs — `termit-uv init` and `termit-uv warp mode` say so with the exact reason and the
node keeps working with direct egress. Nothing is silently
degraded, and `termit-uv uninstall` disconnects the tunnel it left behind.

A node behind a network that filters the WireGuard endpoint does not need a different release:
`termit-uv init --warp-transport proxy` puts it back on the 0.3.x path, where the tunnel is Cloudflare's
own client over HTTPS. The transport is a per-network decision, not a per-version one — the `0.4.0`
production pass found one node of two in that position.

### 6.2 Refusing BitTorrent

`security.bittorrent_block: true` makes the core look at the first bytes of every connection and
blackhole the ones it recognises as BitTorrent. It is off by default and it is not part of the firewall:
it lives in the generated core configuration, and sniffing costs a little CPU on every connection.

```yaml
security:
  bittorrent_block: true
```

Re-run `termit-uv init` (or wait for the daily cycle) and the rule is regenerated into the config; the
core is reloaded the same way any config change is — hot update first, validated restart second. To
undo it, set the key back to `false` and re-run.

Two properties are deliberate and easy to "improve" by accident: `routeOnly: true` keeps the
destination the client asked for (without it a sniffed HTTP `Host` replaces the address and the tunnel
dials a name the node resolves), and `destOverride` stays empty (BitTorrent is classified whether or not
a protocol is listed, so listing `http`/`tls`/`quic` buys nothing and parses every connection).

The block sits on `protocol: bittorrent` in the routing table, before the egress rules, so it applies
whatever the WARP mode is. It cannot be checked from a script: the way to verify it is to run a real
torrent client through the node and watch the handshake die while a normal HTTPS request through the
same client still works. The generated document itself is covered by
`internal/uv/xray/testdata/xray-bittorrent.json` and accepted by the pinned core (`xray -test`).

## 7. Certificates

Both sides use ACME HTTP-01. A node answers the challenge through the nginx webroot it already
serves — Termit never opens a second public port for it — and the MN binds `:80` only while an order is
in flight (`acme.challenge_listen`).

On a node:

- `termit-uv-renew.timer` runs daily with up to an hour of jitter, `Persistent=true`, and calls
  `termit-uv renew-cert`. It renews when the certificate is inside `acme.renew_before_days` (default 30) of
  expiry, reloads nginx and leaves the core alone (it speaks plaintext behind nginx).
- `termit-uv renew-cert` runs the same check on demand; `--force` replaces a healthy certificate, which is
  what a drill or a recovered CA wants.
- Nothing is written until the CA returns a chain: a failed renewal keeps the old certificate
  serving, and `termit-uv status` reports the last run as failed with the failing step. `termit-uv doctor` fails
  below 0 days and warns below 14.
- The agent reports the new leaf on its next sync; the server follows the pin, so a renewal does not
  break the probes.

On the MN, `termit-mn serve` issues a certificate at startup if it is inside `acme.renew_before_days`, then
re-checks every six hours. A renewal replaces the files and the running server serves the new pair on
the next handshake — no restart, no window where the old one is already gone:

- Nothing is written until the CA returns a chain, so a failed attempt leaves the current certificate
  serving. The failure is logged (`certificate renewal failed; the current certificate stays in
  service`) and retried six hours later.
- `termit-mn status` and `termit_mn_certificate_expiry_timestamp_seconds` read the certificate from disk, so a
  renewal is visible immediately rather than after the next restart.
- The renewal needs `:80` for the HTTP-01 challenge. If something else on the host holds it, the
  attempt fails and is logged; the certificate keeps serving until that is fixed.

To move a deployment off production Let's Encrypt (a lab, a private CA with an ACME endpoint):

```bash
termit-uv init --acme-ca https://acme.lab.example/dir --acme-skip-tls-verify   # lab only
```

`--acme-skip-tls-verify` is reported by `termit-uv doctor` for as long as it is on. Run `termit-uv doctor` after
changing anything about the CA.

## 8. Clients, devices and subscriptions

The owner's own devices are **local users** on the node: they are written into the core config and
never sent to the MN.

```bash
termit-uv device list
termit-uv device add --label phone        # prints the new device's vless:// URI
termit-uv device remove phone
termit-uv uri                             # this node's own URI, for the default device
```

### 8.0 One page with every link

Handing out links one `termit-uv uri` at a time is fine for a shell and awkward on a phone, so the node can
also serve a page that collects them:

```bash
termit-uv options            # renders it and prints the address
termit-uv options --rotate   # new address; the old one answers 404
```

`https://<node-domain>/<32 hex characters>/` shows this node's own device, every mesh peer the subscription
describes, and the two subscription forms, each with a copy button. It is a **static file in the decoy
site's tree**, so nginx needs no new location, and the consequences are worth knowing:

- The path is generated once and lives in the state file (`options.token`), not in the configuration;
  `termit-uv status` prints the address, so it can be found again months later.
- Anyone with the address sees the same links — the model is the subscription token's — so the page
  carries `noindex`, is served with the site's own headers, and is only as safe as the browser history it
  ends up in.
- `init`, `renew-site` and `sync` re-render the page when it exists, because the decoy step rewrites its
  whole tree and the peer set moves under it. None of them *creates* one: a node whose owner never asked
  for a page stays without one.
- The page is built from what the node can see. With the management server unreachable it says so and
  shows the local device only — exactly when a link is worth having.
- The node's own entries carry the fragmentation it configured (`happ_fragment`), the mesh entries carry
  the server's: set both to the same value and the page speaks with one voice. A link carries every
  dialect at once, because the owner copies it without being asked which client they run: Happ reads
  `fragment=…`, `podkop-engine` and the patched sing-box builds read `sb.tls.fragment=1` and
  `sb.tls.record_fragment=1`. A fresh node writes `proto.DefaultHappFragment`; a configuration that
  predates the key keeps its links plain, and `init --happ-fragment ""` turns all three off deliberately.
- The page is written in the configured language (`locale: ru` or `en`, Russian by default), as are both
  menus — but only where the console can show Cyrillic: on a C or latin-1 locale the menus fall back to
  English, because a screen of question marks is worse than English. The page never falls back, since a
  browser always has the glyphs, and this node's own device appears once, under "this node", not again in
  the mesh list it also belongs to.

The subscription is the mesh view: one URL per node, containing that node's own profile plus every healthy
peer, with a **pair credential** per peer — valid on that pair and nowhere else.

```bash
termit-uv status                    # shows this node's subscription URL
termit-mn sub rotate node1.example.net --domain mn.example.com   # new token, old one stops working
```

- `https://<MN>/sub/<token>` — base64 of one `vless://` URI per line. This is what a client's
  "subscription URL" field wants.
- `https://<MN>/sub/<token>/json` — the same peers as a sing-box profile with a selector, a `direct`
  outbound and private ranges kept direct.

Both are stable for a given peer set and carry an `ETag`, so a caching client costs one `304`. Rotating the
token revokes a client that should no longer see the mesh; it does not touch the node's own devices, which
never leave the box.

Fragmentation is the one client-side defence that costs nothing on a path that does not need it: the same
handshake, in more pieces. It is stated three ways because clients differ in where they can be told:

```yaml
subscription:
  fragment: false                            # sing-box profile: split across TCP segments
  record_fragment: true                      # sing-box profile: split across TLS records
  happ_fragment: "100-200,1-3,tlshello,6"    # Happ: the per-link fragment= parameter
```

A fresh `termit-mn init` writes that: fragmentation on, because the paths this project is deployed against
freeze a plain ClientHello often enough that it should not depend on somebody remembering a switch. A
configuration written before these keys existed keeps whatever it says — the loader never adds them — so an
installed server changes only when its operator asks:

```bash
termit-mn init --record-fragment --happ-fragment 100-200,1-3,tlshello,6   # on
termit-mn init --no-fragmentation                                        # off, all three
```

The two booleans reach both documents, because the same client may take either. In `/json` they add
`tls.fragment` and `tls.record_fragment` — never by writing `false`, because sing-box validates strictly
and refuses a field it does not know. In the plain list they appear per link as `sb.tls.fragment=1` and
`sb.tls.record_fragment=1`, the spelling `podkop-engine` reads from a URI; a client that does not know a
parameter ignores it, so one list serves Happ and podkop alike (`podkop-engine` r13 knows both). Enable
`record_fragment` when connections hang right after the handshake starts, or when the same profile works
from one network and not another: on a path recorded in `podkop-engine`'s notes that froze every Chrome
ClientHello, 0 of 15 connections got through without it and 15 of 15 with it.

`happ_fragment` is a different mechanism: Happ reads the value from the link itself, so the plain base64
list carries it and a phone needs no setting at all. The value is Happ's `length,interval,packets[,maxSplit]`,
appended as a custom parameter before the label, where a client that does not know it ignores it and
`ParseVLESSUrl` still round-trips. The fourth field is the one that matters: it bounds the number of pieces
and therefore the delay. Measured with Happ's own ping against these two nodes — 769 ms without
fragmentation, 446 ms with `300-500,1-5,tlshello`, 464 ms with `100-200,1-3,tlshello,6`. The fragmented
handshake came out *faster*, because the plain ClientHello was paying for a first packet that never arrived.
Leave the bound off with a small length and the reverse happens: Happ's documentation example
`1-10,5-20,tlshello` cuts a ~700-byte handshake into 70–1500 pieces with 5–20 ms between them, measured at
more than five seconds on the same profile.

The server never looks at any of these, so a node needs no reconfiguration; the value only ever
changes the text a client downloads.

### 8.1 Which clients can use the profile

The profile is VLESS + **XHTTP** (`mode=packet-up`) + TLS, and XHTTP is not a transport every client
has. Check the core, not the store listing:

| Client | Core | Works |
|---|---|---|
| Happ, v2rayNG, v2rayN, NekoRay, FoXray | Xray | yes |
| podkop + `podkop-engine` r11 or newer (OpenWrt) | sing-box with the XHTTP transport patched in | yes |
| Hiddify, Streisand, sing-box itself | sing-box | no — no XHTTP transport upstream |
| Husi | sing-box fork | partial; its XHTTP client is not interoperable with Xray's inbound |

A client without XHTTP does not report "unsupported transport": it falls back to plain VLESS over TLS,
the node answers its request with an HTTP `400`, and the client's VLESS reader reports the first byte
of `HTTP/1.1` as a version — `unknown version: 72` (`72` is `H`). Treat that message as "wrong client",
not "broken node".

Happ's per-server ping is not a health check for this profile. It reports "Ошибка TLS-рукопожатия"
while the tunnel carries traffic, because it probes something other than the secret path (a fixed
health-check URL, or the path without the XHTTP handshake). Judge a profile by a real request —
`https://www.cloudflare.com/cdn-cgi/trace` answering with `warp=on` — and leave the ping alone. The
same warning applies to any client that shows a synthetic latency number instead of moving data.

The subscription's `/json` form is a sing-box profile, so it is only useful to the builds that have
XHTTP: stock sing-box rejects the `xhttp` transport it declares.

The URIs carry `fp=firefox`, an `extra` block with `xPaddingBytes` and `xmux`, and no `alpn`. `fp` is a
client-side uTLS fingerprint the node never inspects; `firefox` is the measured choice because a `chrome`
ClientHello is frozen on some consumer paths when several connections open to one host at once.
`xPaddingBytes` pads each request so its size is not a constant, at a few hundred bytes per POST. `xmux`
reuses one connection for many proxied connections — measured through the node's nginx hop, 20 proxied
connections took 1 TCP connection with it and 5–8 without — which matters on a lossy path, where every
new connection is another chance to lose a handshake. No `alpn` is offered because the transport is
HTTP/1.1 on the wire: nginx terminates TLS and forwards HTTP/1.1 to Xray.

## 9. Node lifecycle operations

Every one of these is audited on the server.

| Goal | Command | Notes |
|---|---|---|
| list nodes | `termit-mn uv list` | state, failed cycles, last seen, agent version |
| inspect one node | `termit-mn uv show <id\|domain>` | state history, last probe, last self-check |
| admit a pending node | `termit-mn uv approve <node>` | `invite+approval` mode |
| reject a pending node | `termit-mn uv reject <node>` | becomes `removed`; credentials stop working |
| block a node | `termit-mn uv block <node> --reason "…"` | operator override, audited |
| unblock a node | `termit-mn uv unblock <node>` | operator override; the node keeps its credentials |
| remove a node | `termit-mn uv rm <node>` | `removed`; it disappears from peers' sets |
| delete the record | `termit-mn uv forget <node>` | only after removal; erases the credentials |
| read a node's agent key | `termit-mn uv agentkey <node>` | for an owner who lost it; does not break the node |
| replace a node's agent key | `termit-mn uv reissue <node>` | the old key stops working immediately; the new one is shown once |
| check now | `termit-mn check now <node>` or `termit-mn check now --all` | one cycle, on demand |
| tune the schedule | `termit-mn policy set check_interval 12h` | runtime override; `policy show` lists the keys |
| **node blocked itself** | `termit-uv unblock` (on the node) | Scenario B: the server re-verifies DNS and the tunnel before re-admitting |
| **node moved host** | `termit-uv migrate --relocate --domain new.example.net` | Scenario C |
| **node rebuilt, state lost** | `termit-uv init --relocate --agent-key-file <file>` then `termit-uv migrate --relocate` | reuses the record; mints a new identity |

Two overrides are worth understanding before you use them:

- **`block` and `unblock` are the operator's word against the prober's.** An operator block is not
  undone by a passing check: the node stays blocked until `unblock` (or the node asks with
  `termit-uv unblock`, which re-runs the checks). This is deliberate — otherwise a flapping check would
  flap the mesh.
- **`reissue` rotates the agent key, not the pair credentials.** The node must be given the new key:
  a running node cannot pick it up by itself, so the usual path is `termit-uv init --relocate
  --agent-key-file` on a box whose state is being rebuilt, or `termit-uv enroll --force` (which consumes an
  invite and creates a new record). Reissuing is for a key that may have leaked.

### 9.1 What the states mean

| State | Meaning | What fixes it |
|---|---|---|
| `pending` | enrolled, waiting for an operator | `termit-mn uv approve` |
| `active` | checks pass, in every subscription | — |
| `unavailable` | one failed cycle; still in subscriptions, labelled degraded | a passing check |
| `blocked` | two failed cycles in a row, or an operator decision | `termit-uv unblock` from the node, or `termit-mn uv unblock` |
| `removed` | out of the mesh for good | a new enrollment |

`unknown` is not a state: it is the absence of a verdict. A prober that cannot run, a DNS resolver that
cannot answer and a node whose certificate could not be read all leave the state untouched.

## 10. Monitoring

- `GET {prefix}/healthz` on the MN — liveness plus build identity, `200`/`503`. The prefix is
  `api_prefix` from `/etc/termit/mn/config.yaml`, and `termit-mn status` prints it. Put it behind your
  usual external monitor.
- `GET /metrics` on the MN — Prometheus text, **loopback only**. It lives on the same TLS listener as
  everything else, at the root of the host (`https://127.0.0.1/metrics`) and not under the API prefix, and
  it answers `403` to any other address. Scrape it from the same host:
  enrollments, syncs and check outcomes by result, a check-duration histogram, node counts by state,
  the scheduler's backlog, whether a probe worker exists, and the server certificate's expiry.
  `docs/PROTOCOL.md` §3.2 lists every family.
- `termit-mn status` — the same numbers, plus per-node state and the effective interval.
- `termit-mn uv show <node>` — one node's recent checks and state history.
- `termit-uv status --json`, `termit-uv doctor --json` — machine-readable node state, for a local monitoring agent.
- Journald: both binaries log structured lines to stderr. `journalctl -u termit-mn -u termit-uv-agent
  --since -1h`.

A useful alert set, in the order that matters:

| Alert | How |
|---|---|
| the server is down | `{prefix}/healthz` fails twice in a row |
| no checks are happening | `termit_mn_prober_up == 0` for 5 minutes, or `termit_mn_check_queue_due > 0` and growing |
| the scheduler is behind | `termit_mn_check_queue_oldest_due_seconds` above a few hours |
| a node is blocked or unavailable | `termit_mn_nodes{state="blocked"}` above zero, or `termit-mn status` |
| the server certificate is expiring | `termit_mn_certificate_expiry_timestamp_seconds - time() < 14 days`; restart the server inside the renewal window (§7, §11) |
| clocks under 14 days on a node | `termit-uv doctor` / `termit-uv status` on the node, or `openssl x509 -enddate` |
| a burst of refusals | `rate(termit_mn_enrollments_total{result="refused"}[15m])` |
| disk over 90% | the host's usual check; a full disk stops SQLite writes and ACME |

## 11. Known limitations

- **Accepting the `proxy` transport needs a host with `/dev/net/tun`.** The default `wireguard`
  transport, fail-open, the route and the refusal are tested; installing and connecting the `warp-cli`
  sidecar is on the release checklist for a host where it can run.
- **The Docker Compose harness does not exist.** The lab is the acceptance gate.
- **`termit-uv` has no self-update.** The node is upgraded by running the bootstrap script or by
  replacing the binary and re-running `termit-uv init`; nothing pulls a release on a timer, and nothing
  should — an unattended proxy should not change itself.
- **Blocking is cooperative.** A node that ignores the server's answer keeps serving; see
  [`THREAT_MODEL.md`](THREAT_MODEL.md) §6.
- **The host firewall survives a reboot only once it was confirmed.** `termit-uv-firewall.service`
  restores what the record says was applied and confirmed (§13.7); a table that was applied and never
  confirmed does not come back, and neither does one an operator took down. The bans survive on their
  own — `bans.json` outlives the reboot and the guard refills the kernel from it, as soon as the table
  exists again.

## 12. Building and publishing a release

A release is one command, and CI runs the same one on a tag:

```bash
go run ./deploy/release --version v0.4.1 --out dist
```

It cross-compiles both binaries for `linux/amd64` and `linux/arm64` with `CGO_ENABLED=0 -trimpath`, stamps
`internal/version` (version, commit, date), copies the bootstrap script and the server's units from the
repository, and writes `SHA256SUMS`:

| File | What it is |
|---|---|
| `termit-uv_<version>_linux_<arch>` | the node binary; this is what `uv.sh` downloads |
| `termit-mn_<version>_linux_<arch>` | the management server binary |
| `uv.sh` | the bootstrap installer (§4) |
| `termit-mn.service` | the unit from `deploy/systemd/`, so a server install needs no clone |
| `termit-mn-firewall.service`, `termit-mn-firewall-rollback.service`, `.timer` | the server's boot unit and the rollback pair (§13.6) |
| `termit-mn-guard.service`, `termit-mn-blocklist.service`, `.timer` | the server's ban engine and blocklist (§13.6) |
| `SHA256SUMS` | covers every file above, in `sha256sum -c` format |

The names are a contract with `uv.sh`, which fetches `<base>/<version>/<asset>` and `<base>/<version>/SHA256SUMS`;
a renamed asset is a broken installation, so the tool refuses a version that is not a `vX.Y.Z` tag rather
than sanitising it. Verify a directory the way an operator would:

```bash
cd dist/v0.4.1 && sha256sum -c SHA256SUMS
```

There are deliberately no tarballs: one artifact per platform, verified by digest, is what the installer
consumes, and a tarball adds an unpacking step that protects nothing. The build is reproducible given the
same toolchain, commit and `--date`:

```bash
go run ./deploy/release --version v0.4.1 --out /tmp/a --commit "$(git rev-parse --short HEAD)" --date 2026-11-10T00:00:00Z
go run ./deploy/release --version v0.4.1 --out /tmp/b --commit "$(git rev-parse --short HEAD)" --date 2026-11-10T00:00:00Z
diff /tmp/a/v0.4.1/SHA256SUMS /tmp/b/v0.4.1/SHA256SUMS
```

Publishing is a tag push on `main`, after the branch process in `AGENTS.md` §9 has put a tested tree there:
`.github/workflows/release.yaml` builds the directory, runs `sha256sum -c SHA256SUMS`, asserts `uv.sh` is
executable and attaches everything to the GitHub release, `uv.sh` included — the install URLs in the
documentation point at `releases/download/<version>/uv.sh`. The workflow marks the release
`prerelease: true`; clearing that flag is the deliberate act that records the release's own production
pass (`docs/IMPLEMENTATION_PLAN.md` M4.3 for `0.2.1`, M5.8 for `0.3.2`, §11.9 for `0.4.0`). A maintainer
can do the same by hand with `gh release create` and the files from `dist/<version>/`: the assets are plain
files, so nothing depends on the workflow.

Before that tag exists, a `release/<version>` branch is built by hand:
`gh workflow run release.yaml --ref release/v0.4.1 -f publish_prerelease=true`. The workflow numbers the
build itself: the pre-release is `v<version>p<n>`, `n` being one more than the highest `v<version>p*` tag
that already exists, so the first build of `0.4.1` is `v0.4.1p1` and the counter never resets for that
version. The artifacts carry that same version — installed with `sh uv.sh --version v0.4.1p1` — and the
body is the `0.4.1` section of the changelog, because a section per build would be noise. That is how
testers get a build without anything being published from `dev` or `main`.

A **release** body is longer. Versions handed out only as pre-releases were never installed by a
participant, so the body of the next release counts back to the last one that was:
`go run ./deploy/release --version v0.4.1 --notes --since v0.4.0` prints the `0.4.1` section and stops at
`0.4.0`. The workflow works the `--since` value out itself — the newest GitHub release not flagged
pre-release — so nobody has to remember which version participants last saw. `--since` refuses a version
the changelog has no section for, and one that is not older than the release being cut: a typo must not
silently publish everything or nothing.

CI gates every push to `main`, `dev` and `release/**`, and every tag and pull request: `gofmt`, `go vet`,
the unit suite with `-race`, the cross-build, the release tool's own tests and `govulncheck`. It never
publishes except through the two doors above. The lab suite is the acceptance gate and is run by hand
(`AGENTS.md` §4.3); CI does not claim it.

After the push, drop what the build left behind:

```bash
go run ./deploy/clean --dry-run     # what would go, and how much it frees
git push && go run ./deploy/clean   # the habit: the artifacts are spent once they are published
```

It removes the release directory, stray binaries, the local development state and everything in the scratch
directory that is not on its keep list — the Go toolchain, the caches and the lab's notes stay, so the next
build is as fast as this one. When space is the problem, `--build-cache` is the cheap half: it drops the
compiled output (gigabytes after a release cycle) and keeps the downloaded modules, so the next build
recompiles for a few seconds and downloads nothing. `--module-cache` drops the modules as well and costs a
download; `--all` does both; `--toolchain` removes Go itself.

## 13. The host firewall (opt-in)

Termit ships a firewall and never turns it on. One nftables table per component — `inet termit_uv` on a
node, `inet termit_mn` on the server — exists only after an operator types `termit-uv firewall on`;
`init`, `sync`, the agent, the menus and every timer leave it alone, and `firewall off` deletes it and
leaves the host exactly as it was. That last sentence is a lab assertion, not a promise: the test
compares `nft list ruleset` before and after. Nothing here is required for the tunnel to work — it is
host hardening for a machine with a public address.

### 13.1 What the table contains

In the order the input chain evaluates it:

| Rule | Why |
|---|---|
| `iif lo accept` | loopback is not a decision |
| `ct state established,related accept` | a connection already flowing is not re-judged |
| `ct state invalid drop` | malformed state is never legitimate |
| `ip saddr @blocked4 drop`, `ip6 saddr @blocked6 drop` | the downloaded list (§13.5) |
| `ip saddr @banned4 drop`, `ip6 saddr @banned6 drop` | what the guard banned |
| `icmpv6 type { nd-*, packet-too-big, destination-unreachable, time-exceeded } accept` | without NDP and PMTU messages IPv6 stops working |
| `tcp dport { 22, 80, 443 } accept` | what a node publishes — or, with `ssh_from`, port 22 only from the listed networks |
| `meter scan4m { … } add @scan4 { … }` | counts attempts to everything else per source, in the kernel — present only when the guard is configured (§13.4), because the guard is what reads the set |
| `counter drop` | with `default_deny`, everything else is refused |

Two properties are worth knowing: **the table is one atomic transaction** — `firewall on` feeds the whole
ruleset to `nft -f -`, which commits it or nothing, and re-applying replaces rather than merges — and
**bans live in sets, not rules**, so adding or removing one element never leaves the chain half-written.

### 13.2 Turning it on without locking yourself out

```bash
termit-uv firewall on --dry-run              # print the rules; changes nothing
termit-uv firewall on                        # arm the timer, apply, record
termit-uv firewall confirm                   # within five minutes, from the same session
```

`on` arms a five-minute rollback timer **before** it applies anything. If `confirm` does not run in time,
the timer runs `termit-uv firewall rollback`, which deletes the table and stops the timer — that is what
makes the command safe over the connection it might cut, and it is worth testing once on purpose. `status`
names the state:

```
firewall:   on, not confirmed — the table is removed in 4m55s unless you run `termit-uv firewall confirm`
firewall:   on, confirmed 3m ago
firewall:   off since 1s ago
```

If you reach SSH from a fixed address, restrict port 22 to it — the networks are compiled into the table,
so the restriction survives a rebuild:

```bash
termit-uv firewall on --ssh-from 203.0.113.0/24,2001:db8::/32
```

Port 22 is then refused from everywhere else even with `default_deny: false`: a restriction that holds
only when the rest is denied is not a restriction. `ssh_from` in the configuration does the same for every
future `on`.

### 13.3 The way back

```bash
termit-uv firewall off        # removes the table and the timer; the host is as it was
```

No confirmation, no argument, and it works from any state — after a rollback it is a no-op. On a host you
have locked yourself out of, wait out the five minutes: the timer runs locally over systemd and needs no
SSH.

### 13.4 The ban engine

With `security.guard.enabled: true`, `init` installs and starts `termit-uv-guard.service`. It watches two
signals and does one thing with them:

| Signal | Evidence | First ban | Repeat |
|---|---|---|---|
| Port scan | the kernel's scan set: attempts to ports the host does not serve, N per window | 1 hour | 24 hours |
| Failed requests | the web server's log: 4xx answers from one source, counted in a window | 5 minutes | doubling to 1 hour |
| You | `termit-uv bans block <ip>` | permanent | until `bans unblock` |

A repeat only escalates inside `security.guard.ladder_window`, measured **from the end of the previous
ban**: coming back a day later earns the long ban, coming back a week later starts over. When the ladder
has nothing harder to offer the source is marked an *offender* — nothing blocks it forever, and
`termit-uv bans offenders` is where you decide.

The guard never applies or removes the table, only elements inside it, which is why it is safe to leave
running: with no table applied it reads nothing and does nothing.

```bash
termit-uv guard status              # is it on, and what has it decided
termit-uv bans list                 # what is banned now, and why
termit-uv bans offenders            # sources the ladder ran out of rungs for
termit-uv bans block 203.0.113.7    # permanent, my decision
termit-uv bans unban 203.0.113.7    # lift it now; the ladder remembers the offence
termit-uv bans unblock 203.0.113.7  # lift a permanent block of mine
```

Decisions live in `/var/lib/termit/uv/bans.json` and survive a reboot; the kernel's sets do not, and the
guard refills them on its next pass. `unban` lifts a ban but keeps the offence, so the next one earns the
longer ban — it is not a way to forget what happened.

**Never banned, whichever signal fires**: loopback, the private and link-local ranges, the management
server (resolved from `mn.url`, cached for ten minutes) and your own `ssh_from` networks. `bans block`
refuses them too — a ban there cuts you off, not an attacker. To block an address in one of those ranges
anyway, do it with `nft` directly and know what you are doing.

### 13.5 The blocklist

A list of networks you never want to hear from again — a hosting provider, a scanner's range, an abusive
subnet — comes in over a URL:

```yaml
security:
  firewall:
    blocklist_url: https://example.net/termit/blocklist.txt
    blocklist_refresh: 24h
```

`http(s)` and `file` URLs and nothing else: no git, no rsync. The file is one network per line, `#` starts
a comment, a bare address means its `/32` or `/128`; overlapping and duplicate entries are normalised and
sorted on the way in, so the digest does not move when the order of a source does. The body is capped at
4 MiB.

`init` installs `termit-uv-blocklist.timer` when there is a URL: the oneshot runs a minute after the timer
starts, then once per `blocklist_refresh`, with jitter and across reboots.

```bash
termit-uv firewall refresh                 # fetch, validate, put in force
termit-uv firewall refresh --dry-run       # fetch and validate only
termit-uv firewall refresh --url file:///root/list.txt   # one-off, test a list before you commit to it
termit-uv firewall status                  # how many networks are in force, and when they were fetched
```

The list in force lives in `/var/lib/termit/uv/blocklist.txt`, and the kernel's sets are rebuilt from that
file, not from the network: a refresh that finds the source unchanged still reloads the kernel, which is
what repairs a reboot. A failed download, a body that does not parse and an empty list are all refused
with "the list in force was left alone" — an empty blocklist silently unblocks everything, which is worse
than an outdated one.

`firewall on` loads the file too, so a table applied after a reboot comes back with its networks. With no
table applied, a refresh writes the file and leaves the kernel alone.

### 13.6 The management server

The server can carry the same table, with its own name and ports. The commands are the same with a
different binary, minus the two things a server does not have: no access log to count 4xx answers (its
admin API is rate-limited instead) and no proxy core to sniff.

```bash
termit-mn firewall status|on|off|confirm|rollback|refresh|restore
termit-mn bans list|offenders|block IP|unban IP|unblock IP
termit-mn guard status|run [--once]
```

The table is `inet termit_mn` and it publishes **22, 80 and 443**: 80 stays open for every node's
certificate renewal, not only while its own order is in flight. The server's units are plain files in
`deploy/systemd`, not rendered by the binary, so `firewall on` **refuses to run until the rollback timer is
installed** — an apply with no way back is worse than a refused command:

```bash
install -m 0644 termit-mn-firewall-rollback.service \
                   termit-mn-firewall-rollback.timer \
                   termit-mn-firewall.service \
                   termit-mn-guard.service \
                   termit-mn-blocklist.service \
                   termit-mn-blocklist.timer /etc/systemd/system/
systemctl daemon-reload
systemctl enable termit-mn-firewall
systemctl enable --now termit-mn-guard termit-mn-blocklist.timer
```

Do not enable the rollback timer: the command starts it, and it exists to fire once. The blocklist timer
runs `termit-mn firewall refresh` daily; its interval is the timer file, not
`security.firewall.blocklist_refresh`, because the server's units are edited rather than generated.

The guard and the refresh run as root because nft does, confined as the node's are: `ProtectSystem=strict`,
writes only to `/var/lib/termit/mn`, and `RestrictAddressFamilies` **including `AF_NETLINK`** — without it
every cycle fails with "Unable to initialize Netlink socket". The ban record and the blocklist live beside
the database, so the backup in `RUNBOOK.md` §10 carries them.

### 13.7 What survives a reboot

The table is a kernel object, so a reboot empties it — but the record of what was applied does not.
`termit-uv-firewall.service` runs at boot, before `network-pre.target`, and hands the decision to
`termit-uv firewall restore`:

| The record says | At the next boot |
|---|---|
| the apply was **confirmed** | the table comes back, ports, `ssh_from` and `default_deny` included, and no timer guards it — you already accepted exactly this table |
| the apply was still **waiting** and its five minutes have not run out | the table comes back and the rollback timer is armed with what is left of the window |
| the apply was **never confirmed** and the window is gone | nothing: nobody ever vouched for that table |
| you ran `firewall off` | nothing: the record says you took it down |

Nothing to restore is not an error — the unit exits 0 and the journal says so. `termit-uv firewall restore
--dry-run` shows what a reboot would do before you reboot.

Restoring a waiting apply **arms the rollback timer**, which writes the timer's units, so the boot unit's
sandbox allows writes to the unit directory; both units say so in `ReadWritePaths`. Without that line the
pending case fails at boot with `read-only file system` and the table does not come back (that was
`0.3.0`); a confirmed apply was never affected, because it arms nothing. The server has the same unit as
`deploy/systemd/termit-mn-firewall.service` (§13.6), enabled the same way.

### 13.8 What it does not do

- It does not rate-limit proxied traffic. Your clients are not throttled — that would add latency to the
  one thing the node exists for.
- It does not touch routing, NAT or WARP. Proxied traffic still leaves through the WARP tunnel — inside
  the core by default, through the loopback sidecar on `warp.transport: proxy` — and direct traffic still
  goes direct.
- It does not survive a **configuration loss**. If the record goes and the configuration still asks for a
  table, `firewall on` is the only way back: the boot unit restores what was applied, it does not invent
  one.
- It does not hide the node from a targeted observer. It refuses scans and probes; see
  [`THREAT_MODEL.md`](THREAT_MODEL.md).
