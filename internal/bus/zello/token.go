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
	"fmt"
	"time"
)

// Token minting.
//
// The Sample Development Token from developers.zello.com expires after 30 days,
// and an earlier reading of this feature treated that as an operational fact the
// operator would have to live with — a calendar reminder to re-paste a credential
// every month, forever, or the bridge goes silent.
//
// It is not. The sample token is a convenience for trying the API without writing
// any server code. The actual mechanism is an RSA-signed JWT carrying two claims,
// issuer and expiry, and Zello's own AUTH.md says so: "We use only two - issuer
// and expiration." Their reference implementation
// (zello-channel-api/auth/go/tokenmanager.go) sets `tokenExpirationSeconds = 60`
// — a fresh token per connection, sixty seconds long.
//
// So a node holding the issuer and the private key mints its own token whenever
// it dials, and nothing expires that an operator has to notice. The private key
// itself does not expire; it is only replaced if the operator regenerates the key
// pair, which is a deliberate act.
//
// The trade is that the node now holds a long-lived signing key rather than a
// 30-day bearer token. That is the better secret to hold — it is what Zello's own
// design expects, and a token stolen off a node is worth sixty seconds — but it
// is why TokenSigner's key is write-only in projections, excluded from portable
// profiles, and never logged.

// DefaultTokenTTL is how long a minted token is valid. It matches Zello's own
// reference implementation. Short is the point: the token is created for one
// logon and is worthless by the time anything could copy it.
//
// It is not shorter than a minute because logon is not instant — the socket has
// to open and the JSON has to cross — and a token that expires mid-handshake
// fails in a way that looks like bad credentials.
const DefaultTokenTTL = 60 * time.Second

// TokenSigner mints Zello auth tokens from an operator's key material.
//
// Both fields come from developers.zello.com under Keys. The issuer is public —
// it identifies the key, and its first segment is a base64 of the account it
// belongs to. The private key is not.
type TokenSigner struct {
	// Issuer is the `iss` claim, copied verbatim from the developer portal.
	Issuer string

	// PrivateKeyPEM is the RSA private key, PEM-encoded. Both the PKCS#8
	// ("PRIVATE KEY") and PKCS#1 ("RSA PRIVATE KEY") headers are accepted,
	// because the portal has emitted both and an operator pasting what they were
	// given should not have to know which they got.
	PrivateKeyPEM string
}

// Token mints a JWT valid for ttl. A ttl of zero uses DefaultTokenTTL.
func (s TokenSigner) Token(ttl time.Duration) (string, error) {
	return s.tokenAt(time.Now(), ttl)
}

// tokenAt is Token with the clock injected, so the claims can be asserted without
// a test depending on when it ran.
func (s TokenSigner) tokenAt(now time.Time, ttl time.Duration) (string, error) {
	if s.Issuer == "" {
		return "", fmt.Errorf("zello: no issuer; copy it from developers.zello.com under Keys")
	}
	if s.PrivateKeyPEM == "" {
		return "", fmt.Errorf("zello: no private key; copy it from developers.zello.com under Keys")
	}
	if ttl <= 0 {
		ttl = DefaultTokenTTL
	}

	key, err := parseRSAPrivateKey(s.PrivateKeyPEM)
	if err != nil {
		return "", err
	}

	// Marshalled from structs rather than assembled as strings so a value
	// containing a quote cannot produce a token that is silently malformed.
	header, err := json.Marshal(struct {
		Alg string `json:"alg"`
		Typ string `json:"typ"`
	}{"RS256", "JWT"})
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(struct {
		Iss string `json:"iss"`
		Exp int64  `json:"exp"`
	}{s.Issuer, now.Add(ttl).Unix()})
	if err != nil {
		return "", err
	}

	// RawURLEncoding: JWT segments are base64url with the padding stripped. A
	// padded segment is rejected by the far end as a malformed token.
	signing := base64.RawURLEncoding.EncodeToString(header) + "." +
		base64.RawURLEncoding.EncodeToString(payload)

	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", fmt.Errorf("zello: signing the token: %w", err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// parseRSAPrivateKey accepts the two PEM shapes the portal emits.
//
// The error deliberately does not echo any part of the input: a malformed key is
// reported by shape, never by content, so a mis-pasted secret cannot end up in a
// log line.
func parseRSAPrivateKey(pemText string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemText))
	if block == nil {
		return nil, fmt.Errorf("zello: the private key is not PEM; it should start with -----BEGIN PRIVATE KEY-----")
	}
	switch block.Type {
	case "PRIVATE KEY":
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("zello: parsing the private key: %w", err)
		}
		rk, ok := k.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("zello: the private key is not RSA; Zello tokens are RS256")
		}
		return rk, nil
	case "RSA PRIVATE KEY":
		k, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("zello: parsing the private key: %w", err)
		}
		return k, nil
	default:
		return nil, fmt.Errorf("zello: the PEM block is %q, not a private key", block.Type)
	}
}
