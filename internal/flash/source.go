package flash

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/KN4OQW/waypoint/internal/minisign"
	"github.com/KN4OQW/waypoint/internal/verifydl"
)

// Where firmware comes from, and why verification is not optional here.
//
// RFC-0013 makes signature checking opt-in per source, because reference data
// is fetched from upstreams that do not sign anything and a node is better off
// with an unverified host list than with none. Firmware is the other case
// entirely: it is arbitrary code that will run on a transmitter, under a
// licence held by a person who is legally answerable for what it emits. So the
// rule here is absolute — a firmware artifact with no valid signature is not
// flashed, and there is no configuration that relaxes it.

// Source supplies the catalog and the images it names.
type Source interface {
	Catalog(ctx context.Context) (Catalog, error)
	Artifact(ctx context.Context, v Variant) ([]byte, error)
}

// HTTPSource fetches from the signed firmware release, caching verified bytes
// on disk.
type HTTPSource struct {
	// CatalogURL is the signed firmware.json. Its .minisig sits beside it
	// unless SigSuffix says otherwise.
	CatalogURL string
	PubKey     minisign.PublicKey
	// CacheDir holds verified artifacts, named by digest. A node that has
	// flashed once can flash again with no network at all — which matters,
	// because the times an operator most wants to reflash are the times their
	// node is least likely to be working.
	CacheDir  string
	UserAgent string
}

const sigSuffix = ".minisig"

// Catalog fetches and verifies the catalog.
func (s *HTTPSource) Catalog(ctx context.Context) (Catalog, error) {
	if s.CatalogURL == "" {
		return Catalog{}, fmt.Errorf("flash: no firmware catalog is configured")
	}
	body, err := verifydl.Download(ctx, s.CatalogURL, verifydl.Verify{
		SigURL:    s.CatalogURL + sigSuffix,
		PubKey:    s.PubKey,
		HasPubKey: true,
		Require:   true,
		UserAgent: s.UserAgent,
	})
	if err != nil {
		return Catalog{}, err
	}
	return ParseCatalog(body)
}

// Artifact returns the image for a variant, verified twice over.
//
// The signature proves the bytes are ours; the digest proves they are the ones
// THIS catalog entry names. Neither subsumes the other: a validly signed image
// for a different board would pass the first check and fail the second, and
// that is a mix-up a firmware release can plausibly make.
func (s *HTTPSource) Artifact(ctx context.Context, v Variant) ([]byte, error) {
	want := strings.ToLower(strings.TrimSpace(v.SHA256))
	if cached, ok := s.fromCache(want); ok {
		return cached, nil
	}

	sig := v.SigURL
	if sig == "" {
		sig = v.URL + sigSuffix
	}
	body, err := verifydl.Download(ctx, v.URL, verifydl.Verify{
		SigURL:    sig,
		PubKey:    s.PubKey,
		HasPubKey: true,
		Require:   true,
		UserAgent: s.UserAgent,
	})
	if err != nil {
		return nil, err
	}
	if got := digest(body); got != want {
		return nil, fmt.Errorf("flash: %s: the signed image does not match the digest catalog entry %s names "+
			"(want %s, got %s)", v.URL, v.ID, want, got)
	}
	s.toCache(want, body)
	return body, nil
}

// fromCache returns a cached artifact, re-checking its digest on the way out —
// an SD card that corrupted a cached file must not be able to feed it to a
// modem later.
func (s *HTTPSource) fromCache(digestHex string) ([]byte, bool) {
	if s.CacheDir == "" || digestHex == "" {
		return nil, false
	}
	b, err := os.ReadFile(s.cachePath(digestHex))
	if err != nil || digest(b) != digestHex {
		return nil, false
	}
	return b, true
}

func (s *HTTPSource) toCache(digestHex string, body []byte) {
	if s.CacheDir == "" || digestHex == "" {
		return
	}
	if err := os.MkdirAll(s.CacheDir, 0o755); err != nil {
		return
	}
	// Written to a temporary name and renamed, so a cache file never exists in a
	// half-written state — the same argument RFC-0014 makes about the binary.
	tmp := s.cachePath(digestHex) + ".part"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, s.cachePath(digestHex))
}

func (s *HTTPSource) cachePath(digestHex string) string {
	return filepath.Join(s.CacheDir, digestHex+".bin")
}

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
