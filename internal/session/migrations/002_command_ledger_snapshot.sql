CREATE TABLE commands (
    id             TEXT PRIMARY KEY NOT NULL,
    session_id     TEXT NOT NULL,
    kind           TEXT NOT NULL,
    payload_json   BLOB NOT NULL,
    payload_sha256 TEXT NOT NULL,
    status         TEXT NOT NULL CHECK (status IN ('accepted', 'running', 'applied', 'rejected', 'failed', 'interrupted')),
    result_seq     INTEGER CHECK (result_seq IS NULL OR result_seq > 0),
    created_at     TEXT NOT NULL,
    updated_at     TEXT NOT NULL,
    FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
);

CREATE INDEX commands_session_created ON commands(session_id, created_at);
CREATE INDEX commands_status_updated ON commands(status, updated_at);
CREATE INDEX runs_command_lookup ON runs(command_id);

CREATE TRIGGER runs_command_reference_insert
BEFORE INSERT ON runs
WHEN NEW.command_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM commands WHERE id=NEW.command_id AND session_id=NEW.session_id
)
BEGIN
    SELECT RAISE(ABORT, 'run command reference mismatch');
END;

CREATE TRIGGER runs_command_reference_update
BEFORE UPDATE OF command_id, session_id ON runs
WHEN NEW.command_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM commands WHERE id=NEW.command_id AND session_id=NEW.session_id
)
BEGIN
    SELECT RAISE(ABORT, 'run command reference mismatch');
END;

CREATE TRIGGER events_command_reference_insert
BEFORE INSERT ON events
WHEN NEW.command_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM runs
    WHERE id=NEW.run_id AND session_id=NEW.session_id AND command_id=NEW.command_id
)
BEGIN
    SELECT RAISE(ABORT, 'event command reference mismatch');
END;

CREATE TABLE snapshots (
    session_id     TEXT PRIMARY KEY NOT NULL,
    through_seq    INTEGER NOT NULL CHECK (through_seq >= 0),
    schema_version INTEGER NOT NULL CHECK (schema_version > 0),
    state_json     BLOB NOT NULL,
    state_sha256   TEXT NOT NULL,
    created_at     TEXT NOT NULL,
    FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
);
