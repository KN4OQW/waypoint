package sysprov

import (
	"context"
	"os"

	"github.com/KN4OQW/waypoint/internal/privhelper"
)

const (
	// sshdDropInDir is the directory Debian's sshd_config includes from, at the
	// very top of the file. sshd takes the *first* value it sees for a keyword, so
	// a drop-in here wins over the distribution's own setting — which is why this
	// is the right place to write and /etc/ssh/sshd_config is not.
	sshdDropInDir = "/etc/ssh/sshd_config.d"

	// Two files rather than one. EnableSSH and LockRoot are independent calls
	// that can be made in either order and re-run separately; sharing a file would
	// mean each had to preserve the other's setting on every write.
	passwordAuthDropIn = sshdDropInDir + "/60-waypoint-passwordauth.conf"
	rootLoginDropIn    = sshdDropInDir + "/61-waypoint-rootlogin.conf"

	// sshUnit is the SSH server's unit on Debian. sshd.service is an alias.
	sshUnit = "ssh"
)

// EnableSSH turns the SSH server on or off, and optionally sets whether it
// accepts passwords.
//
// The drop-in is written before the service is touched, so a restart never picks
// up a half-applied policy. A nil PasswordAuth leaves the current file alone —
// including leaving no file at all, so a caller that only wanted to start the
// service does not silently adopt an opinion about password logins.
func (s *System) EnableSSH(ctx context.Context, req privhelper.EnableSSHRequest) (privhelper.EnableSSHResponse, error) {
	if err := s.enter(ctx, req); err != nil {
		return privhelper.EnableSSHResponse{}, err
	}
	defer s.mu.Unlock()

	changed := false
	if req.PasswordAuth != nil {
		body := "PasswordAuthentication no\n"
		if *req.PasswordAuth {
			body = "PasswordAuthentication yes\n"
		}
		c, err := s.writeSSHDropIn(ctx, passwordAuthDropIn, "password authentication", body)
		if err != nil {
			return privhelper.EnableSSHResponse{}, err
		}
		changed = changed || c
	}

	svcChanged, err := s.setSSHService(ctx, req.Enabled)
	if err != nil {
		return privhelper.EnableSSHResponse{}, err
	}
	changed = changed || svcChanged

	return privhelper.EnableSSHResponse{
		Enabled:      req.Enabled,
		PasswordAuth: s.passwordAuthEnabled(),
		Changed:      changed,
	}, nil
}

// setSSHService enables or disables the SSH unit, reporting whether it had to.
func (s *System) setSSHService(ctx context.Context, enabled bool) (bool, error) {
	if !s.hasSystemd() {
		// A container has systemctl on PATH and nothing behind it. The drop-in is
		// the durable half of this call and has already been written; saying so
		// beats failing a provisioning step over a service manager that is not
		// running.
		s.logf("sysprov: not running under systemd; left the %s service alone", sshUnit)
		return false, nil
	}
	active := s.unitEnabled(ctx)
	if active == enabled {
		return false, nil
	}
	verb := "disable"
	if enabled {
		verb = "enable"
	}
	if _, err := s.run(ctx, nil, "systemctl", verb, "--now", sshUnit); err != nil {
		return false, internalf("%s the %s service: %v", verb, sshUnit, err)
	}
	s.logf("sysprov: %sd the %s service", verb, sshUnit)
	return true, nil
}

// unitEnabled reports whether the SSH unit is enabled at boot.
func (s *System) unitEnabled(ctx context.Context) bool {
	out, err := s.run(ctx, nil, "systemctl", "is-enabled", sshUnit)
	if err != nil {
		return false
	}
	switch trimLine(out) {
	case "enabled", "enabled-runtime", "static", "alias", "indirect":
		return true
	}
	return false
}

// writeSSHDropIn writes one managed sshd drop-in and reloads sshd if the content
// changed. Reloading rather than restarting matters: a restart drops the SSH
// session the operator may be using to watch the setup happen.
func (s *System) writeSSHDropIn(ctx context.Context, path, what, body string) (bool, error) {
	full := s.path(path)
	content := []byte(managedHeader(what) + body)
	changed, err := s.writeFileIfChanged(full, content, 0o644)
	if err != nil {
		return false, internalf("write %s: %v", path, err)
	}
	if !changed {
		return false, nil
	}
	s.logf("sysprov: wrote %s (%s)", path, what)

	if !s.hasSystemd() {
		return true, nil
	}
	// A config error would leave sshd refusing to start — discovered after a
	// reboot, with no other way in — so the file is checked before anything is
	// asked to load it. sshd -t validates the whole assembled configuration,
	// drop-ins included.
	if err := s.validateSSHConfig(ctx, full, content); err != nil {
		return false, err
	}
	if _, err := s.run(ctx, nil, "systemctl", "reload-or-restart", sshUnit); err != nil {
		return true, internalf("reload %s: %v", sshUnit, err)
	}
	return true, nil
}

// validateSSHConfig runs sshd -t and, if it fails, works out whose fault it is.
//
// A bare "sshd -t failed" is not enough to act on. sshd refuses to run at all
// when its privilege-separation directory is missing or its host keys have not
// been generated, and neither has anything to do with the drop-in just written —
// so the check is differential: remove the file and ask again. If sshd is still
// unhappy the problem predates us and the drop-in stays; if sshd is happy without
// it, the drop-in really is broken and stays removed.
//
// Getting this wrong in the other direction is the dangerous one: deleting a
// valid PermitRootLogin drop-in because sshd could not start for an unrelated
// reason would silently undo the hardening the operator just asked for.
func (s *System) validateSSHConfig(ctx context.Context, path string, content []byte) error {
	if _, err := s.run(ctx, nil, "sshd", "-t"); err == nil {
		return nil
	}
	if err := os.Remove(path); err != nil {
		return internalf("sshd rejected the configuration and %s could not be removed: %v", path, err)
	}
	if _, err := s.run(ctx, nil, "sshd", "-t"); err != nil {
		// Broken with or without us. Restore the drop-in and say so rather than
		// discarding a change the operator asked for over an unrelated fault.
		if werr := s.writeFile(path, content, 0o644); werr != nil {
			return internalf("restore %s: %v", path, werr)
		}
		s.logf("sysprov: sshd -t fails for reasons unrelated to %s (%v); the drop-in was kept", path, err)
		return nil
	}
	return internalf("the sshd configuration %s produces is invalid, so it was removed", path)
}

// passwordAuthEnabled reports the effective setting from the drop-in this package
// owns. With no drop-in the answer is sshd's own default, which on Debian is yes.
func (s *System) passwordAuthEnabled() bool {
	b, err := os.ReadFile(s.path(passwordAuthDropIn))
	if err != nil {
		return true
	}
	return !containsDirective(string(b), "PasswordAuthentication no")
}
