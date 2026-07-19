package updater

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/KN4OQW/waypoint/internal/minisign"
	"github.com/KN4OQW/waypoint/internal/verifydl"
)

// FetchManifest downloads and (when a key is configured) verifies the update
// manifest — so a downgrade or a malicious artifact URL is rejected before
// anything is fetched (RFC-0014/0013).
func FetchManifest(ctx context.Context, url string, pub minisign.PublicKey, hasPub bool) (Manifest, error) {
	v := verifydl.Verify{UserAgent: "Waypoint updater"}
	if hasPub {
		v.SigURL, v.PubKey, v.HasPubKey = url+".minisig", pub, true
	}
	body, err := verifydl.Download(ctx, url, v)
	if err != nil {
		return Manifest{}, fmt.Errorf("updater: fetch manifest: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return Manifest{}, fmt.Errorf("updater: parse manifest: %w", err)
	}
	if m.Version == "" {
		return Manifest{}, fmt.Errorf("updater: manifest has no version")
	}
	return m, nil
}

// DownloadArtifact fetches and verifies (SHA-256 + minisign) the release binary
// named in the plan — verify-before-stage.
func DownloadArtifact(ctx context.Context, art Artifact, pub minisign.PublicKey, hasPub bool) ([]byte, error) {
	v := verifydl.Verify{SHA256Hex: art.SHA256, UserAgent: "Waypoint updater"}
	if hasPub {
		v.SigURL, v.PubKey, v.HasPubKey = art.URL+".minisig", pub, true
	}
	return verifydl.Download(ctx, art.URL, v)
}

// OSSystem is the real System: file swaps, a systemd restart, an HTTPS health
// probe, and a JSON marker file. It is thin glue — the tested logic lives in the
// engine's state machine.
type OSSystem struct {
	BinaryPath string // the live binary the service execs
	Unit       string // systemd unit to restart
	HealthURL  string // https://127.0.0.1:<port>/api/health (self-signed, localhost)
	MarkerPath string // where the in-flight-update marker lives
	Systemctl  func(args ...string) error
}

func (s *OSSystem) stagePath() string    { return s.BinaryPath + ".new" }
func (s *OSSystem) rollbackPath() string { return s.BinaryPath + ".rollback" }

func (s *OSSystem) StageBinary(data []byte) (string, error) {
	// Write beside the live binary (same filesystem, so the later rename is atomic),
	// via a temp + rename so a crash never leaves a half-written staged binary.
	sp := s.stagePath()
	if err := writeAtomic(sp, data, 0o755); err != nil {
		return "", err
	}
	return sp, nil
}

func (s *OSSystem) BackupCurrent() (string, error) {
	data, err := os.ReadFile(s.BinaryPath)
	if err != nil {
		return "", err
	}
	rp := s.rollbackPath()
	if err := writeAtomic(rp, data, 0o755); err != nil {
		return "", err
	}
	return rp, nil
}

// Swap atomically replaces the live binary with the staged one. rename(2) on the
// same directory is atomic; Linux permits replacing a running binary's file.
func (s *OSSystem) Swap(stagePath string) error { return os.Rename(stagePath, s.BinaryPath) }

func (s *OSSystem) Restore(rollbackPath string) error {
	data, err := os.ReadFile(rollbackPath)
	if err != nil {
		return err
	}
	sp := s.BinaryPath + ".restore"
	if err := writeAtomic(sp, data, 0o755); err != nil {
		return err
	}
	return os.Rename(sp, s.BinaryPath)
}

func (s *OSSystem) Restart(ctx context.Context) error {
	if s.Systemctl == nil {
		s.Systemctl = func(args ...string) error { return exec.Command("systemctl", args...).Run() }
	}
	return s.Systemctl("restart", s.Unit)
}

// Health probes the running node's /api/health over HTTPS. The cert is the node's
// own self-signed device cert on localhost, so verification is skipped — this is a
// liveness probe of ourselves, not a trust boundary.
func (s *OSSystem) Health(ctx context.Context) (string, bool) {
	client := &http.Client{
		Timeout:   3 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, //nolint:gosec // localhost self-signed
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.HealthURL, nil)
	if err != nil {
		return "", false
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	var h struct {
		Status  string `json:"status"`
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		return "", false
	}
	return h.Version, h.Status == "ok"
}

func (s *OSSystem) WriteMarker(m Marker) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	// fsync the marker (writeAtomic renames after close) so it survives a power cut
	// the moment before the swap.
	return writeAtomic(s.MarkerPath, b, 0o644)
}

func (s *OSSystem) ReadMarker() (*Marker, error) {
	b, err := os.ReadFile(s.MarkerPath)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var m Marker
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func (s *OSSystem) ClearMarker() error {
	err := os.Remove(s.MarkerPath)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (s *OSSystem) Now() time.Time { return time.Now() }

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".upd-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil { // durable before the rename
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
