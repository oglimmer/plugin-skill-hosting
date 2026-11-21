-- Audit gate: check first, publish after.
--
-- With on-change auditing enabled, a new or changed skill/agent is stored but
-- NOT published until the security audit has passed it. While the check is
-- outstanding the item is "audit pending": git, the external mirror and so
-- Claude Code keep serving the last published copy (or nothing, for a new
-- item). A clean verdict clears the pending state and publishes; a harmful one
-- locks the item; a failed check (e.g. the model API is down) leaves it pending
-- — held — until a retry succeeds or an admin approves it by hand.
--
-- audit_pending_token is NULL when the item is not pending. Every content
-- change writes a fresh token, and a verdict may only clear the state if the
-- token it started with is still current. That stops the verdict for an older
-- version from publishing a newer, unchecked one.

ALTER TABLE skills
    ADD COLUMN IF NOT EXISTS audit_pending_token UUID,
    -- When the current pending state began (for display and retry timing).
    ADD COLUMN IF NOT EXISTS audit_pending_at TIMESTAMPTZ,
    -- Non-empty when the last check attempt failed: the item is held.
    ADD COLUMN IF NOT EXISTS audit_pending_error TEXT NOT NULL DEFAULT '';

ALTER TABLE agents
    ADD COLUMN IF NOT EXISTS audit_pending_token UUID,
    ADD COLUMN IF NOT EXISTS audit_pending_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS audit_pending_error TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS skills_audit_pending_idx ON skills(audit_pending_at)
    WHERE audit_pending_token IS NOT NULL;
CREATE INDEX IF NOT EXISTS agents_audit_pending_idx ON agents(audit_pending_at)
    WHERE audit_pending_token IS NOT NULL;
