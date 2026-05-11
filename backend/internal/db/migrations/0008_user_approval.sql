-- User approval workflow.
--
-- When the marketplace runs in OIDC mode WITHOUT a Google Workspace domain
-- allowlist, new users land in 'pending' state and must be approved by an
-- existing approved user before they can access the system. Existing rows
-- keep working transparently because the default is 'approved'.

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'approved'
        CHECK (status IN ('approved', 'pending', 'rejected'));

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS approved_by UUID REFERENCES users(id) ON DELETE SET NULL;

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS approved_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS users_status_idx ON users(status);
