// Package verifydl downloads a file and verifies it before returning any bytes,
// so a tampered or corrupt reference-data download can never replace good cached
// data (RFC-0013 / issue #12). It supports a SHA-256 checksum (pinned or a sidecar
// URL) and a minisign signature (a .minisig sidecar verified against a trusted
// key). Verification is opt-in per source, but once a checksum/signature is
// configured it is mandatory: a mismatch is a typed, human-readable error and no
// body — the caller keeps its previous cache.
package verifydl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/KN4OQW/waypoint/internal/minisign"
)

const maxDownload = 64 << 20 // 64 MiB ceiling for a reference-data file

// Verify configures how a download is checked. An empty Verify means no
// verification (plain fetch) unless Require is set, which then rejects.
type Verify struct {
	SHA256Hex string             // pinned lowercase-hex SHA-256 of the body
	SHA256URL string             // sidecar URL returning the hex digest
	SigURL    string             // sidecar .minisig URL
	PubKey    minisign.PublicKey // trusted key for SigURL
	HasPubKey bool               // whether PubKey is set
	Require   bool               // reject a download that has no verification configured
	UserAgent string
}

func (v Verify) configured() bool {
	return v.SHA256Hex != "" || v.SHA256URL != "" || v.SigURL != ""
}

// Download fetches url and returns its body only if it verifies. A verification
// failure returns a wrapped error naming the url and reason, and no bytes.
func Download(ctx context.Context, url string, v Verify) ([]byte, error) {
	if !v.configured() && v.Require {
		return nil, fmt.Errorf("verifydl: %s: verification required but no checksum/signature configured", url)
	}
	body, err := get(ctx, url, v.UserAgent)
	if err != nil {
		return nil, err
	}

	switch {
	case v.SigURL != "":
		if !v.HasPubKey {
			return nil, fmt.Errorf("verifydl: %s: a signature URL is set but no trusted key", url)
		}
		sig, err := get(ctx, v.SigURL, v.UserAgent)
		if err != nil {
			return nil, fmt.Errorf("verifydl: %s: fetch signature: %w", url, err)
		}
		if err := minisign.Verify(v.PubKey, body, sig); err != nil {
			return nil, fmt.Errorf("verifydl: %s: signature verification failed: %w", url, err)
		}
	case v.SHA256Hex != "" || v.SHA256URL != "":
		want := strings.ToLower(strings.TrimSpace(v.SHA256Hex))
		if want == "" {
			raw, err := get(ctx, v.SHA256URL, v.UserAgent)
			if err != nil {
				return nil, fmt.Errorf("verifydl: %s: fetch checksum: %w", url, err)
			}
			want = firstHexField(string(raw))
		}
		got := hex.EncodeToString(sha256Sum(body))
		if want == "" || got != want {
			return nil, fmt.Errorf("verifydl: %s: SHA-256 mismatch (want %q, got %s)", url, want, got)
		}
	}
	return body, nil
}

func get(ctx context.Context, url, ua string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("verifydl: %s: HTTP %d", url, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxDownload))
}

func sha256Sum(b []byte) []byte {
	s := sha256.Sum256(b)
	return s[:]
}

// firstHexField returns the first whitespace-delimited token of a checksum file
// (handles both "<hex>" and the "<hex>  filename" coreutils format).
func firstHexField(s string) string {
	for _, f := range strings.Fields(s) {
		return strings.ToLower(f)
	}
	return ""
}
