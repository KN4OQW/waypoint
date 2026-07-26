package privhelper

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"strings"
	"testing"
)

// key builds a structurally valid ed25519 authorized_keys line: the algorithm
// name, the wire-framed 32-byte public key, a comment.
func key(seed byte, comment string) string {
	pub := make([]byte, 32)
	for i := range pub {
		pub[i] = seed + byte(i)
	}
	return keyWithAlgo("ssh-ed25519", "ssh-ed25519", pub, comment)
}

// keyWithAlgo lets a test declare one algorithm and embed another, which is the
// mangled-paste case ValidateSSHKey is meant to catch.
func keyWithAlgo(declared, embedded string, pub []byte, comment string) string {
	blob := make([]byte, 0, 8+len(embedded)+len(pub))
	blob = binary.BigEndian.AppendUint32(blob, uint32(len(embedded)))
	blob = append(blob, embedded...)
	blob = binary.BigEndian.AppendUint32(blob, uint32(len(pub)))
	blob = append(blob, pub...)
	out := declared + " " + base64.StdEncoding.EncodeToString(blob)
	if comment != "" {
		out += " " + comment
	}
	return out
}

// Property 1: the hostname rule accepts what an operator would sensibly type and
// refuses the shipped defaults. Refusing the default is the point of the wizard
// step — two nodes that both kept it collide on the same .local address.
func TestValidateHostname(t *testing.T) {
	valid := []string{
		"hs-shack", "shack", "w1aw", "waypoint-north", "a", "N0CALL",
		strings.Repeat("a", 63),
	}
	for _, n := range valid {
		if err := ValidateHostname(n); err != nil {
			t.Errorf("ValidateHostname(%q) = %v, want nil", n, err)
		}
	}

	invalid := map[string]string{
		"empty":            "",
		"blank":            "   ",
		"default":          "raspberrypi",
		"default cased":    "RaspberryPi",
		"other default":    "raspberry",
		"daemon fallback":  "waypoint",
		"fallback cased":   "WAYPOINT",
		"localhost":        "localhost",
		"dotted":           "shack.local",
		"fqdn":             "shack.example.com",
		"leading hyphen":   "-shack",
		"trailing hyphen":  "shack-",
		"underscore":       "hs_shack",
		"space":            "hs shack",
		"all digits":       "12345",
		"too long":         strings.Repeat("a", 64),
		"shell metachar":   "shack;reboot",
		"slash":            "shack/../etc",
		"newline injected": "shack\nraspberrypi",
	}
	for name, n := range invalid {
		t.Run(name, func(t *testing.T) {
			err := ValidateHostname(n)
			if err == nil {
				t.Fatalf("ValidateHostname(%q) = nil, want an error", n)
			}
			if got := CodeOf(err); got != CodeInvalidArgument {
				t.Errorf("code = %q, want %q", got, CodeInvalidArgument)
			}
		})
	}

	// The dotted case earns a specific message, because "type shack, not
	// shack.local" is the correction the operator needs.
	err := ValidateHostname("shack.local")
	if !strings.Contains(err.Error(), `"shack"`) {
		t.Errorf("the dotted-hostname error does not suggest the bare name: %v", err)
	}
}

// Property 2: the username rule refuses root, refuses system accounts, and
// enforces the shape useradd will accept.
func TestValidateUsername(t *testing.T) {
	for _, n := range []string{"rescue", "w1aw", "clint", "_svc", "op-2", "a", strings.Repeat("u", 32)} {
		if err := ValidateUsername(n); err != nil {
			t.Errorf("ValidateUsername(%q) = %v, want nil", n, err)
		}
	}

	invalid := map[string]string{
		"empty":           "",
		"root":            "root",
		"root cased":      "Root",
		"root shouty":     "ROOT",
		"daemon":          "daemon",
		"nobody":          "nobody",
		"www-data":        "www-data",
		"sshd":            "sshd",
		"mosquitto":       "mosquitto",
		"leading digit":   "1user",
		"leading hyphen":  "-user",
		"uppercase":       "Rescue",
		"space":           "res cue",
		"dot":             "res.cue",
		"slash":           "../root",
		"too long":        strings.Repeat("u", 33),
		"shell metachar":  "rescue;id",
		"newline":         "rescue\nroot",
		"colon separator": "rescue:x:0:0",
	}
	for name, n := range invalid {
		t.Run(name, func(t *testing.T) {
			err := ValidateUsername(n)
			if err == nil {
				t.Fatalf("ValidateUsername(%q) = nil, want an error", n)
			}
			if got := CodeOf(err); got != CodeInvalidArgument {
				t.Errorf("code = %q, want %q", got, CodeInvalidArgument)
			}
		})
	}

	// root gets its own message: the operator needs to know why, not just that.
	err := ValidateUsername("root")
	if !strings.Contains(err.Error(), "root is locked") && !strings.Contains(err.Error(), "must not be root") {
		t.Errorf("the root rejection does not explain itself: %v", err)
	}
}

// Property 3: the password rule blocks the chpasswd injection. chpasswd reads
// "user:password" lines, so a newline in this field sets a second account's
// password and a colon truncates this one.
func TestValidatePassword(t *testing.T) {
	if err := ValidatePassword("correct horse battery"); err != nil {
		t.Errorf("a reasonable password was rejected: %v", err)
	}

	invalid := map[string]string{
		"too short":       "short7",
		"newline":         "hunter22\nroot:owned",
		"carriage return": "hunter22\rroot:owned",
		"colon":           "hunter:22extra",
		"nul byte":        "hunter22\x00",
		"control char":    "hunter22\x07bell",
		"too long":        strings.Repeat("p", 513),
	}
	for name, pw := range invalid {
		t.Run(name, func(t *testing.T) {
			if err := ValidatePassword(pw); err == nil {
				t.Fatal("accepted")
			} else if got := CodeOf(err); got != CodeInvalidArgument {
				t.Errorf("code = %q, want %q", got, CodeInvalidArgument)
			}
		})
	}
}

// Property 4: the SSH key rule accepts a real key line and refuses the things
// that would corrupt authorized_keys or install something unusable.
func TestValidateSSHKey(t *testing.T) {
	good := key(1, "operator@laptop")
	if err := ValidateSSHKey(good); err != nil {
		t.Fatalf("a valid ed25519 key was rejected: %v", err)
	}
	// A key with no comment is still a key.
	if err := ValidateSSHKey(key(2, "")); err != nil {
		t.Errorf("a commentless key was rejected: %v", err)
	}
	// Surrounding whitespace is the normal result of a copy-paste.
	if err := ValidateSSHKey("  " + good + "  "); err != nil {
		t.Errorf("a whitespace-padded key was rejected: %v", err)
	}

	pub := make([]byte, 32)
	invalid := map[string]string{
		"empty":                      "",
		"blank":                      "   ",
		"private key":                "-----BEGIN OPENSSH PRIVATE KEY-----\nabc\n-----END",
		"two lines":                  good + "\n" + key(5, "second"),
		"trailing newline injection": good + "\nssh-ed25519 AAAA attacker",
		"command option":             `command="/bin/sh" ` + good,
		"from option":                `from="10.0.0.1" ` + good,
		"no-pty option":              "no-pty," + good,
		"type only":                  "ssh-ed25519",
		"not base64":                 "ssh-ed25519 not!valid!base64 comment",
		"truncated base64":           "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5 comment",
		"dsa":                        keyWithAlgo("ssh-dss", "ssh-dss", pub, ""),
		"unknown type":               keyWithAlgo("ssh-magic", "ssh-magic", pub, ""),
		"type mismatch":              keyWithAlgo("ssh-ed25519", "ssh-rsa", pub, ""),
		"too long":                   "ssh-ed25519 " + strings.Repeat("A", 17<<10),
	}
	for name, k := range invalid {
		t.Run(name, func(t *testing.T) {
			err := ValidateSSHKey(k)
			if err == nil {
				t.Fatalf("accepted %.60q", k)
			}
			if got := CodeOf(err); got != CodeInvalidArgument {
				t.Errorf("code = %q, want %q", got, CodeInvalidArgument)
			}
		})
	}

	// The multi-line rejection is the load-bearing one; make sure it says so.
	err := ValidateSSHKey(good + "\n" + good)
	if !strings.Contains(err.Error(), "single line") {
		t.Errorf("the multi-line rejection does not explain itself: %v", err)
	}
}

// Property 5: Fingerprint produces the OpenSSH form over the decoded blob, and
// distinguishes keys.
func TestFingerprint(t *testing.T) {
	k := key(1, "operator@laptop")
	fp, err := Fingerprint(k)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(fp, "SHA256:") {
		t.Fatalf("Fingerprint = %q, want a SHA256: prefix", fp)
	}
	if strings.HasSuffix(fp, "=") {
		t.Errorf("Fingerprint = %q, want the unpadded base64 ssh-keygen prints", fp)
	}

	// Computed independently: SHA-256 over the decoded blob, unpadded base64.
	blob, err := base64.StdEncoding.DecodeString(strings.Fields(k)[1])
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(blob)
	if want := "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:]); fp != want {
		t.Errorf("Fingerprint = %q, want %q", fp, want)
	}

	// The comment is not part of the identity: the same key with a different
	// comment is the same key.
	same, err := Fingerprint(key(1, "operator@phone"))
	if err != nil {
		t.Fatal(err)
	}
	if same != fp {
		t.Error("the comment changed the fingerprint")
	}
	other, err := Fingerprint(key(9, "operator@laptop"))
	if err != nil {
		t.Fatal(err)
	}
	if other == fp {
		t.Error("two different keys share a fingerprint")
	}

	if _, err := Fingerprint("nonsense"); err == nil {
		t.Error("Fingerprint accepted a non-key")
	}
}

// Property 6: the Wi-Fi rules match what the radio and NetworkManager will accept.
func TestValidateSSIDAndPassphrase(t *testing.T) {
	if err := ValidateSSID("home-network"); err != nil {
		t.Errorf("ValidateSSID: %v", err)
	}
	if err := ValidateSSID(strings.Repeat("s", 32)); err != nil {
		t.Errorf("a 32-byte SSID was rejected: %v", err)
	}
	for name, s := range map[string]string{
		"empty":    "",
		"33 bytes": strings.Repeat("s", 33),
		"control":  "home\x00network",
		"newline":  "home\nnetwork",
	} {
		t.Run("ssid "+name, func(t *testing.T) {
			if err := ValidateSSID(s); err == nil {
				t.Fatal("accepted")
			}
		})
	}

	if err := ValidateWiFiPassphrase("12345678"); err != nil {
		t.Errorf("the minimum-length passphrase was rejected: %v", err)
	}
	if err := ValidateWiFiPassphrase(strings.Repeat("p", 63)); err != nil {
		t.Errorf("the maximum-length passphrase was rejected: %v", err)
	}
	// A raw 64-hex-digit PMK is what the standard allows in place of a passphrase.
	if err := ValidateWiFiPassphrase(strings.Repeat("a1", 32)); err != nil {
		t.Errorf("a hex PSK was rejected: %v", err)
	}
	for name, p := range map[string]string{
		"too short": "1234567",
		"too long":  strings.Repeat("p", 64),
		"control":   "pass\x01word",
	} {
		t.Run("passphrase "+name, func(t *testing.T) {
			if err := ValidateWiFiPassphrase(p); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

// Property 7: the AP address must be a host address inside its prefix. A gateway
// on the network or broadcast address produces an AP clients associate with and
// then cannot route through.
func TestAPUpRequestValidate(t *testing.T) {
	ok := APUpRequest{SSID: "waypoint-setup", Passphrase: "setup12345", Country: "US",
		Channel: 6, AddressCIDR: "192.168.66.1/24"}
	if err := ok.Validate(); err != nil {
		t.Fatalf("a reasonable AP request was rejected: %v", err)
	}

	bad := map[string]APUpRequest{
		"no ssid":           {},
		"short passphrase":  {SSID: "s", Passphrase: "short"},
		"bad country":       {SSID: "s", Country: "USA"},
		"5 GHz channel":     {SSID: "s", Channel: 36},
		"channel zero ok":   {SSID: "s", Channel: -1},
		"not a cidr":        {SSID: "s", AddressCIDR: "192.168.66.1"},
		"ipv6":              {SSID: "s", AddressCIDR: "fd00::1/64"},
		"network address":   {SSID: "s", AddressCIDR: "192.168.66.0/24"},
		"broadcast address": {SSID: "s", AddressCIDR: "192.168.66.255/24"},
		"no room":           {SSID: "s", AddressCIDR: "192.168.66.1/31"},
	}
	for name, req := range bad {
		t.Run(name, func(t *testing.T) {
			if err := req.Validate(); err == nil {
				t.Fatal("accepted")
			} else if got := CodeOf(err); got != CodeInvalidArgument {
				t.Errorf("code = %q, want %q", got, CodeInvalidArgument)
			}
		})
	}
}

// Property 8: a recovery user with neither a password nor a key is refused. It
// would be an account nobody can log in as — the exact failure the account exists
// to prevent.
func TestCreateRecoveryUserValidate(t *testing.T) {
	if err := (CreateRecoveryUserRequest{Username: "rescue"}).Validate(); err == nil {
		t.Error("a recovery user with no password and no key was accepted")
	}
	if err := (CreateRecoveryUserRequest{Username: "rescue", SSHKey: key(1, "")}).Validate(); err != nil {
		t.Errorf("a key-only recovery user was rejected: %v", err)
	}
	if err := (CreateRecoveryUserRequest{Username: "rescue", Password: "correct horse"}).Validate(); err != nil {
		t.Errorf("a password recovery user was rejected: %v", err)
	}
	if err := (CreateRecoveryUserRequest{Username: "root", Password: "correct horse"}).Validate(); err == nil {
		t.Error("root was accepted as the recovery user")
	}
}

// Property 9: String redacts. Logging a request must not put the operator's
// password or PSK in the journal.
func TestRequestsRedactSecrets(t *testing.T) {
	const secret = "correct horse battery staple"

	user := CreateRecoveryUserRequest{Username: "rescue", Password: secret, SSHKey: key(1, ""), Sudo: true}
	if s := user.String(); strings.Contains(s, secret) {
		t.Errorf("CreateRecoveryUserRequest.String leaked the password: %s", s)
	} else if !strings.Contains(s, "REDACTED") || !strings.Contains(s, "rescue") {
		t.Errorf("CreateRecoveryUserRequest.String is unhelpful: %s", s)
	}

	ap := APUpRequest{SSID: "waypoint-setup", Passphrase: secret}
	if s := ap.String(); strings.Contains(s, secret) {
		t.Errorf("APUpRequest.String leaked the passphrase: %s", s)
	}

	join := NetJoinRequest{SSID: "home", PSK: secret}
	if s := join.String(); strings.Contains(s, secret) {
		t.Errorf("NetJoinRequest.String leaked the PSK: %s", s)
	}
	// An open network reads as open rather than as a redacted empty secret.
	if s := (NetJoinRequest{SSID: "home"}).String(); !strings.Contains(s, "open") {
		t.Errorf("an open network does not read as open: %s", s)
	}
}

// Property 10: a checkpoint must have a positive, bounded window. A checkpoint
// with no window is not a rollback, and an hours-long one is a stuck node.
func TestNetCheckpointCreateValidate(t *testing.T) {
	if err := (NetCheckpointCreateRequest{TimeoutSeconds: 90}).Validate(); err != nil {
		t.Errorf("a 90s window was rejected: %v", err)
	}
	for name, secs := range map[string]int{"zero": 0, "negative": -1, "over the cap": 3601} {
		t.Run(name, func(t *testing.T) {
			if err := (NetCheckpointCreateRequest{TimeoutSeconds: secs}).Validate(); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}
