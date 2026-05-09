ALTER TABLE users ADD COLUMN IF NOT EXISTS api_token TEXT;

UPDATE users
SET api_token = encode(gen_random_bytes(32), 'hex')
WHERE api_token IS NULL;

ALTER TABLE users ALTER COLUMN api_token SET NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS users_api_token_idx ON users(api_token);
