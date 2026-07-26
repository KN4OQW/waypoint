// Command waypoint-provision-helper is the root side of Waypoint's provisioning
// boundary.
//
// It runs as root, listens on a Unix socket owned root:waypoint, and performs the
// eleven operations in privhelper.Provisioner and nothing else. waypointd — which
// serves the dashboard, parses operator input, and is the process most likely to
// carry a bug — talks to it over that socket. The point of the split is that a
// flaw in the web tier reaches eleven validated calls rather than reaching root.
//
// It is normally socket-activated. systemd creates the socket with its final mode
// and group already set, so there is never an instant where a bound socket has the
// wrong permissions, and the helper is not running at all until something in the
// waypoint group connects.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/KN4OQW/waypoint/internal/privhelper"
	"github.com/KN4OQW/waypoint/internal/sysprov"
)

// Version is stamped at build time (-ldflags "-X main.Version=…").
var Version = "dev"

func main() {
	socket := flag.String("socket", privhelper.DefaultSocketPath,
		"path to the Unix socket to listen on (ignored under systemd socket activation)")
	root := flag.String("root", "", "filesystem prefix for the files this helper writes (testing only)")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(Version)
		return
	}

	logger := log.New(os.Stderr, "", 0) // journald adds its own timestamps
	if err := run(*socket, *root, logger); err != nil {
		logger.Fatalf("waypoint-provision-helper: %v", err)
	}
}

func run(socket, root string, logger *log.Logger) error {
	// Running as anything else would fail later, one confusing command at a time —
	// midway through creating an account, say. Better to say so before binding.
	if os.Geteuid() != 0 {
		return fmt.Errorf("must run as root (running as uid %d)", os.Geteuid())
	}

	prov := sysprov.New()
	prov.Root = root
	prov.Logf = logger.Printf

	srv, err := privhelper.NewServer(prov)
	if err != nil {
		return err
	}
	srv.Logf = logger.Printf

	ln, activated, err := privhelper.ListenFromSystemd()
	if err != nil {
		return err
	}
	if activated {
		logger.Printf("waypoint-provision-helper %s: serving the socket systemd passed in", Version)
	} else {
		ln, err = privhelper.Listen(socket, srv.AllowedGID)
		if err != nil {
			return err
		}
		logger.Printf("waypoint-provision-helper %s: listening on %s (0660 root:%s)",
			Version, socket, privhelper.SocketGroup)
	}
	defer closeListener(ln, activated, socket)

	// SIGTERM is what systemd sends on stop; SIGINT is an operator running it by
	// hand. Both drain in-flight calls rather than killing a half-run useradd.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := srv.Serve(ctx, ln); err != nil {
		return err
	}
	logger.Printf("waypoint-provision-helper: stopped")
	return nil
}

// closeListener tears down the socket. A socket this process created is unlinked
// on the way out; one systemd handed us belongs to systemd, which will reuse it
// for the next activation, so it is left alone.
func closeListener(ln net.Listener, activated bool, socket string) {
	if ul, ok := ln.(*net.UnixListener); ok {
		ul.SetUnlinkOnClose(!activated)
	}
	_ = ln.Close()
	if !activated {
		_ = os.Remove(socket)
	}
}
