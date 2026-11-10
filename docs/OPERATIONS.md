# Termit operations

How to install, run, upgrade and repair a Termit mesh. This is the operator's document: the design is
in [`DESCRIPTION.md`](DESCRIPTION.md), the wire contract in [`PROTOCOL.md`](PROTOCOL.md), the
adversaries in [`THREAT_MODEL.md`](THREAT_MODEL.md).

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
| UV | `warp-cli` (Cloudflare's unit) | `127.0.0.1:40000` | the WARP SOCKS5 sidecar, when WARP is on |

Termit never touches host routing or the firewall, so every other service on the box (SSH included)
keeps working through every code path. That is a contract, not a default.

### 1.1 Paths

| Path | What |
|---|---|
| `/etc/termit/mn/config.yaml`, `/etc/termit/mn/master.key` | server configuration and the master key (`0600`) |
| `/var/lib/termit/mn/termit.db`, `…/certs/` | registry, ledger, the server's own certificate |
| `/run/termit/mn/admin.sock` | the admin socket (`0600`, directory `0750`) |
| `/etc/termit/uv/config.yaml` | node configuration (public, `0644`: it holds no secret) |
| `/var/lib/termit/uv/state.json` | node identity, seeds, credentials, schedule (`0600`) |
| `/var/lib/termit/uv/certs/` | the node's `fullchain.pem` and `privkey.pem` (`0600`) |
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
- **`/dev/net/tun` for WARP.** An unprivileged LXC container usually has none. That is not a failed
  install: the node records the refusal, keeps the mode you asked for, and sends traffic directly
  (§6).
- **Outbound access** to the ACME directory, to the node's management server, to `github.com` for the
  pinned Xray release (or your mirror), and — when WARP is on — to Cloudflare's package repository and
  WARP endpoint. Nothing else.

The MN's probe worker needs an `xray-core` binary of its own: `prober.xray_binary` in
`/etc/termit/mn/config.yaml`. Without it every check is `unknown`, which never blocks a node — the
mesh still works, the server just cannot vouch for it.

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
config:       /etc/termit/mn/config.yaml
database:     /var/lib/termit/mn/termit.db (schema 5)
master key:   /etc/termit/mn/master.key (generated now)
master key id: <fingerprint>

admin token (shown once, store it now):
  <token>

first invite code (shown once, 1 use(s)):
  ABCD-EFGH-IJKL

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

Verify:

```bash
curl -sS https://mn.example.com/6rfjdqfn5nno/healthz | jq .   # {prefix}/healthz, see §3.1
termit-mn status
```

`termit-mn status` is the one screen to open first:

```
server:   mn.example.com
listen:   :443
version:  termit-mn 0.1.0 (commit …, built …, api 1, profile v1)
uptime:   3h12m (started 2026-11-10 11:18)
database: /var/lib/termit/mn/termit.db (412.0 KiB, schema 5)
prober:   ready (/usr/lib/termit/bin/xray)
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
with the stream module, routing on the SNI before any TLS is parsed (`libnginx-mod-stream`):

```nginx
# /etc/nginx/stream.d/mn.conf, included from a stream{} block in nginx.conf
map $ssl_preread_server_name $mn_backend {
    mn.example.com       127.0.0.1:10443;
    default              127.0.0.1:8443;   # the sites nginx terminates, moved off :443
}

server {
    listen              443;
    proxy_pass          $mn_backend;
    proxy_protocol      on;                # keeps the other sites' clients visible
    ssl_preread         on;
}

# The server does not speak the PROXY protocol, so it gets a second hop that accepts and strips it.
server {
    listen              127.0.0.1:10443 proxy_protocol;
    proxy_pass          10.0.0.5:443;
}
```

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

The server sees the connection as coming from the proxy: it does not read `X-Forwarded-For`, and the
PROXY header is stripped before the server. Node addresses on the server are the proxy's.
## 4. Install a node

The bootstrap script verifies a release checksum, installs the binary and then lets `termit-uv init` do the
work. It never guesses a version and never installs an unverified binary — and it is a file you keep,
not something piped from a branch:

```bash
termit-mn invite create --note "node1" --uses 1        # on the MN; prints the code once

# on the fresh node, as root
curl -fsSLO https://github.com/shogun-kub/termit/releases/download/v0.1.0/uv.sh
sh uv.sh --version v0.1.0 \
  --domain node1.example.net --email you@example.com \
  --mn https://mn.example.com --invite ABCD-EFGH-IJKL
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
| `--dry-run` | verify the download, then run `termit-uv init --dry-run` without installing anything |

Re-running the script with a new `--version` **is** the upgrade: the binary is replaced and `termit-uv init`
converges the rest, keeping the identity, the Xray path and the certificate. The invite code is passed
on the command line and is visible in `ps` for the duration of the run; revoke it afterwards if that
matters (`termit-mn invite revoke`).

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
| `warp` | installs, registers and connects `cloudflare-warp` (skipped when WARP is off, explained when the host cannot run it) |
| `xray-binary` | downloads the pinned release, verifies its SHA-256, installs it |
| `identity` | generates the node's UUID, XHTTP path and decoy seed **once** |
| `decoy` | generates the site |
| `xray-config` | writes the core config, checks it with `xray -test` |
| `web` | writes the vhost, checks it with `nginx -t` |
| `units` | writes and enables the three units and the timer |
| `certificate` | issues the certificate over HTTP-01 through the webroot |
| `start` | starts the core, nginx and the agent, and verifies they stay up |

Useful flags: `--dry-run` (report, change nothing), `--only <steps>` / `--skip <steps>`,
`--no-warp` (opt out of WARP), `--warp selected --warp-selected a.example,b.example`,
`--xray-download-base` (a mirror, a `file://` URL or a local path), `--acme-ca` (a non-production
directory, for a lab).

Preflight is the place to read when something is wrong; it prints one line per check with a hint:

```
  root      ok    running as root
  ports     ok    :80 and :443 are free
  dns       ok    node1.example.net → 203.0.113.7
  tun       warn  /dev/net/tun is missing, so WARP cannot run on this host
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
termit-uv warp status             # mode, what warp-cli says, what a real fetch through it reports
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
being refused rather than exposed), `off`, `not-installed` (the host can run WARP but the package is
not there yet — `termit-uv init` installs it), `unavailable` (the host cannot run WARP at all, with the
reason) and `unknown` (the self-check could not tell). A missing `warp-cli` is a state, not an error:
`termit-uv warp status` reports it and exits 0, which is what makes it usable on a node that was installed
with `--no-warp`.

Fail-open is a routing decision inside the core, not a configuration rewrite: an `observatory` probes
WARP every 30 s through the tunnel, and a `balancer` with `fallbackTag: direct` sends new connections
direct while WARP does not answer. Recovery is equally automatic. With `fail_open: false` the route
names the WARP outbound directly, so the node fails closed instead — the right choice for an owner who
would rather lose connectivity than expose the address.

If the host cannot run WARP (no `/dev/net/tun`, unsupported architecture), `termit-uv init` and `termit-uv warp mode`
say so with the exact reason and the node keeps working with direct egress. Nothing is silently
degraded, and `termit-uv uninstall` disconnects the tunnel it left behind.

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

The subscription is the mesh view: one URL per node, containing that node's own profile plus every
healthy peer, with a **pair credential** for each peer — valid on that pair and nowhere else.

```bash
termit-uv status                    # shows this node's subscription URL
termit-mn sub rotate node1.example.net --domain mn.example.com   # new token, old one stops working
```

- `https://<MN>/sub/<token>` — base64 of one `vless://` URI per line. This is what a client's
  "subscription URL" field wants.
- `https://<MN>/sub/<token>/json` — the same peers as a sing-box profile with a selector, a `direct`
  outbound and private ranges kept direct.

Both are stable for a given peer set and carry an `ETag`, so a client that caches costs one `304`.
Rotating the token is the way to revoke a client that should no longer see the mesh; it does not
affect the node's own devices, which never leave the box.

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

## 10. Backup and restore

### 10.1 The management server

`termit-mn backup` writes an archive containing the database, the configuration, the server's certificate
**and the master key**:

```bash
termit-mn backup                                    # → /var/lib/termit/mn/backups/termit-<ts>.tar.gz
termit-mn backup --out /root/mn-backup.tar.gz
```

The archive holds the master key, which means it can re-derive **every pair credential in the mesh**.
Treat it exactly like the key: encrypt it at rest, store it away from the server, and do not leave it
in a world-readable directory. A backup without the master key restores the registry but cannot
produce working credentials.

Restore, on a fresh machine or after a disaster:

```bash
install -m 0755 termit-mn /usr/bin/termit-mn
termit-mn restore --from /root/mn-backup.tar.gz        # --force to replace an existing database
install -m 0644 deploy/systemd/termit-mn.service /etc/systemd/system/
systemctl daemon-reload && systemctl enable --now termit-mn
termit-mn status
```

`termit-mn restore` validates the archive before writing anything. The restored server keeps every node's
credentials, so nodes keep syncing without re-enrolling. If the address of the server itself changes,
the nodes' `mn.url` changes too (`termit-uv init` writes it, or edit `mn.url` and restart the agent).

Back up on a schedule you can defend: daily is normal, and the database is small.

### 10.2 A node

A node's state file is its identity: the self UUID, the XHTTP path, the decoy seed, the agent key and
the subscription token. Losing it means rebuilding the node (Scenario C) rather than restoring it.

```bash
tar -C / -czf /root/node1-state.tar.gz \
    etc/termit/uv var/lib/termit/uv            # configuration, identity, certificate, journal
```

Restoring that archive onto the same machine brings the node back byte for byte, including its
certificate, which is why it is worth keeping: a restore does not wait for an ACME order. Do not copy
it to another machine and keep using it — two boxes with one identity fight over the record; use
`termit-uv init --relocate` for that case.

## 11. Upgrading

Nodes and the server are separate binaries with separate records; upgrade the server first, then the
nodes. The wire protocol is additive within `api_version: 1`, so a mixed fleet keeps working.

On the MN:

```bash
systemctl stop termit-mn
install -m 0755 termit-mn.new /usr/bin/termit-mn
systemctl start termit-mn
journalctl -u termit-mn -n 20 --no-pager
termit-mn status
```

The database migrates on start. Migrations are forward-only: **take a backup before an upgrade** and
keep the previous binary next to the new one, since the old binary will not read a newer schema.

On a node, the bootstrap script is the short path — it replaces the binary and converges:

```bash
sh uv.sh --version v0.1.1 --base-url https://github.com/shogun-kub/termit/releases/download --no-enroll
```

By hand it is the same two steps:

```bash
install -m 0755 termit-uv.new /usr/bin/termit-uv
termit-uv init                       # converges every step; no credential is regenerated
termit-uv doctor
```

`termit-uv init` is also the upgrade path for everything the node generates: the units, the vhost, the core
config, the pinned Xray release when `xray.version` moved. It never rewrites the identity, the
certificate, the decoy seed or the credentials. Then restart the agent if it is running, so it picks
up a configuration it read at start:

```bash
systemctl restart termit-uv-agent
```

A deliberate restart in the middle of a burst of restarts can hit systemd's start limit; both buses
now run `systemctl reset-failed` first (`termit-uv init`, `termit-uv warp mode`, the agent's own restart), so this
is handled for you. If you restart by hand and see `start-limit-hit`, run `systemctl reset-failed
termit-xray termit-uv-agent` and start again.

## 12. Recovery playbooks

### A node is `blocked` and is fine

```bash
# on the node
termit-uv unblock                    # re-verifies DNS and the tunnel from the node's own address, then syncs
```

If it fails, `termit-uv unblock` prints the server's reason: `dns_mismatch` means the record does not point
at the box; `check_failed` means the tunnel did not answer through the decoy. Fix that, run it again.

### A node was rebuilt on the same host

```bash
termit-uv init --domain node1.example.net --relocate --agent-key-file /root/agent-key
termit-uv migrate --relocate
```

The second command is what tells the server about the new self UUID and certificate. Without it the
server keeps probing the old identity and the node stays `blocked` or `unavailable`.

### A node moved to a new host

```bash
termit-uv init --domain node5.example.net --relocate --agent-key-file /root/agent-key
termit-uv migrate --relocate --domain node5.example.net
```

The old record is reused, so peers keep the same pair credential and only the domain and path change
in their lists at the next sync. The domain must resolve to the new box and the box must serve the
profile it claims, or the server refuses the move and leaves the record as it was.

### The management server is gone

Restore from backup (§10.1). If there is no backup, the mesh cannot be rebuilt without re-enrolling
every node: each node's pair credentials derive from the master key, and the master key is not
recoverable from a node.

### The admin token leaked

Rotate it. The command replaces the credential it authenticated with, and the old one stops working the
moment the new one is issued:

```bash
termit-mn admin rotate --token-out /etc/termit/mn/admin.token
```

It needs the current token — a rotation you could start without one would be a takeover, not a rotation,
and only the authenticated admin's own row is touched. Before you run it, list what still carries the old
token: every timer, cron entry and playbook that does will fail at its next run with
`the admin token is not valid`. `--token-out` writes the new token `0600`; without it the token is printed
once and nowhere else, so if that output is lost the rotation is lost with it.

### The admin token is lost

Rotation needs the current token, so a lost token is the one case that has no online recovery: the server
stores only a digest, deliberately. Take a backup of the database, then remove the `admin` row and let
`init` mint a fresh credential:

```bash
sqlite3 /var/lib/termit/mn/termit.db 'delete from admin;'
systemctl restart termit-mn
termit-mn init --config /etc/termit/mn/config.yaml --admin-token-out /etc/termit/mn/admin.token
```

`init` on an installed server keeps every other decision — domain, listen, master key, the nodes — and
only adds the missing admin. Treat it as an incident: whoever has the old token had the registry, and the
digest in the database no longer matches it after this.

### The clock drifted

ACME and TLS fail first. `timedatectl status`; fix `systemd-timesyncd` or `chrony`, then `uv
renew-cert --force` if an order was half-finished.

### The node reports drift

```bash
termit-uv sync --check        # exit 4 and a diff
termit-uv sync                # converge now
```

Drift means the core's client set differs from the server's — what a manual edit or a missed sync
looks like. The next cycle would fix it anyway; `termit-uv sync` does it now.

## 13. Uninstalling

On a node:

```bash
termit-uv uninstall --dry-run     # what would be removed
termit-uv uninstall               # units, vhost, core, generated files; keeps config and state
termit-uv uninstall --purge       # also config, state, certificates and the service user
```

What it does: disconnects the WARP tunnel, stops and disables the units, removes every path the
install manifest recorded, restores the distribution's default nginx vhost, and reloads nginx so `:80`
and `:443` are released. What it deliberately keeps: **packages** (nginx may serve other sites; remove
`cloudflare-warp` yourself if you want it gone), and — without `--purge` — the configuration and the
state, so a reinstall keeps the same identity.

On the MN:

```bash
systemctl disable --now termit-mn
rm -f /etc/systemd/system/termit-mn.service
rm -rf /etc/termit/mn /var/lib/termit/mn
```

There is no uninstall command for the server: it owns two directories and one unit, and deleting a
registry is not something an automated command should decide to do.

## 14. Monitoring

- `GET {prefix}/healthz` on the MN — liveness plus build identity, `200`/`503`. The prefix is
  `api_prefix` from `/etc/termit/mn/config.yaml`, and `termit-mn status` prints it. Put it behind your
  usual external monitor.
- `GET /metrics` on the MN — Prometheus text, **loopback only**. Scrape it from the same host:
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
| the server certificate is expiring | `termit_mn_certificate_expiry_timestamp_seconds - time() < 14 days`; restart the server inside the renewal window (§7, §15) |
| clocks under 14 days on a node | `termit-uv doctor` / `termit-uv status` on the node, or `openssl x509 -enddate` |
| a burst of refusals | `rate(termit_mn_enrollments_total{result="refused"}[15m])` |
| disk over 90% | the host's usual check; a full disk stops SQLite writes and ACME |

## 15. Known limitations

- **WARP acceptance needs a host with `/dev/net/tun`.** The mode, the route, the fail-open behaviour
  and the refusal are tested; installing, registering and connecting the tunnel is on the release
  checklist for a host where it can run.
- **The Docker Compose harness does not exist.** The lab is the acceptance gate.
- **`termit-uv` has no self-update.** The node is upgraded by running the bootstrap script or by
  replacing the binary and re-running `termit-uv init`; nothing pulls a release on a timer, and nothing
  should — an unattended proxy should not change itself.
- **Blocking is cooperative.** A node that ignores the server's answer keeps serving; see
  [`THREAT_MODEL.md`](THREAT_MODEL.md) §6.

## 16. Troubleshooting

| Symptom | Likely cause | What to do |
|---|---|---|
| `termit-uv init` stops at `preflight` with `ports` failed | something already listens on 80/443 | `ss -H -ltnp`, stop it, re-run `termit-uv init` |
| `dns` warn at preflight | the record does not resolve here yet | point the A/AAAA record at the box; the install does not need it, enrollment does |
| `termit-uv enroll` → `dns_mismatch` | the domain resolves elsewhere, or the request came through a proxy | fix DNS; the check compares against the TCP peer |
| `termit-uv enroll` → `rate_limited` | ten failed attempts in fifteen minutes from one address | wait, then retry; the window is per address |
| `termit-uv enroll` → `domain_taken` | a record with that domain exists | `termit-mn uv show <domain>`; remove or relocate it first |
| node stuck `pending` | `invite+approval` mode | `termit-mn uv approve <node>` |
| node `unavailable` after a passing manual check | the prober cannot reach it | `termit-mn uv show` for the class; check `prober.xray_binary` and the server's own egress |
| `termit-mn status` says `prober: unavailable — every check reports unknown` | no worker could be built | read the reason on the line; fix `prober.xray_binary`, restart `termit-mn`; nodes stay as they were, nothing is blocked |
| `termit-mn status` shows a growing `queue: N due` | the scheduler is not completing passes | `journalctl -u termit-mn` for scheduler errors; a stuck probe worker shows up here before any node changes state |
| node `blocked` right after an upgrade | the certificate was replaced and the server still pinned the old leaf | the next sync reports the new leaf; `termit-uv sync` then `termit-uv unblock` if it stayed blocked |
| `termit-uv doctor` → `services` failed | a unit is down | `systemctl status <unit>`; `termit-uv init` starts them again |
| `termit-uv doctor` → `certificate` warn/fail | renewal is late or broken | `termit-uv renew-cert`; the journal of `termit-uv-renew.service` says why |
| `termit-uv status` shows `warp: … direct` | WARP is down and fail-open is doing its job | `termit-uv warp status`, then `journalctl -u warp-cli`; traffic still works |
| `termit-uv warp status` shows `not-installed` | the node was installed with `--no-warp`, or the package was removed | `termit-uv init` (or `termit-uv warp mode all`) installs and registers it; `termit-uv warp status` exits 0 in this state |
| `termit-uv warp mode` → `start-limit-hit` | too many restarts in five minutes | `systemctl reset-failed termit-xray termit-uv-agent` |
| `termit-uv sync --check` exits 4 | the core's client set differs from the server's | `termit-uv sync` |
| `mn` commands: `cannot reach the server at …` | `termit-mn` is not running, or the socket moved | `systemctl status termit-mn`; check `admin_socket` in the config |
| `termit-mn serve` refuses to start: `cert: listen on :80` | something holds `:80` (nginx on the same host) | free the port, or set `acme.challenge_listen` to a free one and forward it |
| `termit-mn serve` refuses: `cannot read the master key` | the key file is missing or unreadable | restore it from a backup; without it the registry is useless |
| SQLite `database is locked` | the database is on a slow or network filesystem, or a second process opened it | keep it on local disk; admin commands go through the server, never direct |
| subscription `404` | the token was rotated or mistyped | `termit-uv status` shows the current URL; `termit-mn sub rotate` issues a new one |

When nothing obvious is wrong, the order is: `termit-uv doctor` on the node, `termit-mn uv show` on the server,
then the journals of both. A failing check that says `unknown` is a failure of the observer, not of
the node — say so in the report rather than "fixing" the node.

## 17. Building and publishing a release

A release is one command, and CI runs the same one on a tag:

```bash
go run ./deploy/release --version v0.1.0 --out dist
```

It cross-compiles both binaries for `linux/amd64` and `linux/arm64` with `CGO_ENABLED=0 -trimpath`,
stamps `internal/version` (version, commit, date), copies the bootstrap script and the management
server's unit from the repository, and writes `SHA256SUMS`:

| File | What it is |
|---|---|
| `termit-uv_<version>_linux_<arch>` | the node binary; this is what `uv.sh` downloads |
| `termit-mn_<version>_linux_<arch>` | the management server binary |
| `uv.sh` | the bootstrap installer (§4) |
| `termit-mn.service` | the unit from `deploy/systemd/`, so a server install needs no clone |
| `SHA256SUMS` | covers every file above, in `sha256sum -c` format |

The names are a contract with `uv.sh`, which fetches `<base>/<version>/<asset>` and
`<base>/<version>/SHA256SUMS`; a renamed asset is a broken installation, so the tool refuses a version
that is not a `vX.Y.Z` tag rather than sanitising it.

Verify a directory the way an operator would:

```bash
cd dist/v0.1.0 && sha256sum -c SHA256SUMS
```

There are deliberately no tarballs: one artifact per platform, verified by digest, is what the
installer consumes, and a tarball would add an unpacking step that protects nothing. The build is
reproducible given the same toolchain, commit and `--date`:

```bash
go run ./deploy/release --version v0.1.0 --out /tmp/a --commit "$(git rev-parse --short HEAD)" --date 2026-11-10T00:00:00Z
go run ./deploy/release --version v0.1.0 --out /tmp/b --commit "$(git rev-parse --short HEAD)" --date 2026-11-10T00:00:00Z
diff /tmp/a/v0.1.0/SHA256SUMS /tmp/b/v0.1.0/SHA256SUMS
```

Publishing is a tag push on `main`, after the branch process in `AGENTS.md` §9 has put a tested tree
there: `.github/workflows/release.yaml` builds the directory, runs `sha256sum -c SHA256SUMS`, asserts
`uv.sh` is executable, and attaches everything to the GitHub release, including `uv.sh` — the
documentation's install URL points at `releases/download/<version>/uv.sh`. The workflow marks the
release `prerelease: true`: clearing that flag is the deliberate act that records the production pass of
`docs/IMPLEMENTATION_PLAN.md` M4.3 (a real domain, a real CA, a phone client). A maintainer can do the
same by hand with `gh release create` and the files from `dist/<version>/`; the assets are plain files,
so nothing depends on the workflow.

Before that tag exists, a `release/<version>` branch can be built by hand —
`gh workflow run release.yaml --ref release/v0.1.1 -f publish_prerelease=true` — which attaches the same
six files to a pre-release tagged `pre-v<version>-<run>`. That is how testers get a build without
anything being published from `dev` or from `main`.

CI gates on every push to `main`, `dev` and `release/**`, and on tags and pull requests: `gofmt`,
`go vet`, the unit suite with `-race`, the cross-build, the release tool's own tests, and
`govulncheck`. It never publishes except through the two doors above. The lab suite is the acceptance
gate and is run by hand (§4.3 in `AGENTS.md`); CI does not claim it.

After the push, drop what the build left behind:

```bash
go run ./deploy/clean --dry-run     # what would go, and how much it frees
git push && go run ./deploy/clean   # the habit: the artifacts are spent once they are published
```

It removes the release directory, stray binaries, the local development state and everything in the
scratch directory that is not on its keep list — the Go toolchain, the caches and the lab's notes stay,
so the next build is as fast as this one.

When space is the problem, `--build-cache` is the cheap half: it drops the compiled output (gigabytes
after a release cycle) and keeps the downloaded modules, so the next build recompiles for a few seconds
and downloads nothing. `--module-cache` drops the modules as well and costs a download; `--all` does
both; `--toolchain` removes Go itself.
