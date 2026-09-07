-- 012_proposal_applies_revert.sql
-- M4 Slice 03: apply 审计表加 revert 标记列。v0.2 audit-only — 不撤销实际副作用。
ALTER TABLE proposal_applies ADD COLUMN reverted_at TEXT;
ALTER TABLE proposal_applies ADD COLUMN reverted_by TEXT;