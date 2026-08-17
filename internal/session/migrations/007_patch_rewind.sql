ALTER TABLE patch_entries
ADD COLUMN reverse_inline BLOB;

ALTER TABLE patch_entries
ADD COLUMN reverse_size_bytes INTEGER CHECK (reverse_size_bytes IS NULL OR reverse_size_bytes >= 0);

ALTER TABLE patch_entries
ADD COLUMN reverse_sha256 TEXT CHECK (reverse_sha256 IS NULL OR length(reverse_sha256) = 64);

ALTER TABLE patch_journals
ADD COLUMN rewind_command_id TEXT REFERENCES commands(id) ON DELETE SET NULL;

ALTER TABLE patch_journals
ADD COLUMN rewind_started_at TEXT;

CREATE UNIQUE INDEX patch_journals_rewind_command
ON patch_journals(rewind_command_id)
WHERE rewind_command_id IS NOT NULL;

CREATE TRIGGER patch_journal_rewind_command_update
BEFORE UPDATE OF rewind_command_id ON patch_journals
WHEN NEW.rewind_command_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM commands
    WHERE id=NEW.rewind_command_id
      AND session_id=NEW.session_id
      AND kind='session.rewind'
)
BEGIN
    SELECT RAISE(ABORT, 'patch journal rewind command mismatch');
END;
