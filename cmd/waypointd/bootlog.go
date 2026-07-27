package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// The first boot of a freshly flashed node is the one moment with no way to
// observe it.
//
// The image ships no login account (the operator creates one during setup), the
// serial console is disabled because the MMDVM modem owns the UART, and the
// journal is Storage=volatile so it lives in RAM and dies with the power. If the
// setup access point does not come up, every channel for finding out why is one
// the access point was supposed to provide. The node is simply dark, and the
// natural next step — power-cycling it — destroys the only copy of the evidence.
//
// So the setup path leaves a breadcrumb on the FAT boot partition. That is the
// one filesystem an operator can read by moving the card to any laptop, without
// mounting ext4 and without this node booting at all. It records what setup tried
// and what happened, and nothing else: it is a diagnostic of last resort, not a
// second log.

// bootLogDir is the FAT boot partition. Absent on a dev box, in CI, and in the
// container tests — where the journal is readable and this file has no purpose,
// so every function here degrades to a no-op rather than erroring.
const bootLogDir = "/boot/firmware"

// bootLogName is deliberately obvious to somebody who has just put a
// non-booting card into their laptop and is looking for a reason.
const bootLogName = "waypoint-setup.log"

// bootLogMax bounds the file. A node that reboot-loops would otherwise fill the
// boot partition, which is small and also holds the kernel — so past a limit the
// log restarts rather than grows. The most recent boot is the interesting one.
const bootLogMax = 32 << 10

var bootLogMu sync.Mutex

// bootLogf appends one timestamped line to the boot-partition setup log.
//
// It never returns an error and never blocks startup: this exists to explain a
// failure, so it must not become one. If the boot partition is absent or
// read-only the line goes to the journal alone, which is where it was going
// anyway.
func bootLogf(format string, args ...any) {
	path := filepath.Join(bootLogDir, bootLogName)
	if _, err := os.Stat(bootLogDir); err != nil {
		return // not a node; the journal is readable here
	}

	bootLogMu.Lock()
	defer bootLogMu.Unlock()

	flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if fi, err := os.Stat(path); err == nil && fi.Size() > bootLogMax {
		flags = os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o644)
	if err != nil {
		return
	}
	// Discarded explicitly: the content is already synced below, so a Close error
	// has nothing left to tell us about a log written to explain something else.
	defer func() { _ = f.Close() }()

	line := fmt.Sprintf(format, args...)
	// UTC and RFC3339: a node that has never had a network has never seen NTP, so
	// the stamp may be nonsense in absolute terms. It is still ordered, which is
	// what reading a boot log needs.
	if _, err := fmt.Fprintf(f, "%s  %s\n", time.Now().UTC().Format(time.RFC3339), line); err != nil {
		return
	}
	// Synced because the failure this log describes is frequently followed by the
	// operator pulling the power, and an unflushed explanation is no explanation.
	_ = f.Sync()
}

// bootLogBanner opens a boot's worth of entries, so consecutive boots are
// distinguishable in a file that survives them.
func bootLogBanner(version string) {
	bootLogf("---- waypointd %s starting; node is not set up yet ----", version)
}

// bootLogAndPrint writes to both sinks. Most setup milestones want to be in the
// journal for somebody watching live and on the card for somebody who was not.
func bootLogAndPrint(format string, args ...any) {
	log.Printf("waypointd: "+format, args...)
	bootLogf(format, args...)
}
