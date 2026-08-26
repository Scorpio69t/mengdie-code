-- 011_proposal_applies.sql
-- M4 Slice 02: apply 审计表。每次 Store.Apply 完成一条记录（含失败 / policy 拒绝）。
-- 与 reflection_proposals 一对一 (proposal_id UNIQUE)，保证 idempotent guard。
CREATE TABLE proposal_applies (
    rowid        INTEGER PRIMARY KEY,
    id           TEXT NOT NULL UNIQUE,
    proposal_id  TEXT NOT NULL UNIQUE,
    kind         TEXT NOT NULL,
    target       TEXT NOT NULL,
    result       TEXT NOT NULL,        -- success | failed | denied_by_policy
    error        TEXT,
    applied_at   TEXT NOT NULL,
    patch_id     TEXT,
    FOREIGN KEY (proposal_id) REFERENCES reflection_proposals(id) ON DELETE CASCADE
);

CREATE INDEX idx_proposal_applies_applied_at ON proposal_applies (applied_at DESC);