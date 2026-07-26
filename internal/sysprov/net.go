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

	// defaultAPAddress is the node's address on its own access point. 192.168.66/24
	// is chosen to be unlikely to collide with the network the operator's phone
	// normally sits on.
	defaultAPAddress = "192.168.66.1/24"

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
	return resp, nil
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
		}, internalf("join %q: %v", req.SSID, err)
	}

	resp := privhelper.NetJoinResponse{
		SSID: req.SSID, Interface: iface, Profile: profile, Connected: true,
		IPv4: s.connectionIPv4(ctx, profile),
	}
	s.logf("sysprov: joined %q on %s (%s)", req.SSID, iface, resp.IPv4)
	return resp, nil
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
	return true, nil
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
	fmt.Fprintf(&b, "\n[connection]\nid=%s\ntype=wifi\ninterface-name=%s\nautoconnect=true\n",
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
