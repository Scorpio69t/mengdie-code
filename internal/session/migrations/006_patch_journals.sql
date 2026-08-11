CREATE TABLE patch_journals (
    id               TEXT PRIMARY KEY NOT NULL,
    session_id       TEXT NOT NULL,
    run_id           TEXT NOT NULL,
    command_id       TEXT NOT NULL,
    tool_call_id     TEXT NOT NULL,
    tool_name        TEXT NOT NULL CHECK (tool_name IN ('edit_file', 'write_file')),
    call_digest      TEXT NOT NULL CHECK (length(call_digest) = 64),
    status           TEXT NOT NULL CHECK (status IN ('prepared', 'applied', 'verified', 'rewound', 'conflict', 'aborted')),
    conflict_reason  TEXT,
    prepared_at      TEXT NOT NULL,
    applied_at       TEXT,
    verified_at      TEXT,
    resolved_at      TEXT,
    rewound_at       TEXT,
    UNIQUE (run_id, tool_call_id),
    FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE,
    FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE,
    FOREIGN KEY (command_id) REFERENCES commands(id) ON DELETE CASCADE
);

CREATE INDEX patch_journals_session_status
ON patch_journals(session_id, status, prepared_at);

CREATE TABLE patch_entries (
    id                   TEXT PRIMARY KEY NOT NULL,
    journal_id           TEXT NOT NULL,
    ordinal              INTEGER NOT NULL CHECK (ordinal > 0),
    relative_path        TEXT NOT NULL,
    path_fingerprint     TEXT NOT NULL CHECK (length(path_fingerprint) = 64),
    pre_existed          INTEGER NOT NULL CHECK (pre_existed IN (0, 1)),
    pre_sha256           TEXT CHECK (pre_sha256 IS NULL OR length(pre_sha256) = 64),
    pre_mode             INTEGER CHECK (pre_mode IS NULL OR (pre_mode >= 0 AND pre_mode <= 4095)),
    post_existed         INTEGER NOT NULL CHECK (post_existed IN (0, 1)),
    post_sha256          TEXT CHECK (post_sha256 IS NULL OR length(post_sha256) = 64),
    post_mode            INTEGER CHECK (post_mode IS NULL OR (post_mode >= 0 AND post_mode <= 4095)),
    forward_artifact_id  TEXT,
    reverse_artifact_id  TEXT,
    status               TEXT NOT NULL CHECK (status IN ('prepared', 'applied', 'verified', 'rewound', 'conflict', 'aborted')),
    conflict_reason      TEXT,
    applied_at           TEXT,
    verified_at          TEXT,
    resolved_at          TEXT,
    rewound_at           TEXT,
    UNIQUE (journal_id, ordinal),
    CHECK (
        (pre_existed=0 AND pre_sha256 IS NULL AND pre_mode IS NULL) OR
        (pre_existed=1 AND pre_sha256 IS NOT NULL AND pre_mode IS NOT NULL)
    ),
    CHECK (
        (post_existed=0 AND post_sha256 IS NULL AND post_mode IS NULL) OR
        (post_existed=1 AND post_sha256 IS NOT NULL AND post_mode IS NOT NULL)
    ),
    FOREIGN KEY (journal_id) REFERENCES patch_journals(id) ON DELETE CASCADE,
    FOREIGN KEY (forward_artifact_id) REFERENCES artifacts(id) ON DELETE SET NULL,
    FOREIGN KEY (reverse_artifact_id) REFERENCES artifacts(id) ON DELETE SET NULL
);

CREATE INDEX patch_entries_journal_order
ON patch_entries(journal_id, ordinal);

CREATE TRIGGER patch_journal_identity_insert
BEFORE INSERT ON patch_journals
WHEN NOT EXISTS (
    SELECT 1 FROM runs
    WHERE id=NEW.run_id AND session_id=NEW.session_id AND command_id=NEW.command_id AND status='running'
)
BEGIN
    SELECT RAISE(ABORT, 'patch journal identity mismatch');
END;

CREATE TRIGGER patch_journal_identity_update
BEFORE UPDATE OF session_id, run_id, command_id ON patch_journals
WHEN NOT EXISTS (
    SELECT 1 FROM runs
    WHERE id=NEW.run_id AND session_id=NEW.session_id AND command_id=NEW.command_id
)
BEGIN
    SELECT RAISE(ABORT, 'patch journal identity mismatch');
END;
