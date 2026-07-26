package sysprov

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/KN4OQW/waypoint/internal/netconfig"
	"github.com/KN4OQW/waypoint/internal/privhelper"
)

const (
	// nmKeyfileDir is where NetworkManager keeps system connection profiles. This
	// package writes profiles here directly rather than driving `nmcli connection
	// add`, for one reason: nmcli takes the PSK as a command-line argument, and
	// /proc/<pid>/cmdline is readable by other local users for the life of the
	// process. A 0600 keyfile is never readable by anyone but root.
	nmKeyfileDir = "/etc/NetworkManager/system-connections"

	// apProfile is the setup access point's profile. The waypoint- prefix is what
	// netconfig's keyfile checkpoint treats as managed, so these profiles are the
	// ones a rollback restores and hand-made profiles are left alone.
	apProfile = "waypoint-setup-ap"

	// defaultAPAddress is the node's address on its own access point.
	//
	// It is NetworkManager's shared-mode default range. That is not cosmetic: NM
	// starts the DHCP server itself and hands out 10.42.0.x, so choosing a
	// different address here would mean the node answering on one subnet while its
	// own clients were leased another.
	defaultAPAddress = "10.42.0.1/24"

	// nmDnsmasqSharedDir holds drop-ins for the dnsmasq NetworkManager runs in
	// shared mode. NM reads every .conf here and passes it to that dnsmasq.
	nmDnsmasqSharedDir = "/etc/NetworkManager/dnsmasq-shared.d"
	nmDnsmasqSharedAP  = nmDnsmasqSharedDir + "/waypoint-setup-ap.conf"

	defaultJoinTimeout = 45 * time.Second
)

// APUp raises the setup access point — the only way a headless node with no Wi-Fi
// credentials is reachable at all.
func (s *System) APUp(ctx context.Context, req privhelper.APUpRequest) (privhelper.APUpResponse, error) {
	if err := s.enter(ctx, req); err != nil {
		return privhelper.APUpResponse{}, err
	}
	defer s.mu.Unlock()

	iface := req.Interface
	if iface == "" {
		iface = s.wirelessDevice(ctx)
	}
	if iface == "" {
		return privhelper.APUpResponse{}, privhelper.Errorf(privhelper.CodeUnsupported,
			"this node has no wireless interface, so it cannot raise a setup access point")
	}
	addr := req.AddressCIDR
	if addr == "" {
		addr = defaultAPAddress
	}
	s.setRegulatoryDomain(ctx, req.Country)

	if err := s.writeDnsmasqSharedConf(addr); err != nil {
		return privhelper.APUpResponse{}, err
	}
	if err := s.checkDHCPPortFree(ctx); err != nil {
		return privhelper.APUpResponse{}, err
	}

	body := renderAPKeyfile(apProfile, iface, addr, req)
	changed, err := s.writeProfile(ctx, apProfile, body)
	if err != nil {
		return privhelper.APUpResponse{}, err
	}

	resp := privhelper.APUpResponse{SSID: req.SSID, Interface: iface, AddressCIDR: addr, Active: true}
	if changed || !s.connectionActive(ctx, apProfile) {
		if _, err := s.run(ctx, nil, "nmcli", "connection", "up", apProfile); err != nil {
			return privhelper.APUpResponse{}, internalf("raise the setup access point: %v", err)
		}
		resp.Changed = true
		s.logf("sysprov: setup AP %q up on %s (%s)", req.SSID, iface, addr)
	}
	// After the interface exists, not before: the sysctl key is per-interface and
	// does not appear until NetworkManager has brought the device up in AP mode.
	s.confineAPSegment(ctx, iface)
	return resp, nil
}

// writeDnsmasqSharedConf configures the dnsmasq NetworkManager runs for the AP.
//
// Two settings, each load-bearing:
//
//   - port=0 turns dnsmasq's DNS server off while leaving its DHCP server on.
//     waypointd runs the captive portal's own responder on port 53, and two
//     processes cannot both have it. Letting dnsmasq keep DNS would work for the
//     hijack, but it would put the portal's behaviour in a config file this
//     package writes blind rather than in code with tests around it.
//   - dhcp-option=114 is RFC 8910: it hands the portal URL to the client in its
//     DHCP lease. A modern phone that reads it opens the sheet without waiting to
//     notice that its connectivity probe came back wrong, which is the difference
//     between the wizard appearing immediately and appearing after a probe cycle.
func (s *System) writeDnsmasqSharedConf(addrCIDR string) error {
	gateway := addrCIDR
	if i := strings.IndexByte(gateway, '/'); i > 0 {
		gateway = gateway[:i]
	}
	body := managedHeader("setup access point DHCP") +
		"\n# waypointd serves DNS for the captive portal; dnsmasq does DHCP only.\n" +
		"port=0\n\n" +
		"# RFC 8910: tell the client where the portal is, in its lease.\n" +
		"dhcp-option=114,http://" + gateway + "/\n"

	if _, err := s.writeFileIfChanged(s.path(nmDnsmasqSharedAP), []byte(body), 0o644); err != nil {
		return internalf("write %s: %v", nmDnsmasqSharedAP, err)
	}
	return nil
}

// checkDHCPPortFree refuses to raise the AP when something else already holds the
// DHCP port.
//
// NetworkManager's shared mode starts its own dnsmasq for DHCP. An incumbent
// dnsmasq bound to the wildcard 0.0.0.0:67 — which Pi-Star and WPSD both run, and
// which a node migrating to Waypoint therefore has — makes that instance fail to
// bind, and NM reports it as "IP configuration could not be reserved (no
// available address, timeout, etc.)". That message sends an operator looking for
// an address-pool problem that does not exist.
//
// port=0 in the shared drop-in solves the DNS half of the same collision;
// nothing in the drop-in can solve the DHCP half, because the conflict is the
// other process's wildcard bind. bind-dynamic does not help either — tried on the
// bench, the wildcard still wins. So this says plainly what is wrong and what to
// do about it, which is the only thing that actually helps.
func (s *System) checkDHCPPortFree(ctx context.Context) error {
	out, err := s.run(ctx, nil, "ss", "-lunHp", "sport", "=", ":67")
	if err != nil || strings.TrimSpace(out) == "" {
		return nil // nothing listening, or ss unavailable — let NM try
	}
	holder := "another process"
	if i := strings.Index(out, `users:(("`); i >= 0 {
		if j := strings.Index(out[i+9:], `"`); j > 0 {
			holder = out[i+9 : i+9+j]
		}
	}
	return privhelper.Errorf(privhelper.CodeConflict,
		"cannot raise the setup access point: %s already has the DHCP port (67), so NetworkManager's "+
			"shared mode cannot start its own. Stop it (for example `systemctl stop dnsmasq`) or configure "+
			"it with bind-interfaces so it does not hold the wildcard address", holder)
}

// confineAPSegment stops the node routing anything off the setup AP.
//
// The AP is a way to reach this node, not a way to reach the internet through it.
// NetworkManager's shared mode enables forwarding and adds a masquerade rule
// because it assumes you want the second thing; an unattended open access point
// that routes traffic is a very different object from one that serves a setup
// page, and it is not the one Waypoint should be.
//
// The per-interface sysctl is the primary control and always available. The
// firewall rule is belt and braces for a kernel where the global forwarding
// setting overrides it, and is best-effort because nft and iptables are not both
// present on every image.
func (s *System) confineAPSegment(ctx context.Context, iface string) {
	key := "net.ipv4.conf." + strings.ReplaceAll(iface, ".", "/") + ".forwarding"
	if _, err := s.run(ctx, nil, "sysctl", "-w", key+"=0"); err != nil {
		s.logf("sysprov: could not disable forwarding on %s (%v); the AP may route traffic", iface, err)
	} else {
		s.logf("sysprov: forwarding disabled on %s — the setup AP reaches this node only", iface)
	}

	if _, err := s.run(ctx, nil, "nft", "add", "rule", "inet", "filter", "forward",
		"iifname", iface, "drop"); err == nil {
		return
	}
	if _, err := s.run(ctx, nil, "iptables", "-I", "FORWARD", "1", "-i", iface, "-j", "DROP"); err != nil {
		s.logf("sysprov: no packet filter available to fence the AP segment (%v); the sysctl above is the only control", err)
	}
}

// APDown tears the setup access point down. It does not delete the profile: an
// operator whose join fails needs the AP to come back, and re-raising a profile
// that is still there is one call rather than a rebuild.
func (s *System) APDown(ctx context.Context, req privhelper.APDownRequest) (privhelper.APDownResponse, error) {
	if err := s.enter(ctx, req); err != nil {
		return privhelper.APDownResponse{}, err
	}
	defer s.mu.Unlock()

	if !s.connectionActive(ctx, apProfile) {
		return privhelper.APDownResponse{Active: false, Changed: false}, nil
	}
	if _, err := s.run(ctx, nil, "nmcli", "connection", "down", apProfile); err != nil {
		return privhelper.APDownResponse{}, internalf("take the setup access point down: %v", err)
	}
	s.logf("sysprov: setup AP down")
	return privhelper.APDownResponse{Active: false, Changed: true}, nil
}

// NetJoin writes a managed profile for the operator's network and brings it up,
// waiting for the association to succeed or the timeout to expire.
func (s *System) NetJoin(ctx context.Context, req privhelper.NetJoinRequest) (privhelper.NetJoinResponse, error) {
	if err := s.enter(ctx, req); err != nil {
		return privhelper.NetJoinResponse{}, err
	}
	defer s.mu.Unlock()

	iface := req.Interface
	if iface == "" {
		iface = s.wirelessDevice(ctx)
	}
	if iface == "" {
		return privhelper.NetJoinResponse{}, privhelper.Errorf(privhelper.CodeUnsupported,
			"this node has no wireless interface")
	}
	s.setRegulatoryDomain(ctx, req.Country)

	profile := profileName(req.SSID)
	body := renderClientKeyfile(profile, iface, req)
	if _, err := s.writeProfile(ctx, profile, body); err != nil {
		return privhelper.NetJoinResponse{}, err
	}

	timeout := defaultJoinTimeout
	if req.TimeoutSeconds > 0 {
		timeout = time.Duration(req.TimeoutSeconds) * time.Second
	}
	// nmcli's own --wait is the authoritative timer: it returns when the
	// connection is up or when it gives up, so there is no polling loop racing it.
	secs := fmt.Sprintf("%d", int(timeout.Seconds()))
	if _, err := s.run(ctx, nil, "nmcli", "--wait", secs, "connection", "up", profile); err != nil {
		return privhelper.NetJoinResponse{
			SSID: req.SSID, Interface: iface, Profile: profile, Connected: false,
		}, joinFailure(req.SSID, err)
	}

	// The join worked, so the profile becomes one the node rejoins on boot. Doing
	// it now rather than at write time is what kept NM from racing the activation
	// above.
	if _, err := s.run(ctx, nil, "nmcli", "connection", "modify", profile,
		"connection.autoconnect", "yes"); err != nil {
		s.logf("sysprov: could not enable autoconnect on %q (the node will not rejoin on boot): %v", profile, err)
	}

	resp := privhelper.NetJoinResponse{
		SSID: req.SSID, Interface: iface, Profile: profile, Connected: true,
		IPv4: s.connectionIPv4(ctx, profile),
	}
	s.logf("sysprov: joined %q on %s (%s)", req.SSID, iface, resp.IPv4)
	return resp, nil
}

// joinFailure turns nmcli's account of a failed association into one an operator
// can act on.
//
// A rejected passphrase does not say so. NetworkManager stores the key, tries it,
// is refused by the access point, and then asks for a *new* secret; nmcli has no
// agent to ask, so it reports "password ... not given in 'passwd-file'" and
// "Secrets were required, but not provided". Both were verified on the bench
// against a real access point with a deliberately wrong key, with NM logging
// `need-auth -> failed (reason 'no-secrets')` at the same moment. Left as-is, the
// operator is told their password was not supplied when what happened is that it
// was wrong.
func joinFailure(ssid string, err error) error {
	msg := err.Error()
	if strings.Contains(msg, "no-secrets") ||
		strings.Contains(msg, "Secrets were required") ||
		strings.Contains(msg, "passwd-file") {
		return privhelper.Errorf(privhelper.CodeInvalidArgument,
			"%q refused the passphrase; check it and try again", ssid)
	}
	if strings.Contains(msg, "Timeout expired") || strings.Contains(msg, "timeout") {
		return privhelper.Errorf(privhelper.CodeConflict,
			"could not reach %q before the timeout — it may be out of range or not broadcasting", ssid)
	}
	return internalf("join %q: %v", ssid, err)
}

// writeProfile writes a NetworkManager keyfile and reloads NM if it changed.
//
// 0600 and root-owned is not optional: NetworkManager refuses to load a system
// connection whose file is group- or world-readable, precisely because it holds
// the PSK.
func (s *System) writeProfile(ctx context.Context, name, body string) (bool, error) {
	path := filepath.Join(s.path(nmKeyfileDir), name+".nmconnection")
	changed, err := s.writeFileIfChanged(path, []byte(body), 0o600)
	if err != nil {
		return false, internalf("write %s: %v", path, err)
	}
	if !changed {
		return false, nil
	}
	if _, err := s.run(ctx, nil, "nmcli", "connection", "reload"); err != nil {
		return true, internalf("reload NetworkManager profiles: %v", err)
	}
	// `nmcli connection reload` returns before NetworkManager has finished
	// re-reading the directory, so an activation issued immediately afterwards can
	// name a profile NM does not know yet. Waiting is cheap and removes a race
	// that would otherwise depend on how busy the node is.
	s.waitForProfile(ctx, name)
	return true, nil
}

// profileWait bounds how long to wait for NetworkManager to ingest a profile.
// The observed delay is milliseconds; this is generous enough to cover a loaded
// Pi Zero without turning a genuine failure into a long stall.
const profileWait = 5 * time.Second

// waitForProfile blocks until NetworkManager reports the profile, or the wait
// expires. Expiry is not an error: the activation that follows will produce a
// better message than anything this could say.
func (s *System) waitForProfile(ctx context.Context, name string) {
	deadline := time.Now().Add(profileWait)
	for time.Now().Before(deadline) {
		if out, err := s.run(ctx, nil, "nmcli", "-g", "connection.id", "connection", "show", name); err == nil {
			if strings.TrimSpace(out) == name {
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
	s.logf("sysprov: NetworkManager has not reported profile %q after %s; activating anyway", name, profileWait)
}

// wirelessDevice returns the first wifi device NetworkManager reports, or "".
func (s *System) wirelessDevice(ctx context.Context) string {
	out, err := s.run(ctx, nil, "nmcli", "-t", "-f", "DEVICE,TYPE", "device")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(strings.TrimSpace(line), ":")
		if len(f) == 2 && f[1] == "wifi" {
			return f[0]
		}
	}
	return ""
}

// connectionActive reports whether a profile is currently up.
func (s *System) connectionActive(ctx context.Context, name string) bool {
	out, err := s.run(ctx, nil, "nmcli", "-t", "-f", "NAME", "connection", "show", "--active")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == name {
			return true
		}
	}
	return false
}

// connectionIPv4 returns the address a profile acquired, if any.
func (s *System) connectionIPv4(ctx context.Context, name string) string {
	out, err := s.run(ctx, nil, "nmcli", "-g", "IP4.ADDRESS", "connection", "show", name)
	if err != nil {
		return ""
	}
	return trimLine(out)
}

// setRegulatoryDomain applies the operator's country to the radio. It is
// best-effort: on hardware where the domain is fixed in firmware the command
// fails, and failing a join over that would be wrong.
func (s *System) setRegulatoryDomain(ctx context.Context, country string) {
	if country == "" {
		return
	}
	if _, err := s.run(ctx, nil, "iw", "reg", "set", strings.ToUpper(country)); err != nil {
		s.logf("sysprov: could not set the regulatory domain to %q: %v", country, err)
	}
}

// --- checkpoints ---------------------------------------------------------

// checkpoint returns the rollback backend, built on the first use.
//
// It is netconfig's keyfile checkpoint, not a second implementation: that backend
// already snapshots exactly the waypoint-* profiles this package writes, already
// restores them and reloads NM, and is already covered by netconfig's own tests.
// Two implementations of "undo the network change" is one more than a node should
// have.
func (s *System) checkpoint() netconfig.Checkpoint {
	s.cpOnce.Do(func() {
		// The backend lists the directory on every Create, so a node that has
		// never had a managed profile written would fail its first checkpoint.
		// NetworkManager's own mode for this directory is 0755; the secrets are in
		// the 0600 files inside it.
		if err := os.MkdirAll(s.path(nmKeyfileDir), 0o755); err != nil {
			s.logf("sysprov: could not create %s: %v", nmKeyfileDir, err)
		}
		s.cp = netconfig.NewKeyfileCheckpoint(s.path(nmKeyfileDir), func(name string, args ...string) (string, error) {
			// netconfig's Runner has no context; these are file restores and an
			// nmcli reload, both bounded and quick.
			return s.run(context.Background(), nil, name, args...)
		})
		if s.handles == nil {
			s.handles = map[string]string{}
		}
	})
	return s.cp
}

// NetCheckpointCreate snapshots the managed profiles.
func (s *System) NetCheckpointCreate(ctx context.Context, req privhelper.NetCheckpointCreateRequest) (privhelper.NetCheckpointCreateResponse, error) {
	if err := s.enter(ctx, req); err != nil {
		return privhelper.NetCheckpointCreateResponse{}, err
	}
	defer s.mu.Unlock()

	window := time.Duration(req.TimeoutSeconds) * time.Second
	backend, err := s.checkpoint().Create(window)
	if err != nil {
		return privhelper.NetCheckpointCreateResponse{}, internalf("create a network checkpoint: %v", err)
	}
	// Callers get our handle, not the backend's. The indirection means a handle
	// arriving over the socket is looked up in a map this process owns rather than
	// passed through to the backend, so no string a client sends reaches it.
	s.nextCP++
	handle := fmt.Sprintf("cp-%d", s.nextCP)
	s.handles[handle] = backend

	s.logf("sysprov: network checkpoint %s (%s window)", handle, window)
	return privhelper.NetCheckpointCreateResponse{
		Handle:    handle,
		ExpiresAt: time.Now().Add(window).UTC(),
	}, nil
}

// NetCheckpointDestroy discards a snapshot, making the change permanent.
func (s *System) NetCheckpointDestroy(ctx context.Context, req privhelper.NetCheckpointDestroyRequest) (privhelper.NetCheckpointDestroyResponse, error) {
	if err := s.enter(ctx, req); err != nil {
		return privhelper.NetCheckpointDestroyResponse{}, err
	}
	defer s.mu.Unlock()

	backend, err := s.takeHandle(req.Handle)
	if err != nil {
		return privhelper.NetCheckpointDestroyResponse{}, err
	}
	if err := s.checkpoint().Destroy(backend); err != nil {
		return privhelper.NetCheckpointDestroyResponse{}, internalf("destroy checkpoint %s: %v", req.Handle, err)
	}
	s.logf("sysprov: network checkpoint %s destroyed", req.Handle)
	return privhelper.NetCheckpointDestroyResponse{Destroyed: true}, nil
}

// NetCheckpointRollback restores a snapshot.
func (s *System) NetCheckpointRollback(ctx context.Context, req privhelper.NetCheckpointRollbackRequest) (privhelper.NetCheckpointRollbackResponse, error) {
	if err := s.enter(ctx, req); err != nil {
		return privhelper.NetCheckpointRollbackResponse{}, err
	}
	defer s.mu.Unlock()

	backend, err := s.takeHandle(req.Handle)
	if err != nil {
		return privhelper.NetCheckpointRollbackResponse{}, err
	}
	if err := s.checkpoint().Rollback(backend); err != nil {
		return privhelper.NetCheckpointRollbackResponse{}, internalf("roll back checkpoint %s: %v", req.Handle, err)
	}
	s.logf("sysprov: network checkpoint %s rolled back", req.Handle)
	return privhelper.NetCheckpointRollbackResponse{RolledBack: true}, nil
}

// takeHandle resolves and consumes a handle. A handle is single-use in both
// directions: "I destroyed it" and "it was never there" mean different things to a
// caller reconciling state after a crash.
func (s *System) takeHandle(h string) (string, error) {
	s.checkpoint() // ensure the map exists
	backend, ok := s.handles[h]
	if !ok {
		return "", privhelper.Errorf(privhelper.CodeNotFound, "no such checkpoint %q", h)
	}
	delete(s.handles, h)
	return backend, nil
}

// --- keyfile rendering ---------------------------------------------------

// renderAPKeyfile builds the setup access point's profile.
func renderAPKeyfile(name, iface, addr string, req privhelper.APUpRequest) string {
	var b strings.Builder
	b.WriteString(managedHeader("setup access point"))
	fmt.Fprintf(&b, "\n[connection]\nid=%s\ntype=wifi\ninterface-name=%s\nautoconnect=false\n",
		name, kfEscape(iface))
	// band=bg pins 2.4 GHz: the operator's phone has to see this network, and a
	// Pi Zero W has no 5 GHz radio to offer anyway.
	fmt.Fprintf(&b, "\n[wifi]\nmode=ap\nssid=%s\nband=bg\n", kfEscape(req.SSID))
	if req.Channel > 0 {
		fmt.Fprintf(&b, "channel=%d\n", req.Channel)
	}
	if req.Passphrase != "" {
		fmt.Fprintf(&b, "\n[wifi-security]\nkey-mgmt=wpa-psk\nproto=rsn\npsk=%s\n", kfEscape(req.Passphrase))
	}
	// method=shared is what gives associating clients an address and a route
	// without a separate dnsmasq to configure and keep running.
	fmt.Fprintf(&b, "\n[ipv4]\nmethod=shared\naddress1=%s\n", addr)
	b.WriteString("\n[ipv6]\nmethod=ignore\n")
	return b.String()
}

// renderClientKeyfile builds the profile for the operator's own network.
func renderClientKeyfile(name, iface string, req privhelper.NetJoinRequest) string {
	var b strings.Builder
	b.WriteString(managedHeader("wi-fi client profile"))
	// autoconnect is off until the join is verified.
	//
	// NetworkManager auto-activates a new profile the moment it reads it —
	// observed on the bench as "policy: auto-activating connection" arriving
	// before our own `connection up` had been issued. Two activations of the same
	// profile racing each other makes the outcome depend on which one NM finishes
	// first, and a profile that failed to associate would then keep retrying on
	// its own for as long as it exists. It is switched on after the join is
	// verified, so the node still rejoins on boot.
	fmt.Fprintf(&b, "\n[connection]\nid=%s\ntype=wifi\ninterface-name=%s\nautoconnect=false\n",
		name, kfEscape(iface))
	fmt.Fprintf(&b, "\n[wifi]\nmode=infrastructure\nssid=%s\n", kfEscape(req.SSID))
	if req.Hidden {
		b.WriteString("hidden=true\n")
	}
	if req.PSK != "" {
		fmt.Fprintf(&b, "\n[wifi-security]\nkey-mgmt=wpa-psk\npsk=%s\n", kfEscape(req.PSK))
	}
	b.WriteString("\n[ipv4]\nmethod=auto\n")
	b.WriteString("\n[ipv6]\nmethod=auto\n")
	return b.String()
}

// profileName turns an SSID into a managed profile name.
//
// The SSID reaches this having passed validation, so it holds no control
// characters — but it can still hold a slash or a dot, and this name becomes a
// filename. Everything outside a conservative set is replaced, which makes path
// traversal through an SSID structurally impossible rather than merely unlikely.
func profileName(ssid string) string {
	var b strings.Builder
	b.WriteString("waypoint-")
	for _, r := range ssid {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// kfEscape escapes a value for NetworkManager's keyfile format. Backslashes are
// the escape character, and a leading space would be eaten, so both are handled;
// the validators upstream have already excluded newlines and control characters.
func kfEscape(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	if strings.HasPrefix(v, " ") {
		v = `\s` + strings.TrimPrefix(v, " ")
	}
	return v
}
