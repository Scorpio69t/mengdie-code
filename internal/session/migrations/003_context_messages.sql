CREATE TABLE context_messages (
    id             TEXT PRIMARY KEY NOT NULL,
    session_id     TEXT NOT NULL,
    ordinal        INTEGER NOT NULL CHECK (ordinal > 0),
    run_id         TEXT NOT NULL,
    command_id     TEXT NOT NULL,
    role           TEXT NOT NULL CHECK (role IN ('developer', 'system', 'user', 'assistant', 'tool')),
    completeness   TEXT NOT NULL CHECK (completeness IN ('full', 'sanitized')),
    message_json   BLOB NOT NULL CHECK (length(message_json) > 0 AND length(message_json) <= 1048576),
    message_sha256 TEXT NOT NULL CHECK (length(message_sha256) = 71 AND message_sha256 LIKE 'sha256:%'),
    created_at     TEXT NOT NULL,
    UNIQUE (session_id, ordinal),
    FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE,
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE,
    FOREIGN KEY (command_id) REFERENCES commands(id) ON DELETE CASCADE
);

CREATE INDEX context_messages_session_order ON context_messages(session_id, ordinal);
CREATE INDEX context_messages_run_order ON context_messages(run_id, ordinal);

CREATE TRIGGER context_message_identity_insert
BEFORE INSERT ON context_messages
WHEN NOT EXISTS (
    SELECT 1 FROM runs
    WHERE id=NEW.run_id AND session_id=NEW.session_id AND command_id=NEW.command_id AND status='running'
)
BEGIN
    SELECT RAISE(ABORT, 'context message identity mismatch');
END;

CREATE TRIGGER context_message_identity_update
BEFORE UPDATE OF session_id, run_id, command_id ON context_messages
WHEN NOT EXISTS (
    SELECT 1 FROM runs
    WHERE id=NEW.run_id AND session_id=NEW.session_id AND command_id=NEW.command_id AND status='running'
)
BEGIN
    SELECT RAISE(ABORT, 'context message identity mismatch');
END;
