ALTER TABLE users ALTER COLUMN password_hash DROP NOT NULL;

ALTER TABLE users ADD COLUMN IF NOT EXISTS oidc_issuer TEXT;
ALTER TABLE users ADD COLUMN IF NOT EXISTS oidc_subject TEXT;

CREATE UNIQUE INDEX IF NOT EXISTS users_oidc_idx
    ON users(oidc_issuer, oidc_subject)
    WHERE oidc_subject IS NOT NULL;
