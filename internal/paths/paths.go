// Package paths is the one place Waypoint's on-disk layout is written down.
//
// Before this package the layout was a string literal repeated across thirty-odd
// flag defaults, six per-mode const blocks in the renderer, eleven systemd units
// and the image build. Nothing enforced agreement between them, and nothing made
// the layout reviewable as a whole — which is how the tree ended up rooted at a
// directory named for a user that has never existed on a Waypoint node (the image
// ships no account at all; see the image module's start_chroot_script). Every
// path below now has exactly one definition.
//
// The split that matters operationally: StateDir is what an operator must back up
// and what has to survive an update, BinDir is a managed artifact the updater
// replaces atomically and that a re-flash is free to discard.
package paths

// StateDir is the root of everything the node keeps across reboots: the config
// store, the rendered daemon configs, TLS material, the peering keypair, and the
// provisioned marker.
//
// /var/lib is the FHS home for exactly this, and it is on the rootfs so an A/B
// slot switch (RFC-0017) carries it. It is also where part of this tree already
// lived: internal/provision has kept the provisioned marker here since
// first-boot setup existed, so consolidating ends a split where a node's state
// sat in two unrelated directories — one of them named for a user account that
// has never existed on a Waypoint node (the image ships none at all; see the
// image module's start_chroot_script).
const StateDir = "/var/lib/waypoint"

// Managed subdirectories of StateDir.
const (
	// EtcDir holds the rendered daemon configs and the cached hostlists. These
	// are compiled *outputs* of the store (RFC-0001) — regenerated on every
	// apply, never parsed back — so the directory is disposable in a way the
	// rest of StateDir is not.
	EtcDir = StateDir + "/etc"

	// TLSDir holds the self-signed device cert/key minted on first start
	// (RFC-0012); ACMEDir caches Let's Encrypt material when the operator has
	// pointed a real hostname at the node instead.
	TLSDir  = StateDir + "/tls"
	ACMEDir = StateDir + "/acme"

	// PeeringDir holds the node's own peering keypair and its pinned peer certs
	// (RFC-0016). Regenerating the keypair invalidates every pairing at once,
	// which is why this is state and not a rendered output.
	PeeringDir = StateDir + "/peering"

	// OverridesDir is the root of the operator's override drop-ins
	// (<dir>/<daemon>.d/*.conf, RFC-0005). Operator-authored, so it is state: an
	// apply merges it into the render and never rewrites it.
	OverridesDir = StateDir + "/overrides.d"
)

// StorePath is the SQLite configuration store — the authoritative copy of every
// setting on the node, and the one file whose loss is not recoverable by
// re-rendering.
const StorePath = StateDir + "/config.db"

// EventsPath is the persistent last-heard and event history (RFC-0004). It is a
// StorePath sibling but a separate database on purpose: it is append-heavy,
// pruned on a retention window, and losing it costs history rather than
// configuration.
const EventsPath = StateDir + "/events.db"

// UpdateMarker records an update that has been swapped but not yet confirmed, so
// the ExecStartPre boot check can revert it after a power loss (RFC-0014).
const UpdateMarker = StateDir + "/update.marker"

// BinDir holds the waypointd binary the updater manages: it stages, atomically
// renames and (on a failed health check) restores this file, so it is a managed
// artifact rather than node state, and it is deliberately not under StateDir —
// an executable does not belong in /var/lib, and a backup of StateDir should not
// carry a binary. /usr/local/lib is the FHS location for locally-installed
// program files, and it shares a filesystem with the staging path, which the
// swap requires: it is a rename(2).
//
// The stack DAEMONS are not here; they live in /usr/bin, owned by the
// waypoint-stack .debs. Only waypointd updates itself.
const (
	BinDir = "/usr/local/lib/waypoint/bin"
	Binary = BinDir + "/waypointd"
)

// LegacyStateDir is where StateDir lived through the 0.2 series. Nodes flashed
// with those images still have their data here, and their systemd units will
// name this path for the rest of their lives — the image ships the units, and
// neither the binary updater (RFC-0014) nor the apt-based stack updater delivers
// unit files. Migrate turns it into a symlink to StateDir so they keep
// resolving; see migrate.go for why that is the safe direction.
//
// Fresh images do not create it. It is equal to StateDir until the move lands,
// at which point Migrate starts doing real work.
const LegacyStateDir = "/home/pi-star/waypoint"
