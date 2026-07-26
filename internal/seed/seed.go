// Package seed is the power-user fast path: a TOML file on the boot partition
// that provisions a node without anyone touching the wizard.
//
// The wizard exists because a node flashed with dd has no other way to be
// configured. That is the right default and the wrong workflow for someone
// bringing up their fifth node, or building a batch for a club, or reflashing the
// same box while chasing a bug. Those operators have the card in a reader anyway;
// letting them drop a file on it turns twenty minutes of phone-and-captive-portal
// into a boot.
//
// It is a fast path, not a back door. Every field goes through the same wizard
// steps the interactive flow uses, so the same validators run, the same layered
// checks guard the root lock, and the same marker is written. What it skips is
// the access point and the typing — not the rules.
//
// It also does not claim the node. Provisioning says what the box is; claiming
// says who administers it, and handing that to whoever wrote a file on the card
// would make the claim gate decorative. The operator still meets the claim screen
// over HTTPS, on the hostname the file chose.
package seed

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/KN4OQW/waypoint/internal/privhelper"
)

// DefaultPaths are searched in order, first match wins.
//
// waypoint.toml comes first so an operator can carry Waypoint's own settings
// alongside a custom.toml written by Raspberry Pi Imager for something else,
// without the two fighting. /boot and /boot/firmware are both checked because
// Raspberry Pi OS moved the boot mount between releases and a card written on one
// may be booted by the other.
var DefaultPaths = []string{
	"/boot/firmware/waypoint.toml",
	"/boot/waypoint.toml",
	"/boot/firmware/custom.toml",
	"/boot/custom.toml",
}

// SupportedConfigVersion is the schema this build understands.
const SupportedConfigVersion = 1

// File is the accepted subset of Raspberry Pi's custom.toml.
//
// It is deliberately the same shape, key for key, rather than a Waypoint-shaped
// format that happens to be TOML. An operator who already has a custom.toml — from
// Imager, from a provisioning script, from a colleague — should be able to drop it
// on the card and have it mean what it says. Keys outside this subset are ignored
// rather than refused, because a file that also configures things Waypoint has no
// opinion about is a normal file, not a broken one.
type File struct {
	ConfigVersion int `toml:"config_version"`

	System struct {
		Hostname string `toml:"hostname"`
	} `toml:"system"`

	User struct {
		Name string `toml:"name"`
		// Password is plaintext when PasswordEncrypted is false, and a crypt(3)
		// hash when it is true. The hash is strongly preferred: this file sits on a
		// FAT partition that anybody who takes the card can read.
		Password          string `toml:"password"`
		PasswordEncrypted bool   `toml:"password_encrypted"`
	} `toml:"user"`

	SSH struct {
		Enabled *bool `toml:"enabled"`
		// PasswordAuthentication is tri-state: absent means "leave sshd's setting
		// alone", matching the wizard.
		PasswordAuthentication *bool    `toml:"password_authentication"`
		AuthorizedKeys         []string `toml:"authorized_keys"`
	} `toml:"ssh"`

	WLAN struct {
		SSID string `toml:"ssid"`
		// Password is a passphrase, or the 64-hex PSK when PasswordEncrypted is
		// true. Both are accepted by the same validator.
		Password          string `toml:"password"`
		PasswordEncrypted bool   `toml:"password_encrypted"`
		Hidden            bool   `toml:"hidden"`
		Country           string `toml:"country"`
	} `toml:"wlan"`
}

// ErrAbsent means no seed file was found, which is the normal case.
var ErrAbsent = errors.New("seed: no provisioning file on the boot partition")

// Find returns the first seed file that exists, or ErrAbsent.
func Find(paths []string) (string, error) {
	if len(paths) == 0 {
		paths = DefaultPaths
	}
	for _, p := range paths {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p, nil
		}
	}
	return "", ErrAbsent
}

// Load reads and validates a seed file.
func Load(path string) (*File, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrAbsent
		}
		return nil, fmt.Errorf("seed: read %s: %w", path, err)
	}

	var f File
	if err := toml.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("seed: %s is not valid TOML: %w", path, err)
	}
	if err := f.Validate(); err != nil {
		return nil, fmt.Errorf("seed: %s: %w", path, err)
	}
	return &f, nil
}

// Validate checks the file describes a node this build can actually provision.
//
// It fails rather than half-applying. A seed file is written by somebody who is
// not watching the boot, so a partial application — hostname set, no account, root
// unlocked — would produce a node in a state nobody chose and nobody is present to
// notice. Refusing means the node falls back to the wizard, which is a visible,
// recoverable outcome.
func (f *File) Validate() error {
	if f.ConfigVersion != 0 && f.ConfigVersion != SupportedConfigVersion {
		return fmt.Errorf("config_version %d is not supported by this build (it understands %d)",
			f.ConfigVersion, SupportedConfigVersion)
	}

	if strings.TrimSpace(f.System.Hostname) == "" {
		return errors.New("[system] hostname is required — it is the one setting first-boot setup exists to change")
	}
	if err := privhelper.ValidateHostname(f.System.Hostname); err != nil {
		return unwrapCoded(err)
	}

	if strings.TrimSpace(f.User.Name) == "" {
		return errors.New("[user] name is required — the node needs an account that can recover it once root is locked")
	}
	if err := privhelper.ValidateUsername(f.User.Name); err != nil {
		return unwrapCoded(err)
	}

	keys := f.Keys()
	switch {
	case f.User.Password == "" && len(keys) == 0:
		return fmt.Errorf("[user] %q would have neither a password nor an SSH key and could never log in; "+
			"set [user] password, or [ssh] authorized_keys", f.User.Name)
	case f.User.Password != "" && f.User.PasswordEncrypted:
		if err := privhelper.ValidatePasswordHash(f.User.Password); err != nil {
			return unwrapCoded(err)
		}
	case f.User.Password != "":
		if err := privhelper.ValidatePassword(f.User.Password); err != nil {
			return unwrapCoded(err)
		}
	}
	for i, k := range keys {
		if err := privhelper.ValidateSSHKey(k); err != nil {
			return fmt.Errorf("[ssh] authorized_keys[%d]: %w", i, unwrapCoded(err))
		}
	}

	if f.WLAN.SSID != "" {
		if err := privhelper.ValidateSSID(f.WLAN.SSID); err != nil {
			return unwrapCoded(err)
		}
		if f.WLAN.Password != "" {
			if err := privhelper.ValidateWiFiPassphrase(f.WLAN.Password); err != nil {
				return unwrapCoded(err)
			}
		}
	} else if f.WLAN.Password != "" {
		return errors.New("[wlan] password is set but ssid is not")
	}
	return nil
}

// Keys returns the authorized keys, trimmed and with blanks dropped. A trailing
// empty element is what a hand-edited TOML array usually grows.
func (f *File) Keys() []string {
	var out []string
	for _, k := range f.SSH.AuthorizedKeys {
		if k = strings.TrimSpace(k); k != "" {
			out = append(out, k)
		}
	}
	return out
}

// HasWiFi reports whether the file asks for a wireless join.
func (f *File) HasWiFi() bool { return strings.TrimSpace(f.WLAN.SSID) != "" }

// PasswordAuth resolves the sshd password-authentication setting.
//
// Absent means "leave it alone" only when the account has a password to use. For
// a key-only account the answer is no: leaving passwords enabled on a node whose
// only account cannot use one is an open door with nothing behind it.
func (f *File) PasswordAuth() *bool {
	if f.SSH.PasswordAuthentication != nil {
		return f.SSH.PasswordAuthentication
	}
	if f.User.Password == "" {
		no := false
		return &no
	}
	return nil
}

// Describe renders the file for a log line, without its secrets.
func (f *File) Describe() string {
	cred := "ssh key"
	switch {
	case f.User.Password != "" && f.User.PasswordEncrypted:
		cred = "password hash"
	case f.User.Password != "":
		cred = "password"
	}
	if f.User.Password != "" && len(f.Keys()) > 0 {
		cred += " + ssh key"
	}
	wifi := "no wi-fi"
	if f.HasWiFi() {
		wifi = fmt.Sprintf("wi-fi %q", f.WLAN.SSID)
	}
	return fmt.Sprintf("hostname %q, user %q (%s), %d key(s), %s",
		f.System.Hostname, f.User.Name, cred, len(f.Keys()), wifi)
}

// unwrapCoded strips a privhelper error's wire prefix. These messages are read by
// an operator squinting at a boot log, not by a client switching on a code.
func unwrapCoded(err error) error {
	msg := strings.TrimPrefix(err.Error(), "privhelper: ")
	if i := strings.Index(msg, ": "); i > 0 && strings.HasPrefix(msg, string(privhelper.CodeOf(err))) {
		msg = msg[i+2:]
	}
	return errors.New(msg)
}
