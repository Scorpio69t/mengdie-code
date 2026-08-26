-- 010_reflection_proposals.sql
-- M4 Slice 01: 复盘提案表。Reflect Worker 写的可审核条目；不直接改 AGENTS.md / Skill / memory。
-- 来源：mengdie reflect CLI 触发的 5 阶段流水线（Scan → Extract → Verify → Reflect → Propose）。
CREATE TABLE reflection_proposals (
    rowid        INTEGER PRIMARY KEY,
    id           TEXT NOT NULL UNIQUE,
    kind         TEXT NOT NULL,        -- memory_upgrade | agents_md_revision | skill_draft | obsolete
    title        TEXT NOT NULL,
    body         TEXT NOT NULL,        -- JSON-encoded ProposalBody
    status       TEXT NOT NULL,        -- proposed | approved | rejected
    based_on     TEXT NOT NULL,        -- JSON-encoded list of source ids
    session_id   TEXT,
    confidence   REAL NOT NULL,
    evidence     TEXT NOT NULL,        -- JSON-encoded EvidenceList
    observed_at  TEXT NOT NULL,
    reviewed_at  TEXT,
    reviewer     TEXT,
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL
);

CREATE INDEX idx_proposals_status_observed ON reflection_proposals (status, observed_at DESC);
CREATE INDEX idx_proposals_session ON reflection_proposals (session_id);