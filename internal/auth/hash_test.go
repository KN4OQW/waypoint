package auth

import (
	"encoding/base64"
	"strings"
	"testing"

	"golang.org/x/crypto/argon2"
)

// encodeForTest builds a HashRecord.Hash ("<b64 salt>$<b64 digest>") for arbitrary
// cost parameters, so a record written under non-default costs can be exercised.
func encodeForTest(t *testing.T, salt []byte, pw string, m, time uint32, threads uint8) string {
	t.Helper()
	digest := argon2.IDKey([]byte(pw), salt, time, m, threads, argonKeyLen)
	return base64.RawStdEncoding.EncodeToString(salt) + "$" + base64.RawStdEncoding.EncodeToString(digest)
}

// HashPassword produces a verifiable record whose params reflect RFC-0002 and
// whose digest matches only the right password.
func TestHashAndVerify(t *testing.T) {
	rec, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Params != "argon2id$v=19$m=65536,t=1,p=4" {
		t.Fatalf("params = %q, want the RFC-0002 argon2id block", rec.Params)
	}
	ok, err := rec.Verify("correct horse battery staple")
	if err != nil || !ok {
		t.Fatalf("Verify(correct) = %v, %v; want true, nil", ok, err)
	}
	ok, err = rec.Verify("wrong password")
	if err != nil || ok {
		t.Fatalf("Verify(wrong) = %v, %v; want false, nil", ok, err)
	}
}

// Each hash uses a fresh random salt, so the same password hashes differently and
// the plaintext never appears in the record.
func TestHashSaltedAndOpaque(t *testing.T) {
	const pw = "s3cret-passw0rd"
	a, _ := HashPassword(pw)
	b, _ := HashPassword(pw)
	if a.Hash == b.Hash {
		t.Fatal("two hashes of the same password are identical — salt not applied")
	}
	if strings.Contains(a.Hash, pw) || strings.Contains(a.Params, pw) {
		t.Fatal("password plaintext leaked into the hash record")
	}
}

// A corrupt record verifies to false with an error, never to a silent pass.
func TestVerifyMalformedRecord(t *testing.T) {
	for _, rec := range []HashRecord{
		{Params: "", Hash: ""},
		{Params: "argon2id$v=19$m=65536,t=1,p=4", Hash: "not-base64!!!"},
		{Params: "md5$v=19$m=1,t=1,p=1", Hash: "AAAA$BBBB"},
		{Params: "argon2id$v=19$m=0,t=1,p=4", Hash: "AAAA$BBBB"},
	} {
		ok, err := rec.Verify("anything")
		if ok {
			t.Fatalf("malformed record %+v verified true", rec)
		}
		if err == nil {
			t.Fatalf("malformed record %+v returned nil error", rec)
		}
	}
}

// A record written under weaker (past) parameters still verifies — the property
// that lets cost be raised without a breaking migration.
func TestVerifyHonorsStoredParams(t *testing.T) {
	// Hand-build a record at t=1,m=8MiB,p=1 (weaker than the current default) and
	// confirm Verify re-derives under those stored knobs, not the current constants.
	salt := []byte("0123456789abcdef")
	const pw = "legacy-cost-pw"
	// Mirror HashRecord's encoding for a custom cost.
	rec := HashRecord{Params: "argon2id$v=19$m=8192,t=1,p=1"}
	rec.Hash = encodeForTest(t, salt, pw, 8192, 1, 1)
	ok, err := rec.Verify(pw)
	if err != nil || !ok {
		t.Fatalf("Verify under stored legacy params = %v, %v; want true, nil", ok, err)
	}
}

func TestNewSessionTokenHashed(t *testing.T) {
	raw, hash, err := newSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	if raw == "" || hash == "" {
		t.Fatal("empty token or hash")
	}
	if raw == hash {
		t.Fatal("raw token equals its hash — not hashed at rest")
	}
	if hashToken(raw) != hash {
		t.Fatal("hashToken is not deterministic for the same raw token")
	}
	raw2, _, _ := newSessionToken()
	if raw2 == raw {
		t.Fatal("two tokens collided — not random")
	}
}
