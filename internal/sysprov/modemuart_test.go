package sysprov

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KN4OQW/waypoint/internal/privhelper"
)

// stockBoot writes the boot partition a fresh Raspberry Pi OS Lite install has:
// the UART commented out, Bluetooth holding the PL011, a login console on the
// serial port. A hat fitted to this node is electrically fine and mute.
func stockBoot(t *testing.T, s *System) string {
	t.Helper()
	dir := s.path("/boot/firmware")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("config.txt", "dtparam=audio=on\n#enable_uart=1\n")
	write("cmdline.txt", "console=serial0,115200 console=tty1 root=PARTUUID=aa-02 rootwait\n")
	return dir
}

// gettyRunner wraps the fake command runner with a stateful serial-getty:
// enabled until it is masked, masked afterwards. Stateful rather than canned,
// because "the repair actually cleared the problem" is the property under test
// and a stub that always answers "enabled" cannot show it.
func gettyRunner(s *System, masked *[]string) {
	inner := s.Run
	isMasked := map[string]bool{}
	s.Run = func(ctx context.Context, stdin []byte, name string, args ...string) (string, error) {
		if name == "systemctl" && len(args) >= 2 {
			switch args[0] {
			case "is-enabled":
				if isMasked[args[1]] {
					return "masked\n", nil
				}
				return "enabled\n", nil
			case "mask":
				isMasked[args[1]] = true
				*masked = append(*masked, args[1])
				return "", nil
			}
		}
		return inner(ctx, stdin, name, args...)
	}
}

func TestEnableModemUARTFreesTheUARTOnAStockInstall(t *testing.T) {
	s, _ := newSystem(t)
	dir := stockBoot(t, s)
	var masked []string
	gettyRunner(s, &masked)

	got, err := s.EnableModemUART(context.Background(), privhelper.EnableModemUARTRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Applicable {
		t.Fatal("a node with a boot partition reported the repair as not applying")
	}
	if !got.RebootRequired {
		t.Error("config.txt and cmdline.txt are read at boot; the operator has to be told to reboot")
	}
	if len(got.Changed) == 0 {
		t.Fatal("the repair reported nothing changed on a node that needed all of it")
	}
	// The operator is about to be asked to reboot; they are owed a sentence
	// about what for, not a diff.
	joined := strings.Join(got.Changed, "\n")
	for _, want := range []string{"enable_uart=1", "disable-bt", "serial login console"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the change list does not mention %q:\n%s", want, joined)
		}
	}
	if len(got.Remaining) != 0 {
		t.Errorf("problems left unrepaired: %v", got.Remaining)
	}

	config := readTestFile(t, filepath.Join(dir, "config.txt"))
	if !strings.Contains(config, "\nenable_uart=1\n") || !strings.Contains(config, "dtoverlay=disable-bt") {
		t.Errorf("config.txt not repaired:\n%s", config)
	}
	if !strings.Contains(config, "dtparam=audio=on") {
		t.Errorf("the repair ate the operator's own config.txt lines:\n%s", config)
	}
	if cmdline := readTestFile(t, filepath.Join(dir, "cmdline.txt")); strings.Contains(cmdline, "console=serial0") {
		t.Errorf("serial console still on the kernel command line: %q", cmdline)
	}

	// Masked, not disabled: a disabled serial-getty is still startable, and it
	// is exactly the kind of unit a generator recreates for a console the kernel
	// reports. Masked, it cannot come back and take the port from a node on the
	// air.
	if len(masked) == 0 {
		t.Fatal("the serial login service was left able to start")
	}
	for _, u := range masked {
		if !strings.HasPrefix(u, "serial-getty@") {
			t.Errorf("masked %q, which is not a serial login service", u)
		}
	}
}

func TestEnableModemUARTIsIdempotent(t *testing.T) {
	s, _ := newSystem(t)
	stockBoot(t, s)
	var masked []string
	gettyRunner(s, &masked)

	if _, err := s.EnableModemUART(context.Background(), privhelper.EnableModemUARTRequest{}); err != nil {
		t.Fatal(err)
	}
	// Second run: the getty now reports masked, so nothing is left to do.
	s.Run = func(ctx context.Context, stdin []byte, name string, args ...string) (string, error) {
		if name == "systemctl" && len(args) >= 1 && args[0] == "is-enabled" {
			return "masked\n", nil
		}
		return "", nil
	}
	got, err := s.EnableModemUART(context.Background(), privhelper.EnableModemUARTRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if got.RebootRequired || len(got.Changed) != 0 {
		t.Errorf("a second run on an already-correct node reported work: %+v", got)
	}
}

func TestEnableModemUARTOnAHostWithNoBootPartition(t *testing.T) {
	// A dev box or a container. Not an error: there is no UART to free, and
	// reporting that as a failure turns "does not apply to you" into "something
	// went wrong".
	s, _ := newSystem(t)
	got, err := s.EnableModemUART(context.Background(), privhelper.EnableModemUARTRequest{})
	if err != nil {
		t.Fatalf("a host with no boot partition returned an error: %v", err)
	}
	if got.Applicable || got.RebootRequired || len(got.Changed) != 0 {
		t.Fatalf("a host with no boot partition was repaired: %+v", got)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
