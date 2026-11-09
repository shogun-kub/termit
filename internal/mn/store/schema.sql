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
    cert_sha256     TEXT NOT NULL DEFAULT ''
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
