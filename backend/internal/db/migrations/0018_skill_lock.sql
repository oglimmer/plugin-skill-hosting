-- Skill lock. A locked skill is withdrawn from every machine-facing surface
-- (internal git, external git mirror, and the MCP server) while staying visible
-- — marked as locked — in the web UI. A lock is applied either by an admin or
-- automatically by the scheduled security audit when a skill's risk score
-- reaches the alert threshold. Only an admin can set or clear a lock.
--
-- locked_at NULL means "not locked"; a non-NULL value is the source of truth.
-- The other lock_* columns are only meaningful while locked_at is set.

ALTER TABLE skills
    -- When the skill was locked. NULL = not locked.
    ADD COLUMN IF NOT EXISTS locked_at TIMESTAMPTZ,
    -- Admin who applied the lock. NULL when the audit applied it automatically.
    ADD COLUMN IF NOT EXISTS locked_by UUID REFERENCES users(id),
    -- How the lock was applied: 'admin' (manual) or 'audit' (auto, over threshold).
    ADD COLUMN IF NOT EXISTS lock_source TEXT,
    -- Human-readable reason shown in the UI (admin note, or the audit summary).
    ADD COLUMN IF NOT EXISTS lock_reason TEXT NOT NULL DEFAULT '',
    -- Set when an admin unlocks a skill that the audit had auto-locked. It tells
    -- a later audit sweep NOT to re-lock the same skill even if it still scores
    -- over the threshold — the admin unlock is treated as an acknowledgement.
    -- Cleared when the skill's content is edited, since that is a new state
    -- worth re-evaluating.
    ADD COLUMN IF NOT EXISTS audit_lock_suppressed BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE skills DROP CONSTRAINT IF EXISTS skills_lock_source_check;
ALTER TABLE skills ADD CONSTRAINT skills_lock_source_check
    CHECK (lock_source IS NULL OR lock_source IN ('admin', 'audit'));
