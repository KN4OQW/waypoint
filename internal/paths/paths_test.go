package paths

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// busUnitFile is the shipped systemd template, relative to this package. The
// image installs it verbatim from the filesystem overlay, so the bytes read here
// are the bytes that land on a node.
const busUnitFile = "../../image/src/modules/waypoint/filesystem/root/etc/systemd/system/waypoint-bus@.service"

// The bus daemon's binary path and config path are each written down twice: once
// in this package, where the renderer and the daemon read them, and once in the
// systemd template's ExecStart, where systemd does. Nothing in Go links the two,
// and they have already drifted once — the unit was authored against the pre-0.3
// /home/pi-star layout and went on naming it for a full release after the state
// tree moved to /var/lib/waypoint (issue #109). On a node flashed from a 0.3
// image, where no compatibility symlink exists, that unit names a config file
// that is never written and a binary that is not there.
//
// The drift was invisible because both halves are individually correct: the
// renderer wrote a real file, the unit was valid, and the failure only appeared
// on a fresh flash. This test is the link.
func TestBusUnitMatchesPaths(t *testing.T) {
	b, err := os.ReadFile(busUnitFile)
	if err != nil {
		t.Fatalf("read the shipped bus unit: %v", err)
	}
	unit := string(b)

	var exec string
	for _, line := range strings.Split(unit, "\n") {
		if strings.HasPrefix(line, "ExecStart=") {
			exec = line
			break
		}
	}
	if exec == "" {
		t.Fatal("the bus unit has no ExecStart= line")
	}

	// %i is systemd's instance token — the bus id. The renderer's own name for the
	// same file comes from BusConfigFile, so substituting the token has to produce
	// exactly the path the daemon will be handed.
	wantConfig := filepath.Join(EtcDir, BusConfigFile("%i"))
	if !strings.Contains(exec, " -config "+wantConfig+" ") {
		t.Errorf("bus unit ExecStart does not pass -config %s\n  ExecStart: %s", wantConfig, exec)
	}
	if !strings.Contains(exec, "ExecStart="+BusBinary+" ") {
		t.Errorf("bus unit ExecStart does not run %s\n  ExecStart: %s", BusBinary, exec)
	}

	// The asymmetry with waypointd is the point of BusBinary's comment, and it is
	// the mistake most likely to be made by someone tidying the two onto one path:
	// BinDir holds exactly the file the RFC-0014 updater renames, and a second
	// binary there would be swapped by nothing and boot-checked by nothing.
	if strings.Contains(exec, BinDir) {
		t.Errorf("the bus daemon must not be run from the updater's %s; it is delivered with the image\n  ExecStart: %s", BinDir, exec)
	}
	// The tree this issue was filed about. A node flashed from a current image has
	// no symlink at the old path, so naming it here is a unit that cannot start.
	if strings.Contains(unit, LegacyStateDir) {
		t.Errorf("the bus unit names the pre-0.3 %s, which does not exist on a freshly flashed node", LegacyStateDir)
	}
}
