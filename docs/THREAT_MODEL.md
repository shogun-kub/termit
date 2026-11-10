# Termit threat model

What this system tries to protect, from whom, and what is left over when the mitigations have done
their work. The design is in [`DESCRIPTION.md`](DESCRIPTION.md), the operational procedures in
[`OPERATIONS.md`](OPERATIONS.md), the wire contract in [`PROTOCOL.md`](PROTOCOL.md).

The document is deliberately unflattering. A threat model that lists only what the system does well
is a marketing page; the useful part is §6, where the residual risks are.

## 1. What Termit is, in one threat-model sentence

A small group of people each run a public proxy endpoint on their own VPS, a server they trust decides
which endpoints are healthy and hands each owner one credential per peer, and clients use those to
reach the open internet through a machine that is not theirs.

That sentence already contains the two facts everything below follows from:

- **the node operator can see where clients' traffic goes** (it is a proxy; that is what a proxy does),
- **the management server can mint every credential in the mesh** (that is what makes the mesh
  self-healing, and it is also the crown jewel).

## 2. Assets

| # | Asset | Why it matters |
|---|---|---|
| A1 | The destination and metadata of a client's traffic | the thing the client is actually paying for: which site, when, how much |
| A2 | The node's own public address | if a destination learns it, the node can be blocked, and the client's traffic is attributable to its owner |
| A3 | The master key (`/etc/termit/mn/master.key`) | derives **every pair credential in the mesh**; with it, an attacker can impersonate any client to any peer |
| A4 | Agent key and subscription token of one node | access to the mesh as that owner: every peer's credential is derivable from them |
| A5 | Invite codes | admission to the mesh: one code with uses left is one new node |
| A6 | Admin token | control of the registry: block, remove, reissue, relocate, read keys |
| A7 | The decoy site's credibility | the disguise that keeps an active prober from classifying the endpoint as a proxy |
| A8 | The mesh inventory (who is in it, on what domain) | the list an adversary needs to block the mesh |
| A9 | Node TLS keys | impersonation of a node; without the key the decoy cannot be served convincingly |
| A10 | The audit log | the only evidence after an incident |

Credentials are pairwise (DESCRIPTION §6): the credential client `O` uses towards node `X` is derived
from both ids and the lower credential epoch. `O`'s credential towards `Y` is unrelated. That is a
privacy property as well as a containment property: nodes cannot collude to correlate one client's
traffic across the mesh by matching credential values.

## 3. Adversaries

| # | Adversary | Capability | Goal |
|---|---|---|---|
| S1 | Network observer on the client's path (ISP, Wi-Fi, hostel) | sees IPs, ports, timing, SNI and volume; cannot break TLS | learn which sites the client visits, block the proxy |
| S2 | Network observer on the node's path | the same, on the egress side | the same, plus learning the node's traffic profile |
| S3 | Active prober (a blocklist operator) | connects to the node on 80/443 with arbitrary requests, TLS fingerprints, HTTP shapes; retries forever | decide "this is a proxy" and block it |
| S4 | Application on the client's device | makes its own outbound requests, asks third parties "what is my IP" | fingerprint or geolocate the user by the address they appear to come from |
| S5 | Another participant (a node owner) | full control of their node: its files, its traffic, its logs; holds their own credentials | see peers' traffic, escalate to the master key, or evade being removed |
| S6 | Malware on a client device | reads the client's files, including the subscription URL and any stored config | use the mesh at the owner's expense, or unmask the owner |
| S7 | Network attacker between node and MN | drops, delays, replays, MITMs the API | make a healthy node look broken, or learn a credential |
| S8 | Holder of a stolen database copy or a decommissioned disk | reads `termit.db` offline | derive pair credentials, guess invite codes, take over the registry |
| S9 | Root on the MN (compromise or hostile operator) | everything on the server: master key, database, admin socket, process memory | the whole mesh |
| S10 | Root on a node, or the hosting provider | the node's state file, certificate keys, agent key, traffic | that node's identity and traffic, and its peers' inventory |
| S11 | Holder of one leaked credential | a subscription URL or an agent key | use the mesh, enumerate it |
| S12 | Global passive adversary | traffic correlation across many vantage points and time | associate client and node despite the proxy |
| S13 | Legal compulsion of a node operator | demands logs and keys | the operator's own view |

Out of scope by non-goal (DESCRIPTION §3): this is not a censorship circumvention network for
strangers, not an anonymity system, and not a hiding place from a determined state-level adversary
with global visibility.

## 4. Trust boundaries

```
   client device         untrusted network          node (own VPS)              MN
  ┌────────────┐   TLS 1.3    ┌─────┐   ┌──────────────────────────┐   ┌─────────────────────┐
  │ client app │─────────────▶│ S1  │──▶│ nginx :443 (decoy, ACME) │   │ registry + master key│
  │            │              └─────┘   │   └─ secret path ─▶ core │   │ admin socket (0600) │
  └────────────┘                        │        │                 │   └──────────┬──────────┘
        │                               │        ▼                 │              │
        │ subscription (S11)            │  WARP sidecar ─▶ internet │◀── sync ─────┘
        ▼                               └──────────────────────────┘   HTTPS, agent key
   MN /sub/<token>                                 │
                                                   ▼
                                          destination (S2, S4)
```

Trust assertions, each of which a review should check rather than assume:

1. **The client trusts its own node operator with its traffic metadata.** Inherent to a proxy; the
   mesh narrows it (the client can pick a peer) but cannot remove it.
2. **A node trusts the MN's answer about who is a peer.** It has no other source of truth; a
   compromised MN can add itself as a peer and receive the node's traffic.
3. **The MN trusts nothing a node says about itself.** State changes come from the prober, from
   operator commands, and from checks that require the node to be reachable from the MN's own address.
4. **A node never trusts another node.** Pair credentials are derived by the MN and validated by the
   receiving node's own config; there is no peer-to-peer control channel.

## 5. Threats and mitigations

Each row states the mitigation **as implemented**, then what is left.

### 5.1 The disguise (S1, S3)

| Threat | Mitigation | Residual |
|---|---|---|
| A prober requests the node and sees something that is obviously not a website | nginx terminates TLS with a real certificate, serves a deterministic generated site with plausible headers, robots.txt, favicon and a POST endpoint that answers a plausible error; the proxy lives behind one secret path and is never reachable without it | the site is generated, so it is *plausible*, not unique; a determined prober can correlate the same template across nodes |
| A prober probes the secret path with a wrong credential | nginx proxies that path to the core, which refuses the request; the rest of the site is untouched | **this is the sharpest fingerprint in the system**: an unknown path gets the decoy, the known path with a bad UUID gets a core-level failure (a reset, or nginx's `502`) instead of a page. Mitigated only by keeping the path random and secret; not eliminated |
| TLS fingerprinting | stock Debian nginx, HTTP/2, ordinary ALPN and cipher order; no custom TLS stack | the certificate's issue pattern and the certificate transparency log still say "an ACME certificate for this name", like millions of other sites |
| Blocklists by IP reputation | WARP puts a Cloudflare address in front of the node's egress | Cloudflare's ranges are themselves blocked in some networks |
| A prober asks the node "what is my address" | the answer is WARP's, not the node's (default `warp.mode: all`) | with `fail_open: true` a dead WARP answers with the node's address; the owner chooses that trade (§6) |

Active probing is the threat this feature set exists for, and it is also the one where the residual
risk is largest: a proxy cannot be made indistinguishable from a website by configuration alone. The
honest claim is "expensive to classify", not "undetectable".

### 5.2 Traffic in transit (S1, S2, S12)

| Threat | Mitigation | Residual |
|---|---|---|
| Reading client traffic | TLS 1.3 from the client to the node's nginx; the core speaks plaintext only on loopback, behind that TLS | the node operator sees the plaintext of the tunnel's payload; that is the proxy's normal trust boundary |
| Learning destinations from DNS | with WARP on, the domain is forwarded to the sidecar unresolved, so the lookup happens inside the tunnel | with WARP off or failing open, the node resolves destinations with its own resolver; the node's resolver (and its provider) sees the names |
| Correlating client and node | no padding, no cover traffic, no multihop | traffic analysis on volume and timing is possible for an observer who sees both ends. Out of scope |
| Replay or tampering with the API | TLS 1.3 between node and MN; no separate message signing | a party who can break TLS sees the API. Nothing is added on top |

Nothing in this build obfuscates the transport beyond TLS: XHTTP rides inside an ordinary HTTP/2
request, which is a real improvement over a custom protocol on the wire, and it is not a defence
against a global observer.

### 5.3 Credentials (S8, S11)

| Threat | Mitigation | Residual |
|---|---|---|
| A stolen database yields credentials | agent keys, subscription tokens, invite codes and admin tokens are stored only as SHA-256 digests (`secret.Digest`), and the values are 256-bit random, so there is nothing to brute-force; the token that must be readable again (the subscription URL) is AES-GCM sealed under a key derived from the master key | if the master key is stolen **with** the database, everything sealed or derivable is exposed — that is the same as S9 |
| An invite code is guessed | 20 random bytes in base32, single-use by default, optional expiry, revocable, and the only unauthenticated endpoint is rate-limited per source address | an operator who hands out a code with 100 uses has effectively published it |
| A subscription URL leaks | it names one node's list and nothing else; `termit-mn sub rotate` replaces it and the old URL stops working | until rotated, whoever holds it can use the mesh as that owner |
| An agent key leaks | it authenticates one node; `termit-mn uv reissue` replaces it, `termit-mn uv block` stops it from syncing | a leaked key gives the holder the node's peer set **and the pair credentials in it**, i.e. working access to the mesh as that owner. Treat an agent key like a password |
| Credentials are compared in a way that leaks timing | lookup is by digest through a database index, so there is no byte-by-byte comparison to observe; matching a digest requires the full 256-bit value | — |
| Credentials appear in logs or errors | log lines carry node ids, domains and state — never keys, tokens, UUIDs or certificate contents; error texts say "the agent key is not valid", not what was wrong with it; `termit-uv status --json` prints fingerprints | a crash dump or a debugging session that prints memory can still contain them; root on the box is game over (§5.5) |

Pairwise derivation (DESCRIPTION §6.2) is the reason a leaked client credential does not become a
mesh-wide credential: it is valid on one pair, and it is derived from ids the holder cannot choose.
It is also why **the master key is the highest-value object in the system** — it reconstructs every
pair credential without touching a database.

### 5.4 The management server (S7, S8, S9)

| Threat | Mitigation | Residual |
|---|---|---|
| An attacker reaches the admin interface over the network | there is no network admin interface: admin operations are a JSON protocol on a unix socket, `0600`, directory `0750`, for a process on the same host | anything that can run as root on the server is inside |
| A stolen admin token | tokens are 256-bit random, hashed, and every mutation is audited with the actor and the target | there is no rotation command: a leaked admin token is removed by hand (`OPERATIONS.md` §12) |
| The prober is used as a weapon | a prober failure is `unknown`, never `failed`; a DNS resolver that cannot answer is skipped, not counted against the node; only a genuine negative result moves a state, and only after the configured number of retries | a compromised server can of course lie in the other direction — the admission policy is cooperative |
| A node's own report is believed | the sync body's `check` is stored beside the probe and never moves a state; the server's verdict comes from its own probe, from its own address, pinned to the leaf it read | a node that also controls the network path to the server could satisfy both; that is a compromised node, not a protocol flaw |
| An attacker forces re-enrollment of a domain | the domain is unique, the invite is consumed transactionally, and a change of identity requires either an unblock/relocate that passes the same checks or an operator command | an operator can always be socially engineered |
| SQLite is the single point of failure | the database is opened read-only for backups (`VACUUM INTO`), migrations are forward-only, and `termit-mn backup` captures database, configuration, certificate and master key | a corrupt database without a backup loses the mesh's credentials (§5.6) |

### 5.5 The endpoints (S5, S9, S10)

| Threat | Mitigation | Residual |
|---|---|---|
| A node owner reads another participant's credentials | the node's config contains only the pair credentials other owners use **towards it**, plus its own; it cannot derive anybody else's | the node sees that a given pair credential is in use, and it sees the traffic that credential carries |
| A node owner abuses peers' traffic | the client's traffic goes where the client asked; the mesh is not a monitoring system | an operator sees destinations of traffic that traverses their node. Stated here so nobody is surprised |
| A node owner stays in the mesh after being removed | the server refuses to sync or admit a removed node (`403 node_inactive`) and stops issuing its pair credentials into other nodes' sets | a removed node that still knows the exit's IP can keep using the proxy until the exit's config changes; `termit-uv sync` on the exit removes the credential, but blocking is cooperative (§6) |
| The agent runs as root | it needs root for two narrow things: replacing the core's config (`0640 root:termit`) and asking systemd to restart the core. Its unit confines writes to the config and state directories, drops `ProtectHome` only when a configured CA file demands it, and sets `NoNewPrivileges` and `ProtectSystem=strict` | root in the process can still rewrite what it is allowed to write; a compromise of the agent is a compromise of the node |
| The core is exploited | it runs as `termit` with an empty capability set, `ProtectSystem=strict`, `SystemCallFilter=@system-service`, `MemoryDenyWriteExecute`, on loopback only | a memory-safety bug in Xray-core would still be remote code execution as an unprivileged user |

### 5.6 Loss scenarios

| Scenario | Consequence | Mitigation |
|---|---|---|
| The MN's database is lost, the master key survives | the registry is gone, credentials can be re-derived, but the records (ids) are not | restore from backup; re-enroll if there is none |
| The MN's master key is lost | every pair credential is unrecoverable — the mesh can only be rebuilt by re-enrolling every node | the key is in the backup; `termit-mn init` prints its fingerprint so a restore can be checked |
| A node's state file is lost | the node cannot authenticate; it is a rebuilt box | `termit-uv init --relocate --agent-key-file` + `termit-uv migrate --relocate` (Scenario C) |
| A node's certificate key leaks | impersonation of that node's decoy | `termit-uv renew-cert --force` with a new key; the server follows the new pin at the next sync |
| An operator deletes the audit log | evidence is gone | retention is a year; the log lives in the same database as the registry, so an attacker with write access can remove it |

## 6. Residual risk and known limitations

These are decisions, not bugs, and each one is a sentence an operator should be able to repeat:

1. **Blocking is cooperative.** The server decides a node is `blocked` and stops handing its
   credential to peers; it cannot stop a node from running, and it cannot reach into one. A node that
   ignores the server keeps serving whoever still has a credential until the next sync of each peer.
2. **The node operator sees destinations.** Traffic through a node is visible to that node's operator
   as destinations, timing and volume, whatever the TLS inside. WARP hides the node's own address from
   the destination; it does not hide the destination from the node, and it does not hide anything from
   Cloudflare, which now sees the destinations instead.
3. **`fail_open: true` is a deliberate exposure.** When WARP is unhealthy and the owner chose
   fail-open (the default), traffic exits from the node's own address. An owner who would rather lose
   connectivity sets `warp.fail_open: false` and gets a broken proxy instead of a revealed one.
4. **The secret path is a fingerprint.** An unknown path answers the decoy; the known path with a
   wrong credential answers like a proxy. Keeping the path random and secret is the mitigation, and it
   is not a proof.
5. **The decoy is a disguise, not a control.** It is deterministic per install and self-contained, so
   it never phones home, but it is not unique and it is not a security boundary.
6. **The MN is a single point of trust.** It can mint any credential, so a compromised MN is a
   compromised mesh. There is no threshold scheme, no second operator, no HSM. The compensating
   measures are its small surface (no network admin API, one socket), its audit log, and the fact that
   the master key can live outside the database.
7. **No anonymity.** Pair credentials limit linkability across nodes and nothing else. A global
   observer, or the destination itself, can still correlate. Termit is a cooperative proxy for a group
   that already trusts each other enough to run servers for each other, not a mixnet.
8. **Availability is not defended.** Rate limits bound enrollment and subscription traffic; nothing
   bounds a bandwidth flood, a provider null-route, or a domain being blocked.
9. **Compromised endpoints are out of scope.** Root on a client, on a node, or on the server defeats
   every control above, by definition.
10. **The system is new.** The threat model is reviewed with the code it describes; the parts with
    real-world evidence behind them are listed in §7. Anything not listed there is reasoning, not
    measurement.

## 7. What is verified, and how

| Claim | Evidence |
|---|---|
| a failed check never changes a node's state | `internal/mn/sched` transition table tests, including every `unknown` path; lab drill with a stopped prober |
| a prober outage is not a node failure | lab failure drills (M3.6): broken prober, wrong DNS answer, server outage |
| enrollment requires the domain to point at the caller | enrollment tests with faking resolvers; Scenario C drill in the lab |
| credentials are stored as digests, tokens sealed | `internal/mn/secret` tests; schema inspection |
| the core config is validated before it is applied | `xray -test` gate in the install step and in the agent's reconciliation; golden tests; lab |
| direct traffic is never captured | lab: an SSH session stays open through install, sync, WARP toggle and certificate renewal; no routing or firewall code exists to run |
| WARP fail-open keeps traffic flowing with a dead sidecar | `test/lab/warp_test.go`: a real client fetches the Cloudflare trace through the node with nothing listening on the sidecar port and gets `warp=off` |
| the admin surface is a `0600` unix socket | `cmd/mn/socket_linux.go` creates the socket `0600` in a `0750` directory; `termit-mn` is the only client |
| the decoy is self-contained | decoy golden tests: no external hosts in the generated tree |

Not verified yet, and therefore not claimed: the WARP tunnel itself (needs `/dev/net/tun`), a real
Let's Encrypt issuance (the lab uses an ACME test server), and any form of resistance to an adversary
who can see both ends of a connection.
