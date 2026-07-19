package verifydl

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KN4OQW/waypoint/internal/minisign"
	"golang.org/x/crypto/blake2b"
)

// serve maps request paths to bodies.
func serve(t *testing.T, files map[string][]byte) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	for p, b := range files {
		b := b
		mux.HandleFunc(p, func(w http.ResponseWriter, r *http.Request) { w.Write(b) })
	}
	return httptest.NewServer(mux)
}

// Property 2: checksum mode — a matching body is returned, a tampered one rejected.
func TestChecksum(t *testing.T) {
	body := []byte("DMR_Hosts.txt real content")
	sum := sha256.Sum256(body)
	srv := serve(t, map[string][]byte{
		"/hosts":        body,
		"/hosts.sha256": []byte(hex.EncodeToString(sum[:]) + "  hosts\n"),
	})
	defer srv.Close()

	// Good: pinned hex.
	got, err := Download(context.Background(), srv.URL+"/hosts", Verify{SHA256Hex: hex.EncodeToString(sum[:])})
	if err != nil || string(got) != string(body) {
		t.Fatalf("pinned checksum: body=%q err=%v", got, err)
	}
	// Good: sidecar URL.
	if _, err := Download(context.Background(), srv.URL+"/hosts", Verify{SHA256URL: srv.URL + "/hosts.sha256"}); err != nil {
		t.Errorf("sidecar checksum rejected a valid file: %v", err)
	}
	// Tampered: wrong pinned digest → reject, no body.
	got, err = Download(context.Background(), srv.URL+"/hosts", Verify{SHA256Hex: strings.Repeat("00", 32)})
	if err == nil || got != nil {
		t.Errorf("tampered checksum accepted: body=%q", got)
	}
	if !strings.Contains(err.Error(), "SHA-256 mismatch") {
		t.Errorf("checksum error not clear: %v", err)
	}
}

// Property 3: signature mode — a validly-signed body is returned, a tampered one
// (or a body whose sidecar no longer matches) is rejected.
func TestSignature(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var id [8]byte
	rand.Read(id[:])
	pk, _ := minisign.ParsePublicKey("untrusted comment: k\n" +
		base64.StdEncoding.EncodeToString(append(append([]byte("Ed"), id[:]...), pub...)) + "\n")

	body := []byte("TGList.txt signed content")
	sig := makeMinisig(priv, id, body, "TGList v1")
	tampered := append([]byte(nil), body...)
	tampered[0] ^= 0xff
	tsig := makeMinisig(priv, id, tampered, "TGList v1") // valid sig, but for different content

	srv := serve(t, map[string][]byte{
		"/tg":           body,
		"/tg.minisig":   []byte(sig),
		"/bad":          tampered,
		"/bad-mismatch": body,         // body...
		"/bad.minisig":  []byte(tsig), // ...with a signature for OTHER content
	})
	defer srv.Close()

	v := Verify{SigURL: srv.URL + "/tg.minisig", PubKey: pk, HasPubKey: true}
	got, err := Download(context.Background(), srv.URL+"/tg", v)
	if err != nil || string(got) != string(body) {
		t.Fatalf("valid signature: body=%q err=%v", got, err)
	}

	// Body served that does not match its signature → reject.
	v2 := Verify{SigURL: srv.URL + "/bad.minisig", PubKey: pk, HasPubKey: true}
	got, err = Download(context.Background(), srv.URL+"/bad-mismatch", v2)
	if err == nil || got != nil {
		t.Errorf("body not matching its signature was accepted: %q", got)
	}
	if !strings.Contains(err.Error(), "signature verification failed") {
		t.Errorf("signature error not clear: %v", err)
	}
}

// Require rejects a download with no verification configured.
func TestRequire(t *testing.T) {
	srv := serve(t, map[string][]byte{"/x": []byte("data")})
	defer srv.Close()
	if _, err := Download(context.Background(), srv.URL+"/x", Verify{Require: true}); err == nil {
		t.Error("Require did not reject an unverified download")
	}
	// Without Require, an unconfigured download passes through.
	if b, err := Download(context.Background(), srv.URL+"/x", Verify{}); err != nil || string(b) != "data" {
		t.Errorf("plain fetch failed: %q %v", b, err)
	}
}

func makeMinisig(priv ed25519.PrivateKey, id [8]byte, msg []byte, trusted string) string {
	h := blake2b.Sum512(msg)
	sig := ed25519.Sign(priv, h[:])
	block := append(append([]byte("ED"), id[:]...), sig...)
	global := ed25519.Sign(priv, append(append([]byte(nil), sig...), []byte(trusted)...))
	return "untrusted comment: sig\n" + base64.StdEncoding.EncodeToString(block) + "\n" +
		"trusted comment: " + trusted + "\n" + base64.StdEncoding.EncodeToString(global) + "\n"
}
