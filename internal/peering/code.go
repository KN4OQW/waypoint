package peering

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"time"
)

// The pairing short code (RFC-0016 §3). The RFC does not fix the entropy/expiry,
// so this matches the RFC-0002 claim posture: minted from crypto/rand, short-lived,
// single-use. Six digits (~20 bits) is the SAS/numeric-comparison norm; combined
// with a 3-minute expiry, single use, and the online-only channel (the code
// authenticates one live handshake), brute force is infeasible.
const (
	codeDigits = 6
	CodeExpiry = 3 * time.Minute
)

// NewCode returns a fresh 6-digit numeric pairing code (zero-padded), drawn from
// crypto/rand — never math/rand — so it is unpredictable.
func NewCode() (string, error) {
	max := big.NewInt(1)
	for i := 0; i < codeDigits; i++ {
		max.Mul(max, big.NewInt(10))
	}
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		return "", fmt.Errorf("peering: read code: %w", err)
	}
	return fmt.Sprintf("%0*d", codeDigits, n), nil
}

// NewNodeID mints a short, stable node id (8 hex bytes) used as the peer's key in
// the store. It is distinct from the callsign (which may collide) and from the
// cert fingerprint (which changes on re-key).
func NewNodeID() (string, error) { return randomHex(8) }
