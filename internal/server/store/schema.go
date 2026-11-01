package store

// schemaV1 是首个版本的全部表结构。时间统一存 UTC RFC3339Nano 文本；
// 标签/环境变量/审计细节存 JSON 文本；布尔用 0/1。
const schemaV1 = `
CREATE TABLE agents (
    agent_id           TEXT PRIMARY KEY,
    hostname           TEXT NOT NULL,
    labels             TEXT NOT NULL DEFAULT '{}',
    version            TEXT NOT NULL DEFAULT '',
    status             TEXT NOT NULL DEFAULT 'offline',
    certificate_serial TEXT NOT NULL DEFAULT '',
    cert_not_after     TEXT,
    last_heartbeat_at  TEXT,
    created_at         TEXT NOT NULL,
    updated_at         TEXT NOT NULL
);

CREATE TABLE enrollment_tokens (
    id            TEXT PRIMARY KEY,
    token_hash    TEXT NOT NULL UNIQUE,
    description   TEXT NOT NULL DEFAULT '',
    created_by    TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL,
    expires_at    TEXT NOT NULL,
    revoked_at    TEXT,
    last_used_at  TEXT
);

CREATE TABLE jobs (
    job_id          TEXT PRIMARY KEY,
    agent_id        TEXT NOT NULL REFERENCES agents(agent_id),
    kind            TEXT NOT NULL DEFAULT 'command',
    command         TEXT NOT NULL DEFAULT '',
    working_dir     TEXT NOT NULL DEFAULT '',
    extra_env       TEXT NOT NULL DEFAULT '{}',
    timeout_seconds INTEGER NOT NULL DEFAULT 300,
    requested_by    TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL,
    exit_code       INTEGER,
    truncated       INTEGER NOT NULL DEFAULT 0,
    message         TEXT NOT NULL DEFAULT '',
    created_at      TEXT NOT NULL,
    started_at      TEXT,
    finished_at     TEXT,
    updated_at      TEXT NOT NULL
);
CREATE INDEX idx_jobs_agent_status ON jobs(agent_id, status);
CREATE INDEX idx_jobs_status_created ON jobs(status, created_at);

CREATE TABLE job_output (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    job_id  TEXT NOT NULL REFERENCES jobs(job_id) ON DELETE CASCADE,
    seq     INTEGER NOT NULL,
    stream  TEXT NOT NULL,
    data    BLOB NOT NULL,
    UNIQUE(job_id, seq)
);

CREATE TABLE audit_logs (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    at       TEXT NOT NULL,
    actor    TEXT NOT NULL,
    action   TEXT NOT NULL,
    agent_id TEXT,
    job_id   TEXT,
    detail   TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX idx_audit_at ON audit_logs(at);
CREATE INDEX idx_audit_action ON audit_logs(action);

CREATE TABLE releases (
    version      TEXT PRIMARY KEY,
    size         INTEGER NOT NULL,
    sha256_hex   TEXT NOT NULL,
    signature    BLOB NOT NULL,
    uploaded_by  TEXT NOT NULL DEFAULT '',
    uploaded_at  TEXT NOT NULL
);
`
