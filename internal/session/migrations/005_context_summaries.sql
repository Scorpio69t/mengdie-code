CREATE TABLE context_summaries (
    id                            TEXT PRIMARY KEY NOT NULL,
    session_id                    TEXT NOT NULL,
    source_start                  INTEGER NOT NULL CHECK (source_start > 0),
    source_end                    INTEGER NOT NULL CHECK (source_end >= source_start),
    run_id                        TEXT NOT NULL,
    command_id                    TEXT NOT NULL,
    summary_text                  BLOB NOT NULL CHECK (length(summary_text) > 0 AND length(summary_text) <= 65536),
    summary_sha256                TEXT NOT NULL CHECK (length(summary_sha256) = 71 AND summary_sha256 LIKE 'sha256:%'),
    generator_model               TEXT NOT NULL CHECK (length(generator_model) > 0 AND length(generator_model) <= 256),
    generator_version             TEXT NOT NULL CHECK (length(generator_version) > 0 AND length(generator_version) <= 128),
    estimated_before              INTEGER NOT NULL CHECK (estimated_before > 0),
    estimated_after_upper_bound   INTEGER NOT NULL CHECK (estimated_after_upper_bound > 0),
    created_at                    TEXT NOT NULL,
    UNIQUE (session_id, source_end),
    FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE,
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE,
    FOREIGN KEY (command_id) REFERENCES commands(id) ON DELETE CASCADE
);

CREATE INDEX context_summaries_session_range
ON context_summaries(session_id, source_end DESC);

CREATE TRIGGER context_summary_identity_insert
BEFORE INSERT ON context_summaries
WHEN NOT EXISTS (
    SELECT 1 FROM runs
    WHERE id=NEW.run_id AND session_id=NEW.session_id AND command_id=NEW.command_id AND status='running'
)
BEGIN
    SELECT RAISE(ABORT, 'context summary identity mismatch');
END;

CREATE TRIGGER context_summary_identity_update
BEFORE UPDATE OF session_id, run_id, command_id ON context_summaries
WHEN NOT EXISTS (
    SELECT 1 FROM runs
    WHERE id=NEW.run_id AND session_id=NEW.session_id AND command_id=NEW.command_id AND status='running'
)
BEGIN
    SELECT RAISE(ABORT, 'context summary identity mismatch');
END;
