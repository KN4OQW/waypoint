//go:build tier2

package tier2

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/config"
)

// TestTier2_DGIdGatewayAcceptsRenderedConfig is issue #17 Tier 2, the YSF
// gateway-startup claim: the DGIdGateway.ini this project generates must be
// accepted by the pinned DGIdGateway (YSFClients @ 2b480aa) — it has to parse,
// bind MMDVM-Host's YSF loopback, and stay up.
//
// Tier 1 only proves we emit the bytes we intended. This proves the real daemon
// agrees they are a valid configuration, which is a different claim.
func TestTier2_DGIdGatewayAcceptsRenderedConfig(t *testing.T) {
	bin := gwBin("DGIdGateway")
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("DGIdGateway binary not built: %v", err)
	}

	m := &config.Model{
		General: config.General{Callsign: "KN4OQW", ID: "3180202"},
		YSFGW: config.YSFGateway{
			EnableDGId: true,
			Suffix:     "ND",
			Startup:    "FCS00290",
			YCSNetwork: true,
		},
	}
	ini := m.RenderDGIdGateway()

	confPath := filepath.Join(t.TempDir(), "DGIdGateway.ini")
	if err := os.WriteFile(confPath, []byte(ini), 0o600); err != nil {
		t.Fatal(err)
	}

	// The DG-ID 1 Parrot block points at a local YSFParrot on 42012/42013.
	parrot := exec.Command(gwBin("YSFParrot"), "42012")
	parrotLog := &strings.Builder{}
	parrot.Stdout, parrot.Stderr = parrotLog, parrotLog
	if err := parrot.Start(); err == nil {
		defer func() { parrot.Process.Kill(); parrot.Wait() }()
	}

	cmd := exec.Command(bin, confPath)
	logBuf := &strings.Builder{}
	cmd.Stdout, cmd.Stderr = logBuf, logBuf
	if err := cmd.Start(); err != nil {
		t.Fatalf("start DGIdGateway: %v", err)
	}
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
		t.Logf("--- DGIdGateway log ---\n%s", logBuf.String())
		if s := parrotLog.String(); strings.TrimSpace(s) != "" {
			t.Logf("--- YSFParrot log ---\n%s", s)
		}
	}()

	// It must still be alive after startup, i.e. it did not reject the config.
	time.Sleep(3 * time.Second)
	if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
		t.Fatalf("DGIdGateway exited with %v after reading the generated config:\n%s",
			cmd.ProcessState.ExitCode(), logBuf.String())
	}

	// And it must own MMDVM-Host's YSF loopback: binding 4200 should now fail.
	c, err := net.ListenPacket("udp4", "127.0.0.1:4200")
	if err == nil {
		c.Close()
		t.Errorf("DGIdGateway did not bind the YSF loopback :4200 from the generated config")
	} else {
		t.Logf("PASS: DGIdGateway owns the YSF loopback :4200 (bind refused: %v)", err)
	}
	t.Logf("PASS: pinned DGIdGateway accepted the generated config and stayed up")
}
