package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// tokenLen is the raw session token size: 256 bits, per RFC-0002.
const tokenLen = 32

// newSessionToken mints a fresh session token. It returns the raw token (which
// travels to the client in the cookie and is never persisted) and its SHA-256
// hash (which is what the sessions table stores). A store compromise therefore
// hands over hashes, not live tokens — an attacker cannot replay them as cookies.
func newSessionToken() (raw, hash string, err error) {
	b := make([]byte, tokenLen)
	if _, err := rand.Read(b); err != nil {
		return "", "", fmt.Errorf("auth: read token: %w", err)
	}
	raw = base64.RawURLEncoding.EncodeToString(b)
	return raw, hashToken(raw), nil
}

// hashToken returns the at-rest form of a session token: its SHA-256, hex-encoded.
// A fast hash is deliberate — the token is already 256 bits of uniform entropy, so
// it needs no KDF stretching (unlike a low-entropy password), and every request
// hashes it once to look the session up.
func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
