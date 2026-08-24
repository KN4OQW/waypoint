package zello

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"strings"
	"testing"
	"time"
)

// A key generated per run rather than a fixture, so no private key of any kind
// lives in this repository — not even a throwaway one that someone might later
// mistake for a template.
func testKey(t *testing.T) (TokenSigner, *rsa.PublicKey) {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	pemText := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	return TokenSigner{Issuer: "TEST-ISSUER.abc123", PrivateKeyPEM: pemText}, &k.PublicKey
}

func decodeSegment(t *testing.T, seg string) map[string]any {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		t.Fatalf("segment is not unpadded base64url: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("segment is not JSON: %v", err)
	}
	return m
}

// The decisive test: a token this package mints verifies against the matching
// public key. Everything else about the format could be right and the token still
// be rejected if the signature is computed over the wrong bytes.
func TestAMintedTokenVerifiesAgainstItsPublicKey(t *testing.T) {
	s, pub := testKey(t)

	tok, err := s.Token(time.Minute)
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("a JWT has three segments, got %d", len(parts))
	}

	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("signature is not unpadded base64url: %v", err)
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig); err != nil {
		t.Fatalf("the signature does not verify: %v", err)
	}
}

// The header and claims have to be exactly what Zello documents: RS256, and only
// iss and exp. An extra claim is not obviously harmful, but this is a format
// somebody else validates, and the documentation says "we use only two".
func TestTheTokenCarriesExactlyTheDocumentedClaims(t *testing.T) {
	s, _ := testKey(t)
	at := time.Unix(1_700_000_000, 0)

	tok, err := s.tokenAt(at, 90*time.Second)
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	parts := strings.Split(tok, ".")

	head := decodeSegment(t, parts[0])
	if head["alg"] != "RS256" || head["typ"] != "JWT" {
		t.Errorf("header = %v, want RS256/JWT", head)
	}

	claims := decodeSegment(t, parts[1])
	if claims["iss"] != "TEST-ISSUER.abc123" {
		t.Errorf("iss = %v", claims["iss"])
	}
	exp, ok := claims["exp"].(float64)
	if !ok {
		t.Fatalf("exp is %T, want a number", claims["exp"])
	}
	if int64(exp) != at.Add(90*time.Second).Unix() {
		t.Errorf("exp = %d, want %d", int64(exp), at.Add(90*time.Second).Unix())
	}
	if len(claims) != 2 {
		t.Errorf("claims = %v; Zello documents only iss and exp", claims)
	}
}

// JWT segments are base64url with padding stripped. A padded segment is a
// malformed token to the far end, and the failure would look like bad
// credentials rather than a formatting bug.
func TestSegmentsAreUnpaddedBase64URL(t *testing.T) {
	s, _ := testKey(t)
	tok, err := s.Token(0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(tok, "=") {
		t.Errorf("token contains padding: %s", tok)
	}
	if strings.ContainsAny(tok, "+/") {
		t.Errorf("token uses standard base64 rather than base64url: %s", tok)
	}
}

func TestDefaultTTLIsUsedWhenUnset(t *testing.T) {
	s, _ := testKey(t)
	at := time.Unix(1_700_000_000, 0)
	tok, err := s.tokenAt(at, 0)
	if err != nil {
		t.Fatal(err)
	}
	claims := decodeSegment(t, strings.Split(tok, ".")[1])
	if int64(claims["exp"].(float64)) != at.Add(DefaultTokenTTL).Unix() {
		t.Errorf("exp = %v, want now+%v", claims["exp"], DefaultTokenTTL)
	}
}

func TestBothPEMShapesAreAccepted(t *testing.T) {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pkcs1 := string(pem.EncodeToMemory(&pem.Block{
		Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k),
	}))
	s := TokenSigner{Issuer: "iss", PrivateKeyPEM: pkcs1}
	if _, err := s.Token(time.Minute); err != nil {
		t.Errorf("a PKCS#1 key was refused: %v", err)
	}
}

// A mis-pasted secret must never reach a log, so the refusal is by shape and
// never quotes the input.
func TestKeyErrorsDoNotEchoTheInput(t *testing.T) {
	const secretish = "SUPERSECRETMATERIAL"
	cases := []TokenSigner{
		{Issuer: "iss", PrivateKeyPEM: secretish},
		{Issuer: "iss", PrivateKeyPEM: "-----BEGIN PRIVATE KEY-----\n" + secretish + "\n-----END PRIVATE KEY-----"},
	}
	for _, s := range cases {
		_, err := s.Token(time.Minute)
		if err == nil {
			t.Fatal("a malformed key was accepted")
		}
		if strings.Contains(err.Error(), secretish) {
			t.Errorf("the error echoed the key material: %v", err)
		}
	}
}

func TestMissingMaterialIsRefusedWithSomethingActionable(t *testing.T) {
	if _, err := (TokenSigner{PrivateKeyPEM: "x"}).Token(0); err == nil {
		t.Error("a signer with no issuer was accepted")
	} else if !strings.Contains(err.Error(), "developers.zello.com") {
		t.Errorf("error %q does not say where to get the issuer", err)
	}
	if _, err := (TokenSigner{Issuer: "x"}).Token(0); err == nil {
		t.Error("a signer with no key was accepted")
	}
}
