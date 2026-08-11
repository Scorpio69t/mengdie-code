CREATE TABLE artifacts (
    id             TEXT PRIMARY KEY NOT NULL,
    session_id     TEXT NOT NULL,
    run_id         TEXT NOT NULL,
    kind           TEXT NOT NULL,
    mime           TEXT NOT NULL,
    sensitivity    TEXT NOT NULL CHECK (sensitivity IN ('private', 'public', 'metadata')),
    relative_path  TEXT NOT NULL UNIQUE,
    sha256         TEXT NOT NULL CHECK (length(sha256) = 71 AND sha256 LIKE 'sha256:%'),
    size_bytes     INTEGER NOT NULL CHECK (size_bytes >= 0),
    created_at     TEXT NOT NULL,
    expires_at     TEXT,
    deleted_at     TEXT,
    FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE,
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
);

CREATE INDEX artifacts_session_created ON artifacts(session_id, created_at);
CREATE INDEX artifacts_run_created ON artifacts(run_id, created_at);
CREATE INDEX artifacts_live_quota ON artifacts(deleted_at, session_id, size_bytes);

CREATE TRIGGER artifact_identity_insert
BEFORE INSERT ON artifacts
WHEN NOT EXISTS (
    SELECT 1 FROM runs WHERE id=NEW.run_id AND session_id=NEW.session_id
)
BEGIN
    SELECT RAISE(ABORT, 'artifact identity mismatch');
END;

CREATE TRIGGER artifact_identity_update
BEFORE UPDATE OF session_id, run_id ON artifacts
WHEN NOT EXISTS (
    SELECT 1 FROM runs WHERE id=NEW.run_id AND session_id=NEW.session_id
)
BEGIN
    SELECT RAISE(ABORT, 'artifact identity mismatch');
END;

ALTER TABLE context_messages
ADD COLUMN artifact_id TEXT REFERENCES artifacts(id) ON DELETE SET NULL;

CREATE INDEX context_messages_artifact ON context_messages(artifact_id);
