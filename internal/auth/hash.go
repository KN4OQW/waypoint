// Package auth implements Waypoint's security posture (RFC-0002): the first-boot
// device-claim state machine, the single admin credential, server-side sessions,
// brute-force damping, and the reset paths. Its store tables live outside the
// config key tree, so the admin credential and sessions never appear on the
// /api/config surface, in the config view, or in an exported profile.
//
// A device is in exactly one of two states, derived from the store: unclaimed
// (no admin credential; meta.claimed_at null) or claimed. The HTTP gate consults
// that state on every request and serves a strict per-state allowlist; everything
// else is denied. See Gate.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters, fixed by RFC-0002. They are persisted with every hash (see
// HashRecord) so a future release can raise the cost — or migrate the KDF — and
// rehash opportunistically on next login, without a breaking migration: a record
// written under old parameters stays verifiable because it carries its own.
const (
	argonTime    = 1         // iterations
	argonMemory  = 64 * 1024 // KiB → 64 MiB
	argonThreads = 4
	argonSaltLen = 16 // bytes
	argonKeyLen  = 32 // bytes
)

// HashRecord is a verifiable password record: the KDF parameter block, the salt,
// and the derived key. It is stored across two columns — Params (the parameter
// block) alongside Hash (salt + digest) — matching RFC-0002's "parameters stored
// alongside the hash." Nothing here is reversible to the password; a store
// compromise yields only material an attacker must brute-force offline.
type HashRecord struct {
	// Params encodes the KDF and its cost, e.g. "argon2id$v=19$m=65536,t=1,p=4".
	Params string
	// Hash is "<base64(salt)>$<base64(digest)>" — the salt and derived key.
	Hash string
}

// HashPassword derives an argon2id HashRecord for password using the RFC-0002
// parameters and a fresh 16-byte random salt.
func HashPassword(password string) (HashRecord, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return HashRecord{}, fmt.Errorf("auth: read salt: %w", err)
	}
	digest := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return HashRecord{
		Params: fmt.Sprintf("argon2id$v=%d$m=%d,t=%d,p=%d", argon2.Version, argonMemory, argonTime, argonThreads),
		Hash:   base64.RawStdEncoding.EncodeToString(salt) + "$" + base64.RawStdEncoding.EncodeToString(digest),
	}, nil
}

// ErrBadHashRecord marks a stored record that cannot be parsed — a corrupt or
// truncated row, treated as a verification failure, never as "any password works."
var ErrBadHashRecord = errors.New("auth: malformed password hash record")

// Verify reports whether password matches the record, in constant time with
// respect to the digest. It re-derives the key under the record's own stored
// parameters, so a record written under a past cost setting still verifies.
func (r HashRecord) Verify(password string) (bool, error) {
	m, t, p, err := parseParams(r.Params)
	if err != nil {
		return false, err
	}
	salt, digest, err := splitHash(r.Hash)
	if err != nil {
		return false, err
	}
	got := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(digest)))
	return subtle.ConstantTimeCompare(got, digest) == 1, nil
}

// parseParams reads "argon2id$v=19$m=65536,t=1,p=4" back into its cost knobs. Only
// argon2id v=19 is accepted; an unknown KDF or version is a bad record, not a
// silent pass. threads is a uint8 to match argon2.IDKey's parameter type.
func parseParams(s string) (memory, time uint32, threads uint8, err error) {
	parts := strings.Split(s, "$")
	if len(parts) != 3 || parts[0] != "argon2id" || parts[1] != fmt.Sprintf("v=%d", argon2.Version) {
		return 0, 0, 0, ErrBadHashRecord
	}
	if n, _ := fmt.Sscanf(parts[2], "m=%d,t=%d,p=%d", &memory, &time, &threads); n != 3 {
		return 0, 0, 0, ErrBadHashRecord
	}
	if memory == 0 || time == 0 || threads == 0 {
		return 0, 0, 0, ErrBadHashRecord
	}
	return memory, time, threads, nil
}

// splitHash decodes the "<base64 salt>$<base64 digest>" pair.
func splitHash(s string) (salt, digest []byte, err error) {
	sp, dp, ok := strings.Cut(s, "$")
	if !ok {
		return nil, nil, ErrBadHashRecord
	}
	if salt, err = base64.RawStdEncoding.DecodeString(sp); err != nil {
		return nil, nil, ErrBadHashRecord
	}
	if digest, err = base64.RawStdEncoding.DecodeString(dp); err != nil {
		return nil, nil, ErrBadHashRecord
	}
	if len(salt) == 0 || len(digest) == 0 {
		return nil, nil, ErrBadHashRecord
	}
	return salt, digest, nil
}
