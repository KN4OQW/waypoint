package sysprov

import (
	"context"
	"os"
	"strings"

	"github.com/KN4OQW/waypoint/internal/privhelper"
)

const (
	etcHostname = "/etc/hostname"
	etcHosts    = "/etc/hosts"
	// hostsSelfIP is the address Debian points at the machine's own name. It is
	// 127.0.1.1 rather than 127.0.0.1 so that resolving the hostname does not
	// collide with localhost.
	hostsSelfIP = "127.0.1.1"
)

// SetHostname renames the host.
//
// The rename is only half the job. A Debian box whose /etc/hosts still names the
// old host is the one where sudo pauses for ten seconds before every command,
// because it cannot resolve the name it was just given — so the hosts line is
// updated in the same call, and the operator never meets that symptom.
func (s *System) SetHostname(ctx context.Context, req privhelper.SetHostnameRequest) (privhelper.SetHostnameResponse, error) {
	if err := s.enter(ctx, req); err != nil {
		return privhelper.SetHostnameResponse{}, err
	}
	defer s.mu.Unlock()

	prev := s.currentHostname(ctx)
	resp := privhelper.SetHostnameResponse{Previous: prev, Hostname: req.Hostname}

	if prev != req.Hostname {
		if err := s.setHostname(ctx, req.Hostname); err != nil {
			return resp, err
		}
		resp.Changed = true
		s.logf("sysprov: hostname %q -> %q", prev, req.Hostname)
	}

	if req.UpdateHosts {
		changed, err := s.updateHostsFile(prev, req.Hostname)
		if err != nil {
			return resp, err
		}
		resp.Changed = resp.Changed || changed
	}
	if resp.Changed {
		s.refreshMDNS(ctx, req.Hostname)
	}
	return resp, nil
}

// refreshMDNS makes Avahi announce the name the operator just chose.
//
// This is the difference between a node the operator can find and one they
// cannot. Setup tells them where the node will be — "continue at
// <name>.local" — and mDNS is what makes that true. Avahi reads the hostname
// when it starts and does not reliably notice a later hostnamectl change, so
// without this it keeps announcing the name the image shipped with. On the bench
// that meant a node set up as KN4OQW was still published as waypointos.local:
// the address the wizard had just told the operator to use did not exist, and a
// Wi-Fi join that had actually succeeded was indistinguishable from one that had
// failed.
//
// Best-effort by design. A node with no Avahi is reachable by address, and a node
// whose hostname is set but whose mDNS is stale is still a working node — neither
// is worth failing a hostname change that has already been applied.
func (s *System) refreshMDNS(ctx context.Context, name string) {
	if !s.hasSystemd() {
		return
	}
	// try-restart, not restart: it does nothing if Avahi is not running, so this
	// never starts a daemon on a node whose operator disabled it.
	if _, err := s.run(ctx, nil, "systemctl", "try-restart", "avahi-daemon.service"); err != nil {
		s.logf("sysprov: could not restart avahi-daemon, so %q.local may not resolve until reboot: %v", name, err)
		return
	}
	s.logf("sysprov: restarted avahi-daemon so %q.local resolves", name)
}

// currentHostname reads the static hostname, preferring hostnamectl and falling
// back to /etc/hostname.
func (s *System) currentHostname(ctx context.Context) string {
	if out, err := s.run(ctx, nil, "hostnamectl", "--static"); err == nil {
		if n := strings.TrimSpace(out); n != "" {
			return n
		}
	}
	b, err := os.ReadFile(s.path(etcHostname))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// setHostname applies the new name.
//
// hostnamectl is the right call on a node: it updates the static, transient, and
// pretty names together and notifies anything listening. It is not available in a
// container built from a Debian image — systemd-hostnamed is not running — so the
// fallback writes /etc/hostname and sets the transient name directly. The fallback
// is what makes this method testable outside a full VM; it is not a shortcut, it
// is the same two things hostnamectl would have done.
func (s *System) setHostname(ctx context.Context, name string) error {
	if s.hasSystemd() {
		if _, err := s.run(ctx, nil, "hostnamectl", "set-hostname", name); err != nil {
			return internalf("set hostname: %v", err)
		}
		return nil
	}
	if _, err := s.writeFileIfChanged(s.path(etcHostname), []byte(name+"\n"), 0o644); err != nil {
		return internalf("write %s: %v", etcHostname, err)
	}
	if _, err := s.run(ctx, nil, "hostname", name); err != nil {
		// The transient name is best-effort: the static name is what survives a
		// reboot, and that is already written.
		s.logf("sysprov: could not set the running hostname (the reboot will pick it up): %v", err)
	}
	return nil
}

// updateHostsFile rewrites the 127.0.1.1 line to name the host.
//
// It rewrites exactly that line and leaves every other line — including the
// operator's own entries — untouched, and appends the line if the file has none.
// Editing only what it owns is the same rule the config renderer follows.
func (s *System) updateHostsFile(prev, name string) (bool, error) {
	path := s.path(etcHosts)
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return true, s.writeFile(path, []byte(hostsSelfIP+"\t"+name+"\n"), 0o644)
		}
		return false, internalf("read %s: %v", etcHosts, err)
	}

	want := hostsSelfIP + "\t" + name
	lines := strings.Split(string(b), "\n")
	found := false
	for i, l := range lines {
		fields := strings.Fields(l)
		if len(fields) == 0 || strings.HasPrefix(strings.TrimSpace(l), "#") {
			continue
		}
		if fields[0] != hostsSelfIP {
			continue
		}
		found = true
		// Preserve any aliases the operator added beyond the hostname, replacing
		// only the old name.
		rest := make([]string, 0, len(fields))
		for _, f := range fields[1:] {
			if f == prev || f == name {
				continue
			}
			rest = append(rest, f)
		}
		lines[i] = want
		if len(rest) > 0 {
			lines[i] += "\t" + strings.Join(rest, " ")
		}
	}
	if !found {
		// Keep the trailing blank element produced by a file ending in a newline.
		if n := len(lines); n > 0 && lines[n-1] == "" {
			lines = append(lines[:n-1], want, "")
		} else {
			lines = append(lines, want)
		}
	}

	out := strings.Join(lines, "\n")
	if out == string(b) {
		return false, nil
	}
	return true, s.writeFile(path, []byte(out), 0o644)
}
