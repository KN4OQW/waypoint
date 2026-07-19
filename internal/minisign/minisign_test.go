package minisign

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"

	"golang.org/x/crypto/blake2b"
)

// signMinisign builds a .minisig exactly as the minisign tool does, so the test
// exercises the real wire format independently of the verifier's parsing.
func signMinisign(priv ed25519.PrivateKey, keyID [8]byte, message []byte, prehashed bool, trusted string) string {
	alg := "Ed"
	signed := message
	if prehashed {
		alg = "ED"
		h := blake2b.Sum512(message)
		signed = h[:]
	}
	sig := ed25519.Sign(priv, signed)
	block := append([]byte(alg), keyID[:]...)
	block = append(block, sig...)
	global := ed25519.Sign(priv, append(append([]byte(nil), sig...), []byte(trusted)...))
	return "untrusted comment: signature from waypoint test\n" +
		base64.StdEncoding.EncodeToString(block) + "\n" +
		"trusted comment: " + trusted + "\n" +
		base64.StdEncoding.EncodeToString(global) + "\n"
}

func pubKeyString(pub ed25519.PublicKey, keyID [8]byte) string {
	blob := append([]byte("Ed"), keyID[:]...)
	blob = append(blob, pub...)
	return "untrusted comment: minisign public key\n" + base64.StdEncoding.EncodeToString(blob) + "\n"
}

func testKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey, [8]byte, PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var id [8]byte
	rand.Read(id[:])
	pk, err := ParsePublicKey(pubKeyString(pub, id))
	if err != nil {
		t.Fatalf("ParsePublicKey: %v", err)
	}
	return pub, priv, id, pk
}

// Property 1: a correctly-signed file verifies, in both legacy and prehashed modes.
func TestVerifyValid(t *testing.T) {
	_, priv, id, pk := testKey(t)
	msg := []byte("waypointd-arm binary bytes, pretend this is a release artifact")
	for _, prehashed := range []bool{false, true} {
		sig := signMinisign(priv, id, msg, prehashed, "waypoint v1.2.3")
		if err := Verify(pk, msg, []byte(sig)); err != nil {
			t.Errorf("valid signature (prehashed=%v) rejected: %v", prehashed, err)
		}
	}
}

// Property 5 (the #12 acceptance): a byte-flipped artifact is rejected with a
// clear error.
func TestVerifyTamperedContent(t *testing.T) {
	_, priv, id, pk := testKey(t)
	msg := []byte("the real artifact")
	sig := signMinisign(priv, id, msg, true, "v1")

	tampered := append([]byte(nil), msg...)
	tampered[0] ^= 0x01
	err := Verify(pk, tampered, []byte(sig))
	if err == nil {
		t.Fatal("tampered content was accepted")
	}
	if !strings.Contains(err.Error(), "minisign") {
		t.Errorf("error is not a clear minisign error: %v", err)
	}
}

// A signature from a different key (wrong key id) is rejected.
func TestVerifyWrongKey(t *testing.T) {
	_, priv, id, _ := testKey(t)
	msg := []byte("hello")
	sig := signMinisign(priv, id, msg, true, "v1")
	// A different trusted public key: same message/sig, but pk2's id won't match.
	_, _, _, pk2 := testKey(t)
	if err := Verify(pk2, msg, []byte(sig)); err == nil {
		t.Error("signature verified against the wrong trusted key")
	}
}

// A forged trusted comment (changed after signing) is rejected by the global sig.
func TestVerifyForgedTrustedComment(t *testing.T) {
	_, priv, id, pk := testKey(t)
	msg := []byte("payload")
	sig := signMinisign(priv, id, msg, true, "v1.0.0")
	forged := strings.Replace(sig, "trusted comment: v1.0.0", "trusted comment: v9.9.9-evil", 1)
	if err := Verify(pk, msg, []byte(forged)); err == nil {
		t.Error("forged trusted comment was accepted (global signature not checked)")
	}
}

// A malformed / truncated .minisig is a clear error, not a panic or a pass.
func TestVerifyMalformed(t *testing.T) {
	_, _, _, pk := testKey(t)
	for _, bad := range []string{
		"",
		"untrusted comment: x\n",
		"untrusted comment: x\nnot-base64!!!\ntrusted comment: y\nZm9v\n",
		"untrusted comment: x\n" + base64.StdEncoding.EncodeToString([]byte("short")) + "\ntrusted comment: y\nZm9v\n",
	} {
		if err := Verify(pk, []byte("m"), []byte(bad)); err == nil {
			t.Errorf("malformed signature accepted: %q", bad)
		}
	}
}

// ParsePublicKey accepts both the two-line file and a bare base64 line, and
// rejects junk.
func TestParsePublicKey(t *testing.T) {
	pub, _, id, _ := testKey(t)
	full := pubKeyString(pub, id)
	bare := strings.TrimSpace(splitLines(full)[1])
	if _, err := ParsePublicKey(full); err != nil {
		t.Errorf("two-line pubkey rejected: %v", err)
	}
	if _, err := ParsePublicKey(bare); err != nil {
		t.Errorf("bare pubkey line rejected: %v", err)
	}
	if _, err := ParsePublicKey("not a key"); err == nil {
		t.Error("junk pubkey accepted")
	}
}
