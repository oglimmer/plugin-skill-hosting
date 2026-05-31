package server

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"log"
)

// apiTokenForDisplay best-effort decrypts a stored ciphertext so the UI can
// show the raw API token. Returns "" when the column is null/empty or the
// ciphertext can't be decrypted (e.g. the key was rotated) — authentication
// never relies on this, so a display miss is harmless and the user can
// regenerate. Used by every read path that returns a *User to the frontend.
func (a *App) apiTokenForDisplay(enc sql.NullString) string {
	if !enc.Valid || enc.String == "" {
		return ""
	}
	tok, err := a.decryptAPIToken(enc.String)
	if err != nil {
		return ""
	}
	return tok
}

// encryptAPIToken seals a raw API token with AES-256-GCM for storage at rest.
// Output is base64url(nonce || ciphertext). Authentication never depends on
// this — lookups use a separate SHA-256 hash (see userByAPIToken) — so this is
// purely so the UI can re-display the token (see userByID / handleLogin).
func (a *App) encryptAPIToken(plaintext string) (string, error) {
	block, err := aes.NewCipher(a.Cfg.APITokenKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

// decryptAPIToken reverses encryptAPIToken. It returns an error when the
// ciphertext is malformed or was sealed under a different key (e.g. JWT_SECRET
// was rotated with no dedicated API_TOKEN_ENC_KEY set). Callers treat that as
// "token not displayable" — never as an authentication failure.
func (a *App) decryptAPIToken(encoded string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(a.Cfg.APITokenKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("ciphertext too short")
	}
	nonce, ct := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", err
	}
	return string(pt), nil
}

// BackfillAPITokenCiphertext encrypts and stores api_token_enc for rows carried
// over from the plaintext era — the SQL migration set api_token_hash, but only
// the app holds the key needed for the ciphertext. After encrypting a row it
// clears the plaintext api_token, so once this completes no usable token
// remains at rest. Idempotent: it only touches rows still missing ciphertext,
// so it is safe to run on every boot and a no-op once the fleet has migrated.
func (a *App) BackfillAPITokenCiphertext(ctx context.Context) {
	rows, err := a.DB.QueryContext(ctx,
		`SELECT id, api_token FROM users WHERE api_token IS NOT NULL AND api_token_enc IS NULL`)
	if err != nil {
		log.Printf("ERROR: api token backfill query: %v", err)
		return
	}
	// Collect first, then update — the pgx pool runs in single-statement exec
	// mode, so we avoid issuing UPDATEs while the SELECT's rows are still open.
	type row struct{ id, tok string }
	var pending []row
	for rows.Next() {
		var rw row
		if err := rows.Scan(&rw.id, &rw.tok); err != nil {
			log.Printf("ERROR: api token backfill scan: %v", err)
			rows.Close()
			return
		}
		pending = append(pending, rw)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		log.Printf("ERROR: api token backfill rows: %v", err)
		return
	}

	encrypted := 0
	for _, rw := range pending {
		enc, err := a.encryptAPIToken(rw.tok)
		if err != nil {
			log.Printf("ERROR: api token backfill encrypt (user %s): %v", rw.id, err)
			continue
		}
		if _, err := a.DB.ExecContext(ctx,
			`UPDATE users
			 SET api_token_enc = $1,
			     api_token_hash = COALESCE(api_token_hash, $2),
			     api_token = NULL
			 WHERE id = $3`,
			enc, sha256hex(rw.tok), rw.id,
		); err != nil {
			log.Printf("ERROR: api token backfill update (user %s): %v", rw.id, err)
			continue
		}
		encrypted++
	}
	if encrypted > 0 {
		log.Printf("api token backfill: encrypted %d token(s) and cleared their plaintext", encrypted)
	}
}
