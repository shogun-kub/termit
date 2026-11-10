# Termit wire protocol, API v1

This is the contract between a node (`termit-uv`) and the management server (`termit-mn`), plus the
two read-only surfaces the server offers to monitoring and to clients. It describes the bytes this
build actually sends and accepts; [`DESCRIPTION.md`](DESCRIPTION.md) §8 is the design overview, and
where the two differ, this document is what the code does.

Everything here is stable for `api_version` `"1"`. Adding an optional field or a new problem code is
compatible; removing a field, renaming one, changing a status code or tightening a check is not, and
needs a new `api_version`.

| Component | Role |
|---|---|
| UV (node) | calls `/api/v1/enroll` once, then `/api/v1/agent/*` on its daily cycle |
| MN (server) | implements the API, owns the registry and the probe worker |
| Client | fetches `/sub/{token}` or `/sub/{token}/json` (a browser or a proxy app) |
| Monitoring | reads `/healthz`, and `/metrics` from loopback |

---

## 1. Transport

- **HTTPS only**, HTTP/1.1. The server terminates TLS itself with the certificate it issued through
  ACME for its own domain, and refuses to start without one. `mn serve --http` exists for development
  and says so on stderr on every start.
- Requests and responses are JSON (`application/json; charset=utf-8`), errors are
  `application/problem+json; charset=utf-8`.
- Bodies are capped at **64 KiB**. A larger body is refused by the reader, not buffered.
- The server sets `Cache-Control: no-store` on its JSON and problem bodies.
- Request bodies are decoded with **unknown fields rejected**. A body with a field this build does not
  know is a `400`, not a silently ignored value — the same rule the YAML loaders follow. New fields
  therefore have to be optional on the sender's side (`omitempty`) and are only sent when they are
  needed.

## 2. Authentication

| Surface | Credential | Where |
|---|---|---|
| `/api/v1/enroll` | invite code | request body, `invite_code` |
| `/api/v1/agent/*` | agent key | `Authorization: Bearer <agent_key>` |
| `/sub/{token}`, `/sub/{token}/json` | subscription token | the URL path |
| `/metrics` | none; loopback peer only | — |
| `/healthz` | none | — |

The agent key and the subscription token are opaque strings (URL-safe base64, no whitespace). Neither
is ever echoed in a response body after enrollment, written to a log line, or accepted on the other's
endpoint: an agent key presented to `/sub/…` is a 404, a subscription token presented as a bearer
token is a 401.

`Authorization` must be exactly `Bearer ` + the key. Anything else — a lowercase scheme, a missing
space, an empty token — is a `401` with code `unauthorized`.

On the agent endpoints the key is checked **before** the body is read: a wrong key gets `401` even
when the body is malformed, and nothing about the body is echoed back. That is deliberate — an
unauthenticated caller must not be able to make the server parse anything.

**The source address is the TCP peer.** `X-Forwarded-For` and friends are ignored on purpose: the DNS
consistency check (§3.3) exists to prove that the domain resolves to the machine that sent the
request, and a header any client can set would defeat it. Do not put the server behind a reverse
proxy or a CDN that rewrites the peer address; the enrollment check would then compare against the
proxy's address and refuse every node.

## 3. Endpoints

### 3.1 `GET /healthz`

Liveness plus enough identity to answer "which server is this". Never touches a credential.

```json
{
  "status": "ok",
  "api_version": "1",
  "build": "0.1.0",
  "schema_version": 5,
  "domain": "mn.example.com"
}
```

`schema_version` is the **database** schema this build understands, not the configuration's
`schema_version: 1`: it moves when a migration lands. `build` is the binary's version string.

`200` when the database answers. When `Ping` fails the body gains `"status": "degraded"` and
`"database": "unavailable"`, and the status code is `503`.

### 3.2 `GET /metrics`

Prometheus text format, `text/plain; version=0.0.4`. **Loopback only**: a request whose peer is not a
loopback address gets `403` and a one-line explanation. This is the surface a local monitoring agent
reads; it is not exposed to the mesh.

| Family | Type | Meaning |
|---|---|---|
| `termit_mn_up` | gauge | always 1 while the server answers |
| `termit_mn_build_info{version,schema_version}` | gauge | build and database schema identity |
| `termit_mn_start_time_seconds` | gauge | process start, unix seconds |
| `termit_mn_nodes{state}` | gauge | nodes per state, every state present |
| `termit_mn_prober_up` | gauge | 0 means no probe worker: every check is unknown |
| `termit_mn_check_queue_due` | gauge | nodes the scheduler should have checked by now |
| `termit_mn_check_queue_oldest_due_seconds` | gauge | how late the oldest due check is |
| `termit_mn_check_next_due_timestamp_seconds` | gauge | when the next scheduled check is due |
| `termit_mn_certificate_expiry_timestamp_seconds` | gauge | the server's own certificate |
| `termit_mn_enrollments_total{result}` | counter | `ok`, `pending`, `refused` |
| `termit_mn_syncs_total{result}` | counter | `ok`, `refused` |
| `termit_mn_checks_total{result}` | counter | `ok`, `failed`, `unknown`, `error` |
| `termit_mn_check_duration_seconds` | histogram | one scheduler cycle, buckets from 0.1 s to 10 s |

The counters are process-lifetime and reset on restart; the gauges are read from the database at scrape
time. **No label carries a node id, a domain or a token**: the endpoint is scraped by a monitoring
agent, and an inventory does not belong in a metrics store. Ask `mn status` or `mn uv show` for the
per-node view.

A refusal `result` on the enrollment counter counts everything that produced no node — a bad invite, a
taken domain, a failed DNS or probe check, a rate limit. The detail is in the log and in the problem
response, not in the metric.

### 3.3 `POST /api/v1/enroll`

Registers a node. This is the only unauthenticated write endpoint and therefore the only one with a
per-source-address rate limit.

Request:

```json
{
  "invite_code": "ABCD-EFGH-IJKL",
  "domain": "node1.example.net",
  "basic_vless_url": "vless://0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d@node1.example.net:443?encryption=none&security=tls&sni=node1.example.net&type=xhttp&mode=packet-up&path=%2Fk7Qm2%2F#node1",
  "agent_version": "0.1.0",
  "profile": "v1",
  "capabilities": ["warp"]
}
```

| Field | Required | Meaning |
|---|---|---|
| `invite_code` | unless the server is in `open` mode | the code an operator created with `mn invite create` |
| `domain` | yes | the node's name; the server keys the record on it |
| `basic_vless_url` | yes | the node's own profile, as `uv uri` prints it |
| `agent_version` | no, ≤ 64 chars | recorded for the operator |
| `profile` | no, `"v1"` or empty | empty means "the current profile", which is what an older agent sends |
| `capabilities` | no | advisory only; this build does not branch on it |

The domain in `basic_vless_url` must equal `domain`. The URL must satisfy the one profile this build
speaks: `vless`, port `443`, `security=tls`, `type=xhttp`, `mode=packet-up`, a path with a leading and
a trailing slash, a UUID, and no `allowInsecure`. A URL from another profile is refused with
`unsupported_profile`; a malformed one with `url_invalid`.

The server then runs, in this order:

1. body validation and the profile check (`url_invalid`, `unsupported_profile`);
2. domain equality between the body and the URL (`domain_invalid`);
3. invite validation (`invite_invalid`, `invite_exhausted`);
4. domain uniqueness (`domain_taken`, `409`);
5. **DNS consistency** — every resolver that answers must place the domain on the requesting address.
   A resolver that cannot answer is skipped, not counted against the node; when none answers the
   result is `503 dns_mismatch` and the node retries later (`dns_mismatch`);
6. **certificate and tunnel** — the leaf is read from the node and pinned, then a real XHTTP+TLS
   request is made through the tunnel pinned to exactly that leaf. Failure is `502 check_failed`,
   an unavailable prober is `503 check_failed`.

Success is `201 Created` with the three secrets, which are shown **once and never again**:

```json
{
  "api_version": "1",
  "node": {"id": "uv_9f3c1a2b4d5e", "state": "active", "domain": "node1.example.net"},
  "agent_key": "<agent key>",
  "sub_token": "<subscription token>",
  "probe_uuid": "3b7c1f0a-2d4e-4f6a-9c8b-1e2d3f4a5b6c",
  "subscription_url": "/sub/<subscription token>",
  "notice": "enrolled; an operator has to approve this node before it joins the mesh"
}
```

- `state` is `active` in `invite` and `open` mode, `pending` in `invite+approval` mode. `notice`
  explains a non-active state in words that leak nothing (a pending node never learns its queue
  position).
- `subscription_url` is **relative** here; the absolute URL arrives on the first sync (§3.4), built
  from the server's configured domain.
- `probe_uuid` is a second, separate VLESS identity the prober uses. It is not the node's self
  credential and not a pair credential.

Rate limit: `enrollment.attempts` requests per `enrollment.window_seconds` per source address
(default 10 per 900 s). Exceeding it is `429 rate_limited`; the window is the address's, not the
invite's.

### 3.4 `POST /api/v1/agent/sync`

The node's daily call: reports what it is, receives the complete desired peer set.

Headers: `Authorization: Bearer <agent_key>`, and optionally

```
X-Territ-Cert-SHA256: <64 lowercase hex>
```

Request body — both fields optional, and an empty body is valid:

```json
{
  "agent_version": "0.1.0",
  "check": {"ok": true, "error_class": "", "latency_ms": 12, "at": "2026-11-10T04:15:00Z"}
}
```

`check` is the node's own self-check summary. It is stored as a `self` row beside the server's probe
results so an operator can tell "the box is broken" from "the path to it is broken", but **it never
moves the node's state** — only the prober does that.

The certificate header is how the server follows a renewal: the prober pins the leaf, and a node that
renewed would otherwise fail every later check on a mismatch. A valid header replaces the stored pin;
a malformed one is ignored rather than stored, so garbage cannot disable a node's checks.

Response:

```json
{
  "api_version": "1",
  "self": {
    "id": "uv_9f3c1a2b4d5e",
    "domain": "node1.example.net",
    "state": "active",
    "probe_uuid": "3b7c1f0a-2d4e-4f6a-9c8b-1e2d3f4a5b6c"
  },
  "peers": [
    {
      "id": "uv_4c2b8e1f0a9d",
      "domain": "node2.example.net",
      "label": "node2",
      "uuid": "7d1e2f3a-4b5c-4d6e-8f90-a1b2c3d4e5f6",
      "path": "/m3Xr9/",
      "mode": "packet-up",
      "state": "active",
      "degraded": false
    }
  ],
  "subscription_url": "https://mn.example.com/sub/<subscription token>",
  "policy": {
    "check_interval_seconds": 86400,
    "sync_interval_seconds": 86400,
    "min_sync_delay_seconds": 7200
  },
  "next_sync_after": "2026-11-11T04:15:00Z"
}
```

Rules the node may rely on:

- **The peer list is complete, not a delta.** A node that missed ten answers converges from this one.
  It contains every node in `active` or `unavailable`, excluding the asking node itself.
- `uuid` is the **pair credential** for the asking node and that peer, derived from both ids and the
  lower of the two credential epochs. It is valid on that pair and nowhere else. The asking node's own
  entry is absent: it already has its self credential and does not need it twice.
- `degraded: true` marks a peer that is `unavailable`: usable, currently failing its checks. Clients
  label it "(degraded)" instead of hiding it.
- `state` is the peer's state. Only `active` and `unavailable` occur here today; a `pending`, `blocked`
  or `removed` node is not in anyone's set.
- `policy` is the server's timing decision. The node schedules its own next cycle from
  `next_sync_after` plus a stable per-node jitter, and never earlier than
  `min_sync_delay_seconds` after enrollment.
- `subscription_url` is empty when the server cannot open the sealed token (a broken or rotated
  master key); the node then keeps the URL it already had.

A blocked or removed node cannot sync: `403 node_inactive`. A wrong or unknown key is `401
unauthorized` — the same answer, so a guesser learns nothing from the difference.

### 3.5 `POST /api/v1/agent/unblock`

Scenario B: a blocked node asks to be let back in. This is **the only agent endpoint that admits a
blocked node**, because a node that has no other way to ask is the whole point of it.

Request body, both fields optional:

```json
{"domain": "node1.example.net", "basic_vless_url": "vless://..."}
```

- A node that still has its old identity sends `{}`: the server re-runs the checks against the stored
  profile.
- A rebuilt node sends the URL it now answers on; the stored profile and certificate pin are then
  replaced by the verified ones. The node id, the probe credential and every pair credential stay, so
  peers need no change.

The server repeats the enrollment checks (DNS consistency against the requesting address, and the
certificate-tunnel probe) and then:

| Node state | Result |
|---|---|
| `active` | `200`, `notice: "already active; nothing changed"` — idempotent, not an error |
| `blocked` | checks pass → `active`, `200`; checks fail → the problem code below, state unchanged |
| `pending`, `unavailable` | `409 conflict` — only a blocked node can be unblocked; an unavailable one recovers through a normal probe |
| `removed` | `403 node_inactive` |

A request that names a different domain (directly or through the URL) is `409 relocate_required`:
moving a node is Scenario C, not Scenario B. Both the pass and the failure are written to the audit
log as `uv.unblock` / `uv.unblock_failed`.

Response:

```json
{
  "api_version": "1",
  "node": {"id": "uv_9f3c1a2b4d5e", "state": "active", "domain": "node1.example.net"},
  "notice": "the checks passed and the node is active again"
}
```

### 3.6 `POST /api/v1/agent/relocate`

Scenario C: the same node at a new host. Both fields are required.

```json
{"domain": "node5.example.net", "basic_vless_url": "vless://...@node5.example.net:443?..."}
```

Preconditions, in order:

1. the URL's domain equals the request's `domain` (`url_invalid`);
2. the domain differs from the one on record (`conflict`: a rebuild on the same host is
   `uv unblock`);
3. the new domain belongs to no other node (`conflict`);
4. the node's state admits a move — `active`, `unavailable` and `blocked` do; `pending` does not
   (`conflict`: admission is a decision, not a probe), and a removed node never gets here;
5. verification as in §3.5, against the requesting address.

On success the profile, the self UUID and the certificate pin are replaced, a `blocked` or
`unavailable` node becomes `active`, and the response is the record after the move:

```json
{
  "api_version": "1",
  "node": {"id": "uv_9f3c1a2b4d5e", "state": "active", "domain": "node5.example.net"},
  "notice": "this node now answers on node5.example.net; peers pick it up at their next sync"
}
```

### 3.7 `GET /sub/{token}` and `GET /sub/{token}/json`

One node's peer list, fetched by a client. The token is a capability: it names exactly one list, and a
wrong token and an unknown token get the same answer.

**Plain form** (`/sub/{token}`, `text/plain; charset=utf-8`): standard base64 of one `vless://` URI per
line, with a trailing newline. That is the shape every client that takes a "subscription URL" expects.

```
dmxlc3M6Ly8wYTFiMmMzZC00ZTVmLTRhNmItOGM3ZC05ZTBmMWEyYjNjNGRA...
```

**JSON form** (`/sub/{token}/json`, `application/json; charset=utf-8`): a sing-box profile with one
VLESS outbound per peer, a `proxy` selector over them, a `direct` outbound, private address ranges and
`.local`/`.lan` forced direct, and a `termit_subscription_url` field pointing back at the plain form.
The profile routes the mesh's traffic through the tunnel and leaves the local network alone.

Both forms:

- contain the owner's own node with its **self credential**, and every other node with the pair
  credential this owner shares with it — the credential that peer's inbound accepts, and the only one
  this subscription is allowed to expose;
- are ordered by label, then by id, and are **byte-identical for the same peer set**. Deployments
  depend on that: the `ETag` is a digest of the body.
- mark a degraded peer in the label: `node2 (degraded)`.
- carry `ETag: "<32 hex>"` and `Cache-Control: no-store`. A request whose `If-None-Match` equals the
  current tag gets `304 Not Modified` with no body.

| Situation | Answer |
|---|---|
| unknown token | `404`, code `subscription_not_found` |
| more than 60 requests in 15 minutes for one token | `429`, code `rate_limited` |
| rendering failed (a broken peer record) | `500`, code `internal_error` |

The subscription rate limit is load control, not brute-force defence: the token is a 256-bit secret.
A client that refreshes at launch must not see `429`, and one that reconnects in a loop must not look
like an attack to the operator.

## 4. Problem objects and codes

Every error body has the same shape:

```json
{
  "type": "about:blank",
  "title": "Conflict",
  "status": 409,
  "code": "dns_mismatch",
  "detail": "node1.example.net does not resolve to 203.0.113.7",
  "api_version": "1"
}
```

`code` is the contract; `detail` is for a human reading a log and may change. A client must not branch
on `detail`, and must not treat a missing `detail` as a different error.

| Code | Status | Meaning |
|---|---|---|
| `invite_invalid` | 400 | the invite code is unknown, revoked, expired, or missing where one is required |
| `invite_exhausted` | 400 | the invite code has no uses left |
| `url_invalid` | 400 | the body is not the expected JSON, or the URL is not a profile URL. Also used for a malformed `/agent/*` body |
| `domain_invalid` | 400 | the domain in the URL and in the request disagree |
| `domain_taken` | 409 | this domain is already registered |
| `dns_mismatch` | 409, 503 | the domain does not resolve to the requesting address; 503 when no resolver could answer at all |
| `check_failed` | 502, 503 | the certificate or tunnel check failed (502), or the prober could not run (503) |
| `pending_approval` | — | reserved: a pending node learns its state from `node.state` and `notice`, not from an error |
| `rate_limited` | 429 | too many attempts from this address (enrollment) or for this token (subscription) |
| `unsupported_profile` | 400 | the request names a profile this server does not speak |
| `unauthorized` | 401 | missing, malformed or unknown bearer key |
| `conflict` | 409 | the operation does not apply in this node's state |
| `node_inactive` | 403 | this node is blocked or removed and this endpoint is not one it may use |
| `relocate_required` | 409 | the request names another host; use Scenario C |
| `subscription_not_found` | 404 | no subscription for this token |
| `internal_error` | 500 | the server could not complete the operation; nothing was changed |

`503` always means "retry later, the server could not decide" — never a verdict about the node. That
distinction is the protocol's half of the `unknown` invariant: the node's state does not change
because a prober or a resolver failed.

## 5. What may change a node's state

| Endpoint | Allowed transitions |
|---|---|
| enroll | creates the record as `active` or `pending` |
| sync | none — it reports and reads |
| unblock | `blocked → active`, and only after both checks pass |
| relocate | `blocked/unavailable → active`, `active → active` (endpoint only) |
| scheduler probe | `active → unavailable → blocked`, `unavailable → active` |
| admin CLI | `pending → active` (approve), `pending → removed` (reject), any → `blocked`, `unavailable/blocked → active` (unblock), any → `removed` |

The full table, including every path that changes nothing, is in [`DESCRIPTION.md`](DESCRIPTION.md)
§7.2 and is covered by tests in `internal/mn/sched`.

## 6. Versioning

- `api_version` is `"1"` in every response, success or error, so a client that cannot parse a body
  still learns which server it is talking to.
- `profile` is `"v1"`: VLESS + XHTTP (`packet-up`) + TLS on port 443, behind nginx. An empty `profile`
  in a request means "the current one" — an older agent that never sends the field is not an
  incompatible agent.
- New optional request fields must be omittable and must default to the current behaviour. New
  response fields are additive. New problem codes are additive; a client that does not know a code
  must still read `status`.
- A breaking change — removing or renaming a field, changing a status code, making a check stricter —
  bumps `api_version` and needs a migration note in [`OPERATIONS.md`](OPERATIONS.md).

## 7. The admin interface is not part of this protocol

`mn` does not use HTTP. It talks to the running server over a unix socket (`/run/termit/mn/admin.sock`
by default), created `0600`, one JSON request per connection with a 30 s deadline:

```json
{"token": "<admin token>", "op": "uv.list", "args": null}
```

```json
{"result": [{"id": "uv_9f3c1a2b4d5e", "domain": "node1.example.net", "state": "active"}]}
```

or `{"error": {"code": "not_found", "detail": "no such node"}}`. Operations are `invite.*`, `uv.*`,
`check.now`, `policy.*`, `sub.rotate` and `server.state`; `mn help` lists the commands, and every
mutation is audited. Two answers are worth knowing:

- `uv.show` carries the node's recent history (`checks`, newest first: the server's probes and the
  node's own self-reports in one series), which is what tells "the box is broken" from "the path to it
  is broken".
- `server.state` is the dashboard's server block: version, uptime, database size and schema, whether a
  probe worker exists, the scheduler's backlog and the server's certificate expiry. It contains no
  node identifiers.

This surface is for an operator sitting on the server. It has no authentication beyond the socket's
file permissions plus the token, it is not exposed on the network, and its method list is not a
compatibility promise — the CLI in front of it is.

## 8. Worked example

From a node, with the server on `mn.example.com` and a code from `mn invite create`:

```bash
# 1. enroll — the answer carries the only copy of the node's agent key, and the
#    command runs the first sync itself unless --no-sync is given
uv enroll --mn https://mn.example.com --invite ABCD-EFGH-IJKL
# node:      uv_9f3c1a2b4d5e
# domain:    node1.example.net
# state:     pending
# notice:    enrolled; an operator has to approve this node before it joins the mesh
# subscribe: https://mn.example.com/sub/<token>
# credentials are stored in the state file (0600); they are never printed again

# 2. after `mn uv approve uv_9f3c1a2b4d5e`, the daily cycle does the rest
uv sync -v             # -v prints the peer set as well

# 3. what the clients use
uv uri                 # this node's own vless:// URI
mn sub rotate uv_9f3c1a2b4d5e --domain mn.example.com   # invalidate the old token
```

By hand, with `curl` (the node's own profile is what `uv uri` prints):

```bash
curl -sS https://mn.example.com/healthz | jq .

curl -sS -X POST https://mn.example.com/api/v1/enroll \
  -H 'Content-Type: application/json' \
  -d '{"invite_code":"ABCD-EFGH-IJKL","domain":"node1.example.net",
       "basic_vless_url":"vless://…@node1.example.net:443?…","agent_version":"0.1.0","profile":"v1"}'

curl -sS -X POST https://mn.example.com/api/v1/agent/sync \
  -H "Authorization: Bearer $AGENT_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"agent_version":"0.1.0"}'

curl -sS -D - -o /dev/null https://mn.example.com/sub/$SUB_TOKEN
```

## 9. What this protocol deliberately does not carry

- **No secret in a URL except the subscription token**, which is a read-only capability for one
  node's list and can be rotated with `mn sub rotate`.
- **No credentials in a query string, ever.** The agent key travels in a header; the invite code in a
  JSON body. Nothing here ends up in a proxy's access log.
- **No topology the server does not already know.** A sync answer names peers and pair credentials; it
  never reveals another node's agent key, subscription token, certificate pin or probe credential.
- **No node-side decision about admission.** A node can ask (`unblock`, `relocate`); whether it is
  admitted is the server's answer, backed by checks the node cannot fake from outside its own address.
