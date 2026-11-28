-- Schema version 1. It matches docs/DESCRIPTION.md §14; a change here means a new migration file and a
-- bump of store.SchemaVersion, never an edit of this one.

CREATE TABLE IF NOT EXISTS uv (
    id              TEXT PRIMARY KEY,
    label           TEXT NOT NULL DEFAULT '',
    domain          TEXT NOT NULL UNIQUE,
    self_uuid       TEXT NOT NULL,
    probe_uuid      TEXT NOT NULL DEFAULT '',
    profile_json    TEXT NOT NULL DEFAULT '{}',
    state           TEXT NOT NULL,
    state_reason    TEXT NOT NULL DEFAULT '',
    created_at      TEXT NOT NULL,
    approved_at     TEXT,
    last_seen_at    TEXT,
    last_request_ip TEXT NOT NULL DEFAULT '',
    agent_version   TEXT NOT NULL DEFAULT '',
    agent_key_hash  TEXT NOT NULL DEFAULT '',
    agent_key_sealed TEXT NOT NULL DEFAULT '',
    sub_token_hash  TEXT NOT NULL DEFAULT '',
    sub_token_sealed TEXT NOT NULL DEFAULT '',
    failed_cycles   INTEGER NOT NULL DEFAULT 0,
    credential_epoch INTEGER NOT NULL DEFAULT 1,
    invite_id       TEXT NOT NULL DEFAULT '',
    notes           TEXT NOT NULL DEFAULT '',
    next_check_at   TEXT,
    last_check_at   TEXT,
    cert_sha256     TEXT NOT NULL DEFAULT '',
    -- home_mn is the management server that owns this row. Empty means "this one", which is every row
    -- written before the mesh existed; a replica stores the origin's id here and may never write a row
    -- it does not home (docs/IMPLEMENTATION_PLAN.md §5.2.1).
    home_mn         TEXT NOT NULL DEFAULT '',
-- home_term counts home roles: a handoff increments it, so a server that lost a node cannot win a merge
-- with a stale write. 0 is every row that has never been handed over.
    home_term       INTEGER NOT NULL DEFAULT 0,
    -- What this server measured of the node's egress, and what the node said it asks for. Both are
    -- empty until something writes them; a reader treats the empty case as "unknown", never as
    -- "direct".
    egress          TEXT NOT NULL DEFAULT '',
    egress_ip       TEXT NOT NULL DEFAULT '',
    egress_want     TEXT NOT NULL DEFAULT '',
    egress_at       TEXT
);

CREATE INDEX IF NOT EXISTS uv_state ON uv (state);
CREATE INDEX IF NOT EXISTS uv_last_seen ON uv (last_seen_at);
CREATE INDEX IF NOT EXISTS uv_next_check ON uv (next_check_at);

CREATE TABLE IF NOT EXISTS uv_event (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    uv_id     TEXT NOT NULL,
    at        TEXT NOT NULL,
    from_state TEXT NOT NULL,
    to_state  TEXT NOT NULL,
    reason    TEXT NOT NULL DEFAULT '',
    actor     TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS uv_event_uv ON uv_event (uv_id, at DESC);

CREATE TABLE IF NOT EXISTS "check" (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    uv_id       TEXT NOT NULL,
    at          TEXT NOT NULL,
    kind        TEXT NOT NULL,
    ok          INTEGER NOT NULL,
    latency_ms  INTEGER NOT NULL DEFAULT 0,
    error_class TEXT NOT NULL DEFAULT '',
    detail_json TEXT NOT NULL DEFAULT '{}'
);

CREATE INDEX IF NOT EXISTS check_uv_at ON "check" (uv_id, at DESC);
CREATE INDEX IF NOT EXISTS check_at ON "check" (at);

CREATE TABLE IF NOT EXISTS invite (
    id         TEXT PRIMARY KEY,
    code_hash  TEXT NOT NULL UNIQUE,
    created_at TEXT NOT NULL,
    created_by TEXT NOT NULL DEFAULT '',
    expires_at TEXT,
    max_uses   INTEGER NOT NULL DEFAULT 1,
    used_count INTEGER NOT NULL DEFAULT 0,
    note       TEXT NOT NULL DEFAULT '',
    revoked_at TEXT
);

CREATE TABLE IF NOT EXISTS admin (
    id           TEXT PRIMARY KEY,
    name         TEXT NOT NULL,
    token_hash   TEXT NOT NULL UNIQUE,
    created_at   TEXT NOT NULL,
    last_used_at TEXT
);

CREATE TABLE IF NOT EXISTS settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS audit (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    at          TEXT NOT NULL,
    actor       TEXT NOT NULL DEFAULT '',
    action      TEXT NOT NULL,
    target      TEXT NOT NULL DEFAULT '',
    detail_json TEXT NOT NULL DEFAULT '{}'
);

CREATE INDEX IF NOT EXISTS audit_at ON audit (at);

-- What each node reported it serves (proto.Transport). Kept beside the node rather than inside its row
-- so an older build's INSERT list stays valid, and a kind can be dropped without touching the node.
CREATE TABLE IF NOT EXISTS uv_transport (
    uv_id TEXT NOT NULL,
    kind  TEXT NOT NULL,
    -- The renderings of the TLS profile, comma-separated: empty on every other kind, and on a node
    -- that reported none, which is read as the profile's default.
    renderings TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (uv_id, kind)
);

-- The mesh's journal: one append-only log per management server, keyed by that server's id and a
-- sequence that only ever grows. A peer applies it in order and records how far it got; a peer that is
-- new or too far behind starts from a snapshot instead (docs/IMPLEMENTATION_PLAN.md §5.2.1).
CREATE TABLE IF NOT EXISTS replication_event (
    mn_id   TEXT NOT NULL,
    seq     INTEGER NOT NULL,
    at      TEXT NOT NULL,
    kind    TEXT NOT NULL,
    payload BLOB NOT NULL,
    PRIMARY KEY (mn_id, seq)
);

-- How far this server has applied another server's log. Its absence means "nothing applied yet".
CREATE TABLE IF NOT EXISTS replication_cursor (
    mn_id TEXT PRIMARY KEY,
    seq   INTEGER NOT NULL
);

-- A node's self-report that arrived at a replica while its home was unreachable: the home applies it when
-- it is back, so a node talking to the other half of the mesh loses nothing (IMPLEMENTATION_PLAN.md §5.2.1
-- decision 3). One row per node — a newer report replaces an older one, because the report is a complete
-- statement of what the node serves, not an increment.
CREATE TABLE IF NOT EXISTS mesh_report (
    uv_id   TEXT PRIMARY KEY,
    at      TEXT NOT NULL,
    payload BLOB NOT NULL
);