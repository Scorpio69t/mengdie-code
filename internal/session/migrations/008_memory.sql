CREATE TABLE memories (
    rowid          INTEGER PRIMARY KEY,
    id             TEXT NOT NULL UNIQUE,
    claim          TEXT NOT NULL,
    kind           TEXT NOT NULL CHECK (kind IN ('episode', 'fact', 'preference', 'procedure', 'reference')),
    scope_kind     TEXT NOT NULL CHECK (scope_kind IN ('user', 'project', 'branch', 'task')),
    scope_value    TEXT,
    authority      TEXT NOT NULL CHECK (authority IN ('explicit', 'repository', 'verified', 'inferred')),
    source_type    TEXT NOT NULL,
    source_ref     TEXT NOT NULL,
    observed_at    TEXT NOT NULL,
    valid_from     TEXT,
    valid_until    TEXT,
    status         TEXT NOT NULL CHECK (status IN ('proposed', 'active', 'stale', 'disputed', 'superseded', 'archived')),
    confidence     REAL NOT NULL,
    evidence_score REAL NOT NULL DEFAULT 0,
    supersedes     TEXT,
    created_at     TEXT NOT NULL,
    updated_at     TEXT NOT NULL
);

CREATE INDEX idx_memories_scope     ON memories(scope_kind, scope_value, status);
CREATE INDEX idx_memories_authority ON memories(authority, status);
CREATE INDEX idx_memories_validity  ON memories(valid_until) WHERE valid_until IS NOT NULL;

CREATE VIRTUAL TABLE memories_fts USING fts5(
    claim,
    content='memories',
    content_rowid='rowid'
);

CREATE TRIGGER memories_ai AFTER INSERT ON memories BEGIN
    INSERT INTO memories_fts(rowid, claim) VALUES (new.rowid, new.claim);
END;
CREATE TRIGGER memories_ad AFTER DELETE ON memories BEGIN
    INSERT INTO memories_fts(memories_fts, rowid, claim) VALUES('delete', old.rowid, old.claim);
END;
CREATE TRIGGER memories_au AFTER UPDATE ON memories BEGIN
    INSERT INTO memories_fts(memories_fts, rowid, claim) VALUES('delete', old.rowid, old.claim);
    INSERT INTO memories_fts(rowid, claim) VALUES (new.rowid, new.claim);
END;

CREATE TABLE memory_evidence (
    id          TEXT PRIMARY KEY,
    memory_id   TEXT NOT NULL,
    kind        TEXT NOT NULL,
    source_ref  TEXT NOT NULL,
    weight      REAL NOT NULL,
    created_at  TEXT NOT NULL,
    FOREIGN KEY(memory_id) REFERENCES memories(id)
);

CREATE INDEX idx_evidence_memory ON memory_evidence(memory_id, created_at);

CREATE TABLE memory_usage (
    memory_id   TEXT NOT NULL,
    session_id  TEXT NOT NULL,
    recalled_at TEXT NOT NULL,
    outcome     TEXT,
    PRIMARY KEY(memory_id, session_id, recalled_at),
    FOREIGN KEY(memory_id) REFERENCES memories(id)
);

CREATE INDEX idx_usage_recency ON memory_usage(session_id, recalled_at);