// Package minisign verifies minisign signatures (Ed25519) — the tamper-evidence
// primitive Waypoint uses for both signed releases and verified reference-data
// downloads (RFC-0013 / issue #12). minisign is a tiny, well-specified format with
// no PKI: a 32-byte Ed25519 public key an operator can pin and offline-verify.
// This package is verify-only (signing is done by the `minisign` CLI in CI).
//
// It supports both signature modes: legacy "Ed" (Ed25519 over the raw file) and
// modern prehashed "ED" (Ed25519 over BLAKE2b-512 of the file). It also verifies
// the global signature over the trusted comment, so a trusted comment (e.g. the
// release version bound into the signature) cannot be forged. Every failure is a
// distinct, wrapped error — never a bare false.
package minisign

import (
	"crypto/ed25519"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/crypto/blake2b"
)

// PublicKey is a parsed minisign public key: the 8-byte key id and the Ed25519 key.
type PublicKey struct {
	id  [8]byte
	key ed25519.PublicKey
}

// ParsePublicKey reads a minisign public key. It accepts either the two-line .pub
// file (an "untrusted comment:" line followed by the base64 key) or the bare
// base64 key line. The decoded blob is sig_alg(2) ++ key_id(8) ++ ed25519_pk(32).
func ParsePublicKey(s string) (PublicKey, error) {
	line := lastDataLine(s)
	if line == "" {
		return PublicKey{}, errors.New("minisign: empty public key")
	}
	raw, err := base64.StdEncoding.DecodeString(line)
	if err != nil {
		return PublicKey{}, fmt.Errorf("minisign: public key not base64: %w", err)
	}
	if len(raw) != 42 {
		return PublicKey{}, fmt.Errorf("minisign: public key is %d bytes, want 42", len(raw))
	}
	if raw[0] != 'E' || raw[1] != 'd' {
		return PublicKey{}, fmt.Errorf("minisign: unsupported public-key algorithm %q", string(raw[0:2]))
	}
	var pk PublicKey
	copy(pk.id[:], raw[2:10])
	pk.key = ed25519.PublicKey(append([]byte(nil), raw[10:42]...))
	return pk, nil
}

// Verify checks that sigFile (the bytes of a .minisig) is a valid minisign
// signature over message under pub. message is the signed file's raw content;
// the prehash is applied here when the signature is prehashed ("ED").
func Verify(pub PublicKey, message, sigFile []byte) error {
	alg, keyID, sig, trustedComment, globalSig, err := parseSignature(sigFile)
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare(keyID[:], pub.id[:]) != 1 {
		return fmt.Errorf("minisign: signature key id does not match the trusted key")
	}

	var signed []byte
	switch alg {
	case "ED": // prehashed: Ed25519 over BLAKE2b-512 of the file
		h := blake2b.Sum512(message)
		signed = h[:]
	case "Ed": // legacy: Ed25519 over the raw file
		signed = message
	default:
		return fmt.Errorf("minisign: unsupported signature algorithm %q", alg)
	}
	if !ed25519.Verify(pub.key, signed, sig) {
		return errors.New("minisign: signature verification failed (content does not match signature)")
	}
	// The global signature binds the trusted comment to the file signature, so the
	// trusted comment (version, filename, …) is authenticated too.
	if !ed25519.Verify(pub.key, append(append([]byte(nil), sig...), []byte(trustedComment)...), globalSig) {
		return errors.New("minisign: trusted-comment signature verification failed")
	}
	return nil
}

// VerifyFile verifies the file at path against the .minisig at sigPath under pub.
func VerifyFile(pub PublicKey, path, sigPath string) error {
	msg, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("minisign: read %s: %w", path, err)
	}
	sig, err := os.ReadFile(sigPath)
	if err != nil {
		return fmt.Errorf("minisign: read %s: %w", sigPath, err)
	}
	return Verify(pub, msg, sig)
}

// parseSignature reads a .minisig: an untrusted-comment line, the base64 signature
// (alg ++ key_id ++ sig), a "trusted comment:" line, and the base64 global sig.
func parseSignature(data []byte) (alg string, keyID [8]byte, sig []byte, trustedComment string, globalSig []byte, err error) {
	lines := splitLines(string(data))
	if len(lines) < 4 {
		return "", keyID, nil, "", nil, fmt.Errorf("minisign: malformed signature (%d lines, want >=4)", len(lines))
	}
	// lines[0] = untrusted comment (ignored). lines[1] = base64 sig block.
	sigRaw, derr := base64.StdEncoding.DecodeString(strings.TrimSpace(lines[1]))
	if derr != nil {
		return "", keyID, nil, "", nil, fmt.Errorf("minisign: signature line not base64: %w", derr)
	}
	if len(sigRaw) != 74 { // 2 alg + 8 key id + 64 signature
		return "", keyID, nil, "", nil, fmt.Errorf("minisign: signature block is %d bytes, want 74", len(sigRaw))
	}
	alg = string(sigRaw[0:2])
	copy(keyID[:], sigRaw[2:10])
	sig = append([]byte(nil), sigRaw[10:74]...)

	tc, ok := strings.CutPrefix(lines[2], "trusted comment: ")
	if !ok {
		return "", keyID, nil, "", nil, errors.New("minisign: missing 'trusted comment:' line")
	}
	trustedComment = tc

	globalSig, derr = base64.StdEncoding.DecodeString(strings.TrimSpace(lines[3]))
	if derr != nil {
		return "", keyID, nil, "", nil, fmt.Errorf("minisign: global signature not base64: %w", derr)
	}
	if len(globalSig) != 64 {
		return "", keyID, nil, "", nil, fmt.Errorf("minisign: global signature is %d bytes, want 64", len(globalSig))
	}
	return alg, keyID, sig, trustedComment, globalSig, nil
}

// lastDataLine returns the last non-empty, non-comment line (the base64 payload of
// a minisign .pub, whether or not the comment line is present).
func lastDataLine(s string) string {
	var out string
	for _, ln := range splitLines(s) {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "untrusted comment:") {
			continue
		}
		out = ln
	}
	return out
}

func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.Split(strings.TrimRight(s, "\n"), "\n")
}
