# When something goes wrong

[English](troubleshooting.md) · [Русский](ru/troubleshooting.md)

Most of the time nothing does: the agent runs once a day, the server checks every node on its own
schedule, and certificates renew themselves. When that stops being true, this page is the order to
look in.

## First three commands

```bash
termit-uv doctor                  # on the node: host and node health, one line per check
termit-uv status                  # identity, certificate, peers, last sync, WARP
termit-mn status                  # on the server: prober, queue, certificate, node table
```

`termit-uv doctor` prints a hint next to every failing check, and `termit-mn uv show <node>` on the server has the
last probe and the last self-check together — which is how you tell "the box is broken" from "the
path to it is broken". For anything else: `journalctl -u termit-uv-agent` on the node,
`journalctl -u termit-mn` on the server.

## Read this before you panic

- **`unknown` is not a failure.** A check that could not run — prober down, DNS broken on the server,
  timeout — changes nothing. Nodes keep their state, peers keep working. A mesh whose checks all say
  `unknown` is a blind mesh, not a dead one: fix the observer.
- **A `pending` node is normal.** In `invite+approval` mode it waits for the operator. It is not
  broken and it does not need reinstalling.
- **`direct` is not an outage.** With WARP enabled and fail-open on (the default), a dead tunnel means
  proxied traffic leaves from your own address. Everything keeps working; the address is just yours.
- **Re-running is safe.** Every install step is idempotent: `termit-uv init` again converges, it does not
  regenerate identities, credentials or certificates.

## Installing the server

| Symptom | Likely cause | What to do |
|---|---|---|
| `termit-mn init` refuses: `domain is required` | no `--domain` | pass the name that resolves to this box |
| `termit-mn serve` exits: `cert: listen on :80` | something else holds port 80 | free it (`ss -lntp`), or set `acme.challenge_listen` and forward the port |
| `termit-mn serve` exits: ACME error | DNS not pointing here yet, or port 80 blocked | check `dig +short mn.example.com`, and that the port is reachable from outside |
| `termit-mn serve` exits: `cannot read the master key` | `/etc/termit/mn/master.key` missing or unreadable | restore it from a backup — without it the registry is useless |
| `termit-mn status` says `cannot reach the server at …` | the service is not running, or the socket moved | `systemctl status termit-mn`, then check `admin_socket` in the config |
| `termit-mn` commands say `an admin token is required` | no token anywhere | export `TERMIT_MN_ADMIN_TOKEN`, or pass `--token` or `--token-file` |
| `termit-mn` refuses the token file: `is mode 0644` | the file is readable by the group or the world | `chmod 600` it — a token file that others can read is a leaked credential |
| `termit-mn` refuses the token file: `owned by uid N` | the file belongs to somebody else, who could rewrite it | `chown` it to the user running the commands (`root` on a server), or create it as that user |
| `termit-mn` refuses `--token` together with `--token-file` | two credentials at once | pass one: an explicit flag is never guessed over another |
| `termit-mn` commands say `the admin token is not valid` | wrong token, or a database restored without its `admin` table | use the token printed by `termit-mn init` |

## Enrolling a node

| Symptom | Likely cause | What to do |
|---|---|---|
| `termit-uv init` stops at `ports` | something already listens on 80/443 | stop it, re-run `termit-uv init` |
| `termit-uv init` stops at `dns` | the record does not resolve here yet | fix DNS; the install itself does not need it, enrolment does |
| `termit-uv init` stops at `certificate` | port 80 unreachable from the internet, or DNS is wrong | check both from outside; `termit-uv init --only certificate` re-runs just this step |
| `termit-uv enroll` → `dns_mismatch` | the domain resolves elsewhere, or the request came through a proxy | fix the A/AAAA record and retry; the server compares the record with the address your request came from |
| `termit-uv enroll` → `invalid_invite` | code mistyped, revoked, expired or used up | `termit-mn invite list` on the server, create a new one |
| `termit-uv enroll` → `domain_taken` | a record with that domain already exists | `termit-mn uv show <domain>`; remove the old record or pick another name |
| `termit-uv enroll` → `rate_limited` | ten failed attempts in fifteen minutes from one address | wait, then retry |
| node stays `pending` | `invite+approval` mode | `termit-mn uv approve <node>` on the server |
| `termit-uv enroll` → TLS error | the server's certificate is not trusted by the node yet | check `termit-uv doctor` → `acme`, and that the node's clock is right |

## Day to day

| Symptom | Likely cause | What to do |
|---|---|---|
| node `unavailable` although a manual check passes | the prober cannot reach it | `termit-mn uv show` for the class; check `prober.xray_binary` and the server's own egress |
| `termit-mn status` says `prober: unavailable — every check reports unknown` | no prober worker could be built | read the reason on the line, fix `prober.xray_binary`, restart `termit-mn`; nodes stay as they were |
| `termit-mn status` shows a growing `queue: N due` | the scheduler is not completing passes | `journalctl -u termit-mn`; a stuck probe shows up here before any node changes state |
| node `blocked` right after an upgrade | the certificate was replaced and the server still pinned the old one | the next sync reports the new leaf; `termit-uv unblock` if it stayed blocked |
| peer list is stale | the daily sync has not run yet (it is once a day, by design) | `termit-uv sync` |
| `termit-uv sync --check` exits 4 | the core's client set differs from the server's | `termit-uv sync` |
| `termit-uv status` shows an old `last sync` | the agent is not running | `systemctl status termit-uv-agent`, `journalctl -u termit-uv-agent` |
| node lost after a reboot | units not enabled | `termit-uv init` re-enables and starts them |

## Certificates

| Symptom | Likely cause | What to do |
|---|---|---|
| `termit-uv doctor` → `certificate` warn/fail | renewal is late or broken | `termit-uv renew-cert`; `journalctl -u termit-uv-renew.service` says why |
| certificate expired | renewal could not reach the CA for a long time | `termit-uv renew-cert --force` after checking DNS and port 80 |
| server certificate expired | the six-hourly renewal kept failing | the journal of `termit-mn` names the ACME error; the old certificate stays in service until a new one is issued, so the fix is the ACME path, not a restart |
| `termit-uv renew-cert` → challenge failed | port 80 not reachable, or DNS moved | fix it, then retry |

## Egress and WARP

| Symptom | Likely cause | What to do |
|---|---|---|
| `termit-uv status` shows `warp: … direct` | WARP is down and fail-open is doing its job | `termit-uv warp status`, then `journalctl -u warp-cli`; traffic still works |
| `termit-uv warp status` shows `not-installed` | the node was installed with `--no-warp`, or the package was removed | `termit-uv init` or `termit-uv warp mode all` installs and registers it |
| `termit-uv warp status` shows `unavailable` | the host cannot run WARP: no `/dev/net/tun`, or an architecture with no package | nothing to fix — the reason is printed; the node keeps working and exits directly |
| `termit-uv warp mode` → `start-limit-hit` | too many restarts in five minutes | `systemctl reset-failed termit-xray termit-uv-agent`, then retry |

## Subscriptions and clients

| Symptom | Likely cause | What to do |
|---|---|---|
| subscription returns `404` | the token was rotated or mistyped | `termit-uv status` shows the current URL; `termit-mn sub rotate` issues a new one |
| client shows old peers | it has not refreshed | force a refresh in the client; the URL carries an `ETag`, so a cached client costs one `304` |
| client connects but no traffic | the peer's credential was rotated, or the node is `blocked` | `termit-mn uv show <node>`; a blocked node is out of every peer set |
| a leaked subscription URL | it is a credential | `termit-mn sub rotate <node> --domain <MN>`; the old URL stops working immediately |

## Getting help

Collect this, and paste it into the issue — it answers nine questions out of ten and contains no
credentials:

```bash
termit-uv version; termit-uv status; termit-uv doctor
journalctl -u termit-uv-agent -n 100 --no-pager
# on the server
termit-mn version; termit-mn status
termit-mn uv show <the node>
journalctl -u termit-mn -n 100 --no-pager
```

Never paste `state.json`, `/etc/termit/uv/config.yaml`, the master key or an admin token: they hold
credentials. If a maintainer asks for the configuration, send the output of
`sed -E 's/(key|token|uuid|secret)[^:]*: .*/\1: <redacted>/I' /etc/termit/uv/config.yaml`.

Next: [How Termit works](overview.md) · [Using a node](using-uv.md) · [Running the server](using-mn.md)
