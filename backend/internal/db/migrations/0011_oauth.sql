-- OAuth 2.1 Authorization Code + PKCE support for the MCP endpoint.

CREATE TABLE IF NOT EXISTS oauth_auth_codes (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code_hash      TEXT NOT NULL UNIQUE,
    user_id        UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    redirect_uri   TEXT NOT NULL,
    code_challenge TEXT NOT NULL,
    expires_at     TIMESTAMPTZ NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS oauth_refresh_tokens (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    token_hash TEXT NOT NULL UNIQUE,
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Transient store for OAuth params while the user authenticates via OIDC.
-- Keyed by the nonce embedded in the OIDC state parameter.
CREATE TABLE IF NOT EXISTS oauth_pending (
    state_key      TEXT PRIMARY KEY,
    redirect_uri   TEXT NOT NULL,
    code_challenge TEXT NOT NULL,
    oauth_state    TEXT NOT NULL,
    expires_at     TIMESTAMPTZ NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
