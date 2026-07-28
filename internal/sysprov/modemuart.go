package sysprov

import (
	"context"
	"os"
	"path/filepath"

	"github.com/KN4OQW/waypoint/internal/bootcfg"
	"github.com/KN4OQW/waypoint/internal/privhelper"
)

// EnableModemUART frees the Raspberry Pi's PL011 UART on GPIO 14/15 for the
// modem (#18).
//
// The diagnosis and the file edits live in internal/bootcfg, which is pure
// enough to test against a fixture boot partition. What is here is the part that
// needs root and systemd: pointing bootcfg at the real boot directory, and
// masking the serial login services so nothing starts one later.
//
// Masking rather than disabling is deliberate. `systemctl disable` stops a unit
// being started at boot but leaves it startable — and serial-getty is exactly
// the kind of unit something else re-enables, because generators create it on
// demand for a console the kernel reports. Masked, it cannot come back and take
// the port from a node that is on the air.
func (s *System) EnableModemUART(ctx context.Context, req privhelper.EnableModemUARTRequest) (privhelper.EnableModemUARTResponse, error) {
	if err := s.enter(ctx, req); err != nil {
		return privhelper.EnableModemUARTResponse{}, err
	}
	defer s.mu.Unlock()

	opts := s.bootOptions(ctx)
	before := bootcfg.Inspect(opts)
	if !before.Applicable {
		s.logf("enable_modem_uart: no Raspberry Pi boot partition; nothing to free")
		return privhelper.EnableModemUARTResponse{Applicable: false}, nil
	}

	after, err := bootcfg.Apply(opts, &gettyMasker{s: s, ctx: ctx})
	if err != nil {
		return privhelper.EnableModemUARTResponse{}, privhelper.Errorf(privhelper.CodeInternal,
			"freeing the modem UART: %v", err)
	}

	resp := privhelper.EnableModemUARTResponse{
		Applicable:     true,
		RebootRequired: after.RebootRequired,
		Changed:        changedFrom(before, after),
	}
	for _, p := range after.Problems {
		resp.Remaining = append(resp.Remaining, p.ID)
	}
	s.logf("enable_modem_uart: changed=%v reboot=%v remaining=%v", resp.Changed, resp.RebootRequired, resp.Remaining)
	return resp, nil
}

// bootOptions points bootcfg at the real boot partition, honouring System.Root
// so the file-writing behaviour is exercisable against a temp directory without
// a Raspberry Pi.
func (s *System) bootOptions(ctx context.Context) bootcfg.Options {
	o := bootcfg.Options{
		UnitEnabled: func(unit string) bool { return s.unitPresent(ctx, unit) },
	}
	if s.Root != "" {
		// Mirror bootcfg's own search order under the test root.
		for _, d := range []string{"/boot/firmware", "/boot"} {
			candidate := filepath.Join(s.Root, d)
			if _, err := os.Stat(filepath.Join(candidate, "config.txt")); err == nil {
				o.BootDir = candidate
				break
			}
		}
		if o.BootDir == "" {
			o.BootDir = filepath.Join(s.Root, "boot", "firmware")
		}
	}
	return o
}

// unitPresent reports whether a serial getty is enabled or running. Anything
// other than a clean "disabled"/"masked"/"not-found" counts, because the states
// in between (static, indirect, generated) are all ones systemd will happily
// start a getty from.
func (s *System) unitPresent(ctx context.Context, unit string) bool {
	out, err := s.run(ctx, nil, "systemctl", "is-enabled", unit)
	if err != nil && out == "" {
		return false
	}
	switch trimLine(out) {
	case "", "masked", "masked-runtime", "disabled", "not-found":
		return false
	}
	return true
}

// gettyMasker is bootcfg.Applier over systemd.
type gettyMasker struct {
	s   *System
	ctx context.Context
}

func (g *gettyMasker) DisableGetty(units []string) error {
	for _, u := range units {
		// Best-effort per unit: a node may have only one of the two, and
		// "no such unit" is the expected answer for the other. Failing the
		// whole repair over a unit that does not exist would leave the boot
		// files edited and the operator told it did not work.
		_, _ = g.s.run(g.ctx, nil, "systemctl", "stop", u)
		if _, err := g.s.run(g.ctx, nil, "systemctl", "mask", u); err != nil {
			g.s.logf("enable_modem_uart: mask %s: %v", u, err)
		}
	}
	return nil
}

// changedFrom describes the repair in the operator's terms rather than as a
// diff. They are about to be asked to reboot; they are owed a sentence about
// what for.
func changedFrom(before, after bootcfg.Report) []string {
	var out []string
	if !before.EnableUART && after.EnableUART {
		out = append(out, "config.txt: enable_uart=1 — the GPIO serial port is now on")
	}
	if !before.BluetoothOff && after.BluetoothOff {
		out = append(out, "config.txt: dtoverlay=disable-bt — Bluetooth no longer owns the modem's UART")
	}
	if before.SerialConsole && !after.SerialConsole {
		out = append(out, "cmdline.txt: the serial login console was removed")
	}
	if before.GettyEnabled {
		out = append(out, "the serial login service was masked")
	}
	return out
}
