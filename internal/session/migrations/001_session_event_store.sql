CREATE TABLE sessions (
    id               TEXT PRIMARY KEY NOT NULL,
    project_root     TEXT NOT NULL,
    project_identity TEXT NOT NULL,
    title            TEXT,
    status           TEXT NOT NULL CHECK (status IN ('active', 'completed', 'failed', 'cancelled', 'interrupted')),
    last_seq         INTEGER NOT NULL DEFAULT 0 CHECK (last_seq >= 0),
    snapshot_seq     INTEGER NOT NULL DEFAULT 0 CHECK (snapshot_seq >= 0 AND snapshot_seq <= last_seq),
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL
);

CREATE TABLE runs (
    id               TEXT PRIMARY KEY NOT NULL,
    session_id       TEXT NOT NULL,
    command_id       TEXT,
    status           TEXT NOT NULL CHECK (status IN ('running', 'completed', 'failed', 'cancelled', 'interrupted')),
    provider         TEXT NOT NULL,
    model            TEXT NOT NULL,
    last_run_seq     INTEGER NOT NULL DEFAULT 0 CHECK (last_run_seq >= 0),
    started_at       TEXT NOT NULL,
    finished_at      TEXT,
    FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
);

CREATE TABLE events (
    event_id         TEXT PRIMARY KEY NOT NULL,
    session_id       TEXT NOT NULL,
    session_seq      INTEGER NOT NULL CHECK (session_seq > 0),
    run_id           TEXT NOT NULL,
    run_seq          INTEGER NOT NULL CHECK (run_seq > 0),
    command_id       TEXT,
    kind             TEXT NOT NULL,
    schema_version   INTEGER NOT NULL CHECK (schema_version > 0),
    visibility       TEXT NOT NULL CHECK (visibility IN ('private', 'public', 'metadata')),
    payload_json     BLOB NOT NULL,
    created_at       TEXT NOT NULL,
    UNIQUE (session_id, session_seq),
    UNIQUE (run_id, run_seq),
    FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE,
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);

CREATE INDEX events_session_after_seq ON events(session_id, session_seq);
CREATE INDEX events_run_order ON events(run_id, run_seq);
