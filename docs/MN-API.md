# The management server's API

This file is the reference for every interface a running `termit-mn` exposes: the node API the agent
calls, the subscription endpoints a client fetches, the two operational endpoints, and the unix-socket
interface `termit-mn` itself uses. It states what is on the wire, what it means, and what it
deliberately does not do.

[`PROTOCOL.md`](PROTOCOL.md) is the narrative — why the credential model is shaped this way, what the
state machine promises, which scenario each endpoint serves. This file is the lookup table. Where the
two disagree, this one describes what the code sends.

---

## 1. Where the API lives

### 1.1 The prefix

Every node-facing endpoint lives under `api_prefix`, a path decided at install time:

```yaml
api_prefix: /6rfjdqfn5nno
```

```bash
curl https://mn.example.com/6rfjdqfn5nno/healthz
```

The prefix exists so that an installed server is not detectable by probing the names everyone uses.
`/api/v1` is the first thing a scanner tries; a server whose API lives at a random path answers 404
there like any other web server, and DPI sees one more HTTPS connection to a host that serves nothing
recognisable. It is the same reasoning as the node's XHTTP path, applied to the control plane.

| | |
|---|---|
| Config key | `api_prefix` in `/etc/termit/mn/config.yaml` |
| Default | `/api/v1`, applied when the key is absent |
| Fresh install | `termit-mn init` generates a random one unless `--api-prefix` is given |
| Shape | one or more path segments, each `A–Z a–z 0–9 - _ . ~`, at most 128 characters, no trailing `/` |
| Not a secret | it is in every node's configuration and in `termit-mn status`; treat it as an identifier, not a credential |

```bash
termit-mn init                               # random: what a real install does
termit-mn init --api-prefix /vpn-control     # a chosen, guessable-but-not-standard path
termit-mn init --api-prefix /api/v1          # keep the discoverable path (tests, compatibility)
```

The generation happens once per install. `init` refuses to move an existing prefix, because the prefix
is half of every node's `mn.url` and moving it would cut the mesh off silently.

### 1.2 The node learns it through its URL

There is no discovery step and no separate configuration key on the node: the prefix is part of the
management server URL a node is given.

```bash
termit-mn init                                 # prints: api prefix: /6rfjdqfn5nno
termit-uv enroll --mn https://mn.example.com/6rfjdqfn5nno --invite CODE-...
```

`mn.url` in `/etc/termit/uv/config.yaml` is that URL, prefix included, and the agent appends its own
paths (`/enroll`, `/agent/sync`, …) to it. A URL with no prefix therefore reaches nothing, and the
agent says so rather than reporting a broken invite:

```
agent: the server has no API at https://mn.example.com/enroll: does the MN URL include the server's api_prefix?
```

### 1.3 The paths

`{prefix}` is `api_prefix` throughout this document.

| Method | Path | Caller | Purpose |
|---|---|---|---|
| `POST` | `{prefix}/enroll` | a node, once | Exchange an invite for credentials |
| `POST` | `{prefix}/agent/sync` | a node, daily | Fetch the complete desired peer set |
| `POST` | `{prefix}/agent/unblock` | a blocked node | Ask to be let back in (§4.2 of `PROTOCOL.md`) |
| `POST` | `{prefix}/agent/relocate` | a moved node | Announce a new host (§4.3) |
| `GET` | `{prefix}/healthz` | monitoring | Liveness and version |
| `GET` | `/sub/{token}` | a client | Peer list, base64 VLESS URIs |
| `GET` | `/sub/{token}/json` | a client | Peer list, sing-box JSON |
| `GET` | `/metrics` | Prometheus, loopback only | Counters and gauges |

Everything else is `404`, including every path under a prefix the server is not serving.

Two paths are deliberately **not** behind the prefix:

- **`/metrics`** is a scrape target. A Prometheus configuration that has to know a random path per
  server is a configuration nobody can keep; the endpoint is refused off loopback instead, which is a
  stronger control than obscurity.
- **`/sub/{token}`** is a URL already written into clients' applications. Moving it would break every
  configured client for a benefit the token already provides: it is a 256-bit capability, not an
  enumerable path.

### 1.4 TLS

The server terminates TLS itself, with a certificate it obtains through ACME (`acme.*`). The prefix
changes the path, never the scheme: `https://` everywhere, HTTP/1.1, no h2 requirement on the API. A
server that cannot serve TLS serves nothing.

---

## 2. Conventions

### 2.1 Bodies

Requests and responses are `application/json; charset=utf-8` unless stated otherwise. Unknown fields in
a request body are rejected (`DisallowUnknownFields`), and request bodies are capped at 64 KiB — an
agent sends a few hundred bytes, and anything larger is not an agent.

### 2.2 Versioning fields

Every success body and every problem body carries `api_version` (`"1"` for this build). It is there so a
client that cannot parse the rest still learns which server it is talking to. The profile a node may
enroll with is `profile` (`"v1"`), and an absent value means the current version — an older agent that
does not send the field is not an incompatible agent.

### 2.3 Errors

Failures use one body shape, modelled on RFC 9457:

```json
{
  "type": "about:blank",
  "title": "Conflict",
  "status": 409,
  "code": "dns_mismatch",
  "detail": "mn.example.com resolves to 198.51.100.7, but the request came from 203.0.113.10",
  "api_version": "1"
}
```

`code` is the stable part: it is what a client branches on. `detail` is for humans, may change between
versions, and never contains a secret.

| Code | Status | Meaning |
|---|---|---|
| `invite_invalid` | 400 | no invite, unknown invite, revoked, or expired |
| `invite_exhausted` | 400 | the invite has no uses left |
| `url_invalid` | 400 | the VLESS URL does not parse, or lacks a required part |
| `domain_invalid` | 400 | the domain is not a usable host name, or does not match its URL |
| `unsupported_profile` | 400 | the node asked for a profile this server does not speak |
| `domain_taken` | 409 | another node already holds this domain |
| `dns_mismatch` | 409 / 503 | DNS does not point at the caller, or no resolver could answer |
| `check_failed` | 502 / 503 | the tunnel did not carry traffic, or could not be checked |
| `pending_approval` | 403 | the node is pending and has not been approved yet |
| `node_inactive` | 403 | the node is removed, and this endpoint no longer serves it |
| `relocate_required` | 409 | the request names a different host; that is `agent/relocate`, not this endpoint |
| `conflict` | 409 | the node's state does not permit this operation |
| `unauthorized` | 401 | no agent key, an unknown key, or a key that does not match this node |
| `rate_limited` | 429 | too many attempts from this address inside the window |
| `internal_error` | 500 | the server could not complete the request; the detail says what failed |

A `503` means *try again*: DNS was unreachable or the prober was down. The client must treat it as
"not now", not as "this node is broken" — that distinction is the whole `unknown ≠ failed` rule.

### 2.4 Authentication

| Caller | Proof | Where |
|---|---|---|
| A node, after enrollment | agent key: 32 random bytes, base64url | `Authorization: Bearer <agent key>` |
| A client fetching a subscription | subscription token | in the path |
| An operator | admin token, over the unix socket only | §6 |
| An enrollment | invite code | in the request body |

Agent keys and subscription tokens are stored as SHA-256 digests; the plaintext exists in the node's
config, in the operator's records, and in one response at issuance. Nothing is ever authenticated by
the node id or the domain: both are public, and the id appears in every subscription.

### 2.5 Rate limits and size limits

| Limit | Value | Applies to |
|---|---|---|
| Enrollment attempts per source address | `enrollment.attempts` (default 10) per `enrollment.window_seconds` (default 900) | `{prefix}/enroll` |
| Subscription requests | 60 per 15 minutes, per token | `/sub/…` |
| Request body | 64 KiB | every POST |
| Connection deadline | 30 s | admin socket |

Exceeding an enrollment limit is `429 rate_limited`. The subscription limit is load control, not
defence: the token cannot be guessed.

---

## 3. The node API

The four endpoints below are all that a node ever calls. Each is idempotent in the sense the state
machine needs: repeating a successful call is harmless, and repeating a failed one is the retry
mechanism.

### 3.1 `POST {prefix}/enroll`

Exchanges an invite code for the node's credentials. Called once per install, and again only after a
rebuild that lost its agent key.

Request:

```json
{
  "invite_code": "ABCD-EFGH-....",
  "domain": "node1.example.com",
  "basic_vless_url": "vless://<uuid>@node1.example.com:443?encryption=none&security=tls&type=xhttp&path=%2Fp%2F&mode=packet-up",
  "agent_version": "termit-uv v0.1.0",
  "profile": "v1",
  "capabilities": []
}
```

| Field | Required | Meaning |
|---|---|---|
| `invite_code` | yes | trimmed and case-folded by the server |
| `domain` | yes | the host name this box serves; must match the URL's host |
| `basic_vless_url` | yes | the endpoint the box is presenting now |
| `agent_version` | no | at most 64 characters; recorded for diagnostics |
| `profile` | no | `v1`; empty means the current version |
| `capabilities` | no | reserved; ignored by this build |

The server checks, in this order: the body parses and its URL is a valid profile; the domain is a
usable host name and is not already registered; the invite exists and has uses left; DNS for the domain
points at the request's source address; the prober fetches the configured probe URL through the
tunnel. Only then does the node exist.

Response `201 Created` — the only place these three secrets are ever returned:

```json
{
  "api_version": "1",
  "node": {"id": "uv_9f3c1a2b4d5e", "state": "active", "domain": "node1.example.com"},
  "agent_key": "...",
  "sub_token": "...",
  "probe_uuid": "...",
  "subscription_url": "https://mn.example.com/sub/...",
  "notice": ""
}
```

`state` is `active` in invite mode, `pending` in `invite+approval` mode. A pending node receives its
credentials too — it needs them to exist before approval — and `notice` says what it is waiting for.
Losing this response means re-enrolling, which the operator does with `termit-mn uv reissue`.

Refusals: `invite_invalid`, `invite_exhausted`, `url_invalid`, `domain_invalid`, `unsupported_profile`,
`domain_taken`, `dns_mismatch`, `check_failed`, `rate_limited`. Note that a DNS or check failure is
**not** `rate_limited`: the two are what the operator most needs to tell apart.

### 3.2 `POST {prefix}/agent/sync`

Returns the complete desired peer set. Called daily per `policy.sync_interval`, and by
`termit-uv sync --check`.

Headers:

| Header | Required | Meaning |
|---|---|---|
| `Authorization` | yes | `Bearer <agent key>` |
| `X-Territ-Cert-SHA256` | no | leaf certificate the node now serves, lowercase hex; the server re-pins its probes to it |

Request:

```json
{
  "agent_version": "termit-uv v0.1.0",
  "check": {"ok": true, "latency_ms": 84, "at": "2026-11-10T18:16:11Z"}
}
```

`check` is the node's own last self-check, folded into the server's history for that node. It is
informational: it never moves a node's state, because a node that cannot reach the internet is exactly
the case where its own report is least trustworthy.

Response `200 OK`:

```json
{
  "api_version": "1",
  "self": {"id": "uv_9f3c1a2b4d5e", "domain": "node1.example.com", "state": "active", "probe_uuid": "..."},
  "peers": [
    {"id": "uv_2b3c...", "domain": "node2.example.com", "label": "node2", "uuid": "...",
     "path": "/p/", "mode": "packet-up", "state": "active"}
  ],
  "subscription_url": "https://mn.example.com/sub/...",
  "policy": {"check_interval_seconds": 86400, "sync_interval_seconds": 86400, "min_sync_delay_seconds": 7200},
  "next_sync_after": "2026-11-11T18:16:11Z"
}
```

Two invariants matter more than the fields:

- **`peers` is the whole set, not a delta.** A node that missed a response converges on the next one.
  `changes` does not exist; a client that needs to know what moved compares the two sets it holds.
- **`uuid` is the pair credential** for this node and that peer, derived from the two ids and this
  node's `credential_epoch`. It is valid on that pair and nowhere else, so a leaked subscription
  exposes one relationship, not the mesh.

`next_sync_after` is a hint, not an instruction: a node that syncs early is welcome, a node that syncs
late is not a crisis. `state` is the *peer's* state; `degraded` is set when a peer is still usable but
currently failing its checks.

Refusals: `unauthorized` (no key, unknown key, key of another node), `pending_approval`,
`node_inactive`, `relocate_required` (the stored domain no longer matches what the node presents).

### 3.3 `POST {prefix}/agent/unblock`

Scenario B: a blocked node asks to be let back in. This is the only agent endpoint that accepts a
blocked node's key — a node with no other way to ask is the point of the scenario.

Request (both fields optional):

```json
{"domain": "node1.example.com", "basic_vless_url": "vless://..."}
```

A node that still has its old identity sends `{}` or omits the body. A rebuilt one sends the URL it now
answers on, and the server stores that endpoint after verifying DNS and the tunnel again.

Responses:

| State | Answer |
|---|---|
| `active` | `200`, `notice: "already active; nothing changed"` — idempotent |
| `blocked`, checks pass | `200`, node is `active`, failed cycles reset |
| `blocked`, checks fail | the refusal from the failed check (`dns_mismatch`, `check_failed`), node stays `blocked` |
| anything else | `409 conflict`: only a blocked node can be unblocked |
| different host in the body | `409 relocate_required`: that is §3.4, not this endpoint |

### 3.4 `POST {prefix}/agent/relocate`

Scenario C: the same node at a new host. The body carries the identity the box now presents, and the
server verifies it against the request's source address before touching the record.

```json
{"domain": "node5.example.com", "basic_vless_url": "vless://..."}
```

`200 OK` with the record after the move, the same `Node` shape as everywhere else. The node id, the
probe credential and every pair credential are keyed by the record, so peers keep working and only the
domain and path change in their next sync.

Refusals: `400 url_invalid` when the body does not parse, lacks a field, or the URL's host and the
`domain` disagree; `409 conflict` when the domain belongs to another node or when the storage rejects
the move; `dns_mismatch` and `check_failed` from the verification the request must pass, with the same
`unknown ≠ failed` rule as everywhere else.

The verify step is the point: a request is not believed because it carries a key. The box must resolve
from the address that sent it, serve the decoy, and answer as a proxy.

---

## 4. Subscriptions

`GET /sub/{token}` returns the peer list as base64-encoded VLESS URIs, one per line before encoding —
the format v2rayNG, NekoBox and friends import.

`GET /sub/{token}/json` returns the same list as a sing-box configuration fragment, `application/json`.

Both:

- answer `404 subscription_not_found` for an unknown, revoked or rotated token — the same answer for
  every failure, so a guesser learns nothing from the difference;
- carry `ETag`, and honour `If-None-Match` with `304 Not Modified`;
- carry `Cache-Control: no-store`: a subscription is a capability, not public content;
- contain only nodes in `active` or `unavailable` state, with the owner's own node carrying its self
  credential and every other node the pair credential the owner shares with it;
- are ordered by label, then id, so the same peers produce byte-identical output and therefore the same
  `ETag`.

A node whose subscription token leaked is rotated with `termit-mn sub rotate <node>`; the old URL stops
working immediately.

---

## 5. Operational endpoints

### 5.1 `GET {prefix}/healthz`

```json
{"api_version": "1", "build": "v0.1.0", "domain": "mn.example.com",
 "schema_version": 5, "status": "ok"}
```

`200` when the database answers a ping inside 2 seconds, `503` with `status: "degraded"` and
`database: "unavailable"` added when it does not. There is no `database` field on the healthy answer:
the absence of the failure field is the success signal. It carries no node data: a health endpoint that leaks the
registry is a health endpoint an attacker polls.

### 5.2 `GET /metrics`

Prometheus text format, refused with `403` unless the request comes from loopback — bind the listener
to a public address and the endpoint is still unreachable from outside.

| Metric | Type | Meaning |
|---|---|---|
| `termit_mn_up` | gauge | the process is serving |
| `termit_mn_build_info` | gauge | labels carry the version and schema version |
| `termit_mn_nodes` | gauge | nodes per state |
| `termit_mn_checks_total` | counter | probes, per result (`ok`/`failed`/`unknown`) |
| `termit_mn_enrollments_total` | counter | enrollment attempts, per result |
| `termit_mn_syncs_total` | counter | syncs served |
| `termit_mn_check_duration_seconds` | histogram | how long a probe takes |
| `termit_mn_check_queue_due` | gauge | checks waiting |
| `termit_mn_check_queue_oldest_due_seconds` | gauge | age of the oldest waiting check |
| `termit_mn_check_next_due_timestamp_seconds` | gauge | when the next check runs |
| `termit_mn_certificate_expiry_timestamp_seconds` | gauge | the server's own certificate |
| `termit_mn_prober_up` | gauge | the prober binary is present and runnable |
| `termit_mn_start_time_seconds` | gauge | process start |

---

## 6. The admin socket

The admin interface is not HTTP and is not exposed on the network. It is a unix socket, created `0600`
and owned by root, so the only way in is to be root on the machine.

| | |
|---|---|
| Path | `admin_socket`, default `/run/termit/mn/admin.sock` |
| Framing | one JSON request per connection, one JSON response, then the server closes |
| Deadline | 30 s |
| Permissions | `0600`, created by the server; a second instance cannot bind it |

Request:

```json
{"token": "<admin token>", "op": "uv.list", "args": null}
```

Response, exactly one of:

```json
{"result": [...]}
```

```json
{"error": {"code": "not_found", "detail": "no such node"}}
```

| Admin code | Meaning |
|---|---|
| `unauthorized` | missing or wrong admin token |
| `bad_request` | arguments missing or malformed |
| `not_found` | no such node, invite or subscription |
| `conflict` | the request contradicts the current state |
| `unavailable` | the server is not able to answer yet (starting, shutting down) |
| `internal_error` | everything else; the detail says what failed |

### 6.1 Operations

`args` is an object; `null` and `{}` are equivalent for the operations that take none. Every operation
is authenticated by the same token, and every mutation is journalled with the actor (`admin`) and the
target.

| Op | Args | Result |
|---|---|---|
| `server.state` | — | domain, listen, version, uptime, schema version, database size, prober state, certificate expiry, counts per state, scheduler queue |
| `invite.list` | — | invites with id, uses left, expiry |
| `invite.create` | `{"uses": n}` (1–100), `{"note": "..."}`, `{"expires_hours": n}` | the invite with its plaintext code, once |
| `invite.revoke` | `{"id": "<invite id>"}` | the revoked invite |
| `uv.list` | — | nodes: id, domain, state, created, last seen |
| `uv.show` | `{"id": "<node id or domain>"}` | the node with its recent history |
| `uv.approve` / `uv.reject` | `{"id": ...}` | the node after the transition |
| `uv.block` / `uv.unblock` | `{"id": ...}` | the node after the transition |
| `uv.rm` | `{"id": ...}` | the node, removed (its records stay for history) |
| `uv.forget` | `{"id": ...}` | confirmation; the record and its history are deleted |
| `uv.reissue` | `{"id": ...}` | a new agent key, shown once; the subscription token is separate |
| `uv.agentkey` | `{"id": ...}` | the stored agent key, decrypted with the master key |
| `sub.rotate` | `{"id": ...}` | a new subscription token, shown once |
| `check.now` | `{"id": ...}` | one cycle for that node: event, state, attempts, reason |
| `policy.show` | — | the current thresholds |
| `policy.set` | `{"key": ..., "value": ...}`; keys are `check_interval` (a Go duration, `24h`) and `probe_retries` (1–10); an empty value clears the override | the value after the change |
| `admin.rotate` | — | a new admin token, shown once; the old one stops working immediately |

`admin.rotate` rotates the credential that authenticated the call and nothing else: a token entitled to
its own access is never entitled to re-issue somebody else's.

The CLI (`termit-mn invite|uv|check|policy|sub|status|admin`) is a thin client of this table; use the
CLI unless you are writing an integration.

---

## 7. Compatibility

- **`api_version` changes only on a breaking change.** Adding a field, a code or an operation is not
  breaking and does not bump it: clients must ignore fields and codes they do not know.
- **A server may serve an older agent.** `profile` absent means the current profile; an unknown profile
  is refused with `unsupported_profile` rather than guessed at.
- **The admin socket is not a public API.** Its operations may be added or reshaped between releases;
  the CLI is the interface that stays put.
- **The prefix is part of the deployment, not of the protocol.** Two servers running different prefixes
  are the same API; changing a running server's prefix is a migration of every node's `mn.url`, not an
  upgrade.
