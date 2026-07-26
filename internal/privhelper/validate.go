package privhelper

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"net/netip"
	"regexp"
	"strings"
)

// The validators below are the argument half of the boundary. The interface makes
// the helper's surface small; these make each argument on that surface a value the
// helper can hand to useradd, hostnamectl, or nmcli without a shell-injection or
// file-injection question hanging over it.
//
// Every one of them returns a *Error with CodeInvalidArgument, so a rejection
// crosses the socket classified and reaches the operator as a message about what
// they typed rather than a 500.

// Length and format rules, as regexes where a regex is honest about the rule.
var (
	// hostnameRE is one RFC 1123 DNS label: 1–63 chars, alphanumeric at both ends,
	// hyphens allowed inside. Dots are deliberately absent — see ValidateHostname.
	hostnameRE = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)

	// usernameRE is Debian's adduser rule, widened by one character to allow a
	// leading underscore: start with a lowercase letter or underscore, then
	// lowercase letters, digits, hyphens, underscores. Capped at 32, the
	// utmp/useradd limit. Uppercase is excluded because a mixed-case account is a
	// standing source of "I can't log in" on a case-folding client.
	usernameRE = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

	// sshKeyRE splits an authorized_keys line into type, base64 blob, and an
	// optional comment. Anchored at the start so a line beginning with an options
	// field (command="…", no-pty, from="…") cannot match — options are exactly
	// what an attacker would want to smuggle in, and there is no reason for the
	// setup wizard to accept them.
	sshKeyRE = regexp.MustCompile(`^([a-z0-9][a-z0-9@.-]*)\s+([A-Za-z0-9+/]+={0,2})(?:\s+(.*))?$`)

	// hexPSKRE is a raw 256-bit PMK: exactly 64 hex digits.
	hexPSKRE = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

	// cryptHashRE is a crypt(3) password hash: $<id>$[params$]<salt>$<hash>, as
	// yescrypt ($y$), SHA-512 ($6$), SHA-256 ($5$), and bcrypt ($2b$) produce.
	// Anchored and colon-free — see ValidatePasswordHash.
	cryptHashRE = regexp.MustCompile(`^\$[0-9a-zA-Z]+\$[!-9;-~]+$`)

	// countryRE is a two-letter ISO 3166-1 regulatory domain.
	countryRE = regexp.MustCompile(`^[A-Za-z]{2}$`)
)

// ReservedHostnames are names the wizard refuses. This is the "not accepting the
// default name" rule, and it is about more than tidiness: these nodes advertise
// themselves over mDNS, so two boxes that both kept the shipped name collide on
// the same .local address and the operator reaches whichever one answered first.
//
//   - raspberrypi is Raspberry Pi OS Lite's default, which the Waypoint image does
//     not override, so it is what every freshly flashed node is called.
//   - waypoint and waypoint.local are the fallback the daemon uses when it cannot
//     read a hostname, and the name in the device certificate's SANs.
//   - localhost is not a name a host may take.
var ReservedHostnames = []string{
	"raspberrypi",
	"raspberry",
	"waypoint",
	"localhost",
}

// ReservedUsernames are account names the wizard refuses. root heads the list for
// the obvious reason; the rest are Debian system accounts whose home directories,
// shells, or group memberships already mean something, and colliding with one
// turns a recovery account into a broken system account.
//
// This list is necessary but not sufficient. "root" is uid 0, not a spelling: an
// implementation must also refuse any name that already exists in /etc/passwd and
// any name that resolves to uid 0, because a node that has been tampered with can
// have a uid-0 account called anything at all.
var ReservedUsernames = []string{
	"root", "daemon", "bin", "sys", "sync", "games", "man", "lp", "mail",
	"news", "uucp", "proxy", "www-data", "backup", "list", "irc", "gnats",
	"nobody", "systemd-network", "systemd-resolve", "systemd-timesync",
	"messagebus", "sshd", "avahi", "mosquitto", "nm-openvpn", "_apt",
}

// sshKeyTypes are the key algorithms accepted, newest first. ssh-dss is absent on
// purpose: OpenSSH disabled DSA by default years ago and removed it outright, so
// accepting one would install a key that cannot be used.
var sshKeyTypes = map[string]bool{
	"ssh-ed25519":                        true,
	"sk-ssh-ed25519@openssh.com":         true,
	"ecdsa-sha2-nistp256":                true,
	"ecdsa-sha2-nistp384":                true,
	"ecdsa-sha2-nistp521":                true,
	"sk-ecdsa-sha2-nistp256@openssh.com": true,
	"ssh-rsa":                            true,
	"rsa-sha2-256":                       true,
	"rsa-sha2-512":                       true,
}

// Limits that keep a hostile request from turning into a large file or a long
// argument list.
const (
	maxSSHKeyLen     = 16 << 10 // an RSA-8192 key with a comment is far under this
	maxPasswordLen   = 512
	minPasswordLen   = 8
	maxSSIDLen       = 32 // 802.11: an SSID is at most 32 bytes
	minPassphraseLen = 8  // WPA2-PSK
	maxPassphraseLen = 63
)

// ValidateHostname checks the hostname the operator chose for the node.
//
// It is stricter than a general DNS check in two ways, both deliberate:
//
//   - A single label. An operator who types "shack.local" wants the node called
//     shack; accepting the dotted form gives them a node that answers to
//     shack.local.local and a certificate with the wrong SANs.
//   - No reserved names. Keeping the shipped default is the specific outcome
//     first-boot setup exists to prevent (see ReservedHostnames).
func ValidateHostname(name string) error {
	n := strings.TrimSpace(name)
	if n == "" {
		return Errorf(CodeInvalidArgument, "hostname is empty")
	}
	if strings.Contains(n, ".") {
		return Errorf(CodeInvalidArgument,
			"hostname %q must be a single name, not a dotted domain — use %q, the .local suffix is added by mDNS",
			name, strings.SplitN(n, ".", 2)[0])
	}
	if len(n) > 63 {
		return Errorf(CodeInvalidArgument, "hostname %q is %d characters (max 63)", name, len(n))
	}
	if !hostnameRE.MatchString(n) {
		return Errorf(CodeInvalidArgument,
			"hostname %q may contain only letters, digits, and hyphens, and may not start or end with a hyphen", name)
	}
	if isAllDigits(n) {
		return Errorf(CodeInvalidArgument, "hostname %q is all digits, which resolvers treat as an address", name)
	}
	lower := strings.ToLower(n)
	for _, r := range ReservedHostnames {
		if lower == r {
			return Errorf(CodeInvalidArgument,
				"hostname %q is the default name — pick something unique to this node, or two Waypoints on one network answer to the same .local address", name)
		}
	}
	return nil
}

// ValidateUsername checks the recovery account's name. See ReservedUsernames for
// why passing this is necessary but not sufficient.
func ValidateUsername(name string) error {
	n := strings.TrimSpace(name)
	if n == "" {
		return Errorf(CodeInvalidArgument, "username is empty")
	}
	// Checked before the format rule so "root" gets the message about root rather
	// than a generic one, and so "Root" is caught too.
	lower := strings.ToLower(n)
	if lower == "root" {
		return Errorf(CodeInvalidArgument,
			"the recovery user must not be root — it exists so the node stays reachable after root is locked")
	}
	for _, r := range ReservedUsernames {
		if lower == r {
			return Errorf(CodeInvalidArgument, "username %q is a reserved system account", name)
		}
	}
	if !usernameRE.MatchString(n) {
		return Errorf(CodeInvalidArgument,
			"username %q must be 1–32 characters, start with a lowercase letter or underscore, and contain only lowercase letters, digits, hyphens, and underscores", name)
	}
	return nil
}

// ValidatePassword checks a password bound for chpasswd.
//
// The character rules are not password-strength theatre. chpasswd reads
// "user:password" lines from stdin, so a password containing a newline injects a
// second line — an arbitrary account's password, set by whoever filled in this
// field — and a colon truncates the one it is in. Both are refused here rather
// than left to the implementation to remember to escape.
func ValidatePassword(pw string) error {
	if len(pw) < minPasswordLen {
		return Errorf(CodeInvalidArgument, "password must be at least %d characters", minPasswordLen)
	}
	if len(pw) > maxPasswordLen {
		return Errorf(CodeInvalidArgument, "password is longer than %d characters", maxPasswordLen)
	}
	if strings.ContainsAny(pw, "\n\r") {
		return Errorf(CodeInvalidArgument, "password must not contain a line break")
	}
	if strings.Contains(pw, ":") {
		return Errorf(CodeInvalidArgument, "password must not contain a colon")
	}
	if strings.ContainsFunc(pw, isControl) {
		return Errorf(CodeInvalidArgument, "password must not contain control characters")
	}
	return nil
}

// ValidatePasswordHash checks a pre-computed crypt(3) hash.
//
// The character rules are the same injection guard as ValidatePassword and for
// the same reason: this value reaches `chpasswd -e`, which reads "user:hash"
// lines from stdin, so a colon truncates the record and a newline appends
// another one. The regex excludes both by construction rather than by a separate
// check, since every real crypt alphabet is printable ASCII without colons.
//
// It deliberately does not enumerate accepted hash identifiers. crypt(3) grows
// new ones — yescrypt replaced SHA-512 as Debian's default within this project's
// lifetime — and a node that refused a hash its own libcrypt understands would be
// rejecting a credential that works.
func ValidatePasswordHash(hash string) error {
	if hash == "" {
		return Errorf(CodeInvalidArgument, "password hash is empty")
	}
	if len(hash) > maxPasswordLen {
		return Errorf(CodeInvalidArgument, "password hash is longer than %d characters", maxPasswordLen)
	}
	if !strings.HasPrefix(hash, "$") {
		return Errorf(CodeInvalidArgument,
			"password hash must be a crypt(3) string starting with $ (openssl passwd -6 produces one); a bare password goes in the password field")
	}
	if !cryptHashRE.MatchString(hash) {
		return Errorf(CodeInvalidArgument,
			"password hash is not a valid crypt(3) string (it must contain no whitespace, colons, or line breaks)")
	}
	return nil
}

// ValidateSSHKey checks a single authorized_keys entry.
//
// The load-bearing rule is that it is *one line*. authorized_keys is
// line-delimited, so a "key" containing a newline is two entries, and the second
// one carries whatever options the submitter chose. Everything else here — the
// algorithm allowlist, the base64 decode, the check that the decoded blob's
// embedded algorithm name matches the declared one — catches the honest mistakes:
// a truncated paste, a private key pasted by accident, a key type OpenSSH will
// refuse at load time and thereby break every key in the file.
func ValidateSSHKey(key string) error {
	k := strings.TrimSpace(key)
	if k == "" {
		return Errorf(CodeInvalidArgument, "SSH key is empty")
	}
	if len(k) > maxSSHKeyLen {
		return Errorf(CodeInvalidArgument, "SSH key is longer than %d bytes", maxSSHKeyLen)
	}
	if strings.ContainsAny(k, "\n\r") {
		return Errorf(CodeInvalidArgument,
			"SSH key must be a single line — a line break would add a second authorized_keys entry")
	}
	if strings.HasPrefix(k, "-----BEGIN") {
		return Errorf(CodeInvalidArgument,
			"that is a private key, not a public key — paste the contents of the matching .pub file")
	}

	m := sshKeyRE.FindStringSubmatch(k)
	if m == nil {
		return Errorf(CodeInvalidArgument,
			"SSH key must look like \"<type> <base64> [comment]\", with no options before the type")
	}
	algo, blob64 := m[1], m[2]
	if !sshKeyTypes[algo] {
		return Errorf(CodeInvalidArgument, "SSH key type %q is not accepted", algo)
	}
	blob, err := base64.StdEncoding.DecodeString(blob64)
	if err != nil {
		return Errorf(CodeInvalidArgument, "SSH key body is not valid base64 (a truncated paste?)")
	}
	fields, ok := sshBlobFields(blob)
	if !ok {
		return Errorf(CodeInvalidArgument, "SSH key body is malformed")
	}
	// Every real public key is at least [algorithm, key material]: ed25519 carries
	// the 32-byte point, RSA the exponent and modulus, ECDSA the curve and point.
	// A body holding only the algorithm name decodes cleanly and matches its
	// declared type, so nothing but this length check catches a paste that was cut
	// off after the first field.
	if len(fields) < 2 || len(fields[1]) == 0 {
		return Errorf(CodeInvalidArgument, "SSH key body carries no key material (a truncated paste?)")
	}
	if embedded := string(fields[0]); embedded != algo {
		// Catches the classic mangled paste where the type and the body came from
		// different keys — which authorized_keys accepts and sshd then ignores.
		return Errorf(CodeInvalidArgument,
			"SSH key says %q but its body is a %q key", algo, embedded)
	}
	return nil
}

// Fingerprint returns the OpenSSH SHA256 fingerprint of a public key, in the
// "SHA256:…" form ssh-keygen -l prints, so the dashboard can show the operator the
// same string their own machine shows and they can confirm it matches.
func Fingerprint(key string) (string, error) {
	if err := ValidateSSHKey(key); err != nil {
		return "", err
	}
	m := sshKeyRE.FindStringSubmatch(strings.TrimSpace(key))
	blob, err := base64.StdEncoding.DecodeString(m[2])
	if err != nil {
		return "", Errorf(CodeInvalidArgument, "SSH key body is not valid base64")
	}
	sum := sha256.Sum256(blob)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:]), nil
}

// maxSSHBlobFields bounds the walk below. No public key format has more than
// three fields; the cap is there so a crafted blob cannot make this loop build an
// unbounded slice.
const maxSSHBlobFields = 8

// sshBlobFields splits a decoded key blob into its SSH wire fields. The format is
// a sequence of 4-byte big-endian lengths each followed by that many bytes, which
// is what lets a blob be checked against the type declared next to it.
//
// It requires the fields to consume the blob exactly: trailing bytes mean the
// thing being validated is not a key, whatever else it may be.
func sshBlobFields(blob []byte) ([][]byte, bool) {
	var out [][]byte
	for len(blob) > 0 {
		if len(blob) < 4 {
			return nil, false // a trailing partial length
		}
		n := binary.BigEndian.Uint32(blob[:4])
		// Bounded before slicing: the length comes from the submitted key.
		if uint64(n) > uint64(len(blob)-4) {
			return nil, false
		}
		out = append(out, blob[4:4+n])
		blob = blob[4+n:]
		if len(out) > maxSSHBlobFields {
			return nil, false
		}
	}
	return out, len(out) > 0
}

// ValidateSSID checks a network name against the 802.11 limit. An over-long SSID
// is silently truncated by some tooling, which produces a profile that never
// associates and no explanation of why.
func ValidateSSID(ssid string) error {
	if ssid == "" {
		return Errorf(CodeInvalidArgument, "SSID is empty")
	}
	if len(ssid) > maxSSIDLen {
		return Errorf(CodeInvalidArgument, "SSID is %d bytes (802.11 allows at most %d)", len(ssid), maxSSIDLen)
	}
	if strings.ContainsFunc(ssid, isControl) {
		return Errorf(CodeInvalidArgument, "SSID contains control characters")
	}
	return nil
}

// ValidateWiFiPassphrase checks a WPA2 credential: either an 8–63 character
// passphrase or a raw 64-hex-digit PMK, which is what the standard allows and what
// NetworkManager will accept.
func ValidateWiFiPassphrase(pass string) error {
	if hexPSKRE.MatchString(pass) {
		return nil
	}
	if len(pass) < minPassphraseLen || len(pass) > maxPassphraseLen {
		return Errorf(CodeInvalidArgument,
			"Wi-Fi passphrase must be %d–%d characters, or a 64-digit hex PSK", minPassphraseLen, maxPassphraseLen)
	}
	if strings.ContainsFunc(pass, isControl) {
		return Errorf(CodeInvalidArgument, "Wi-Fi passphrase contains control characters")
	}
	return nil
}

// validateCountry checks a regulatory domain. Empty means "leave it".
func validateCountry(c string) error {
	if c == "" {
		return nil
	}
	if !countryRE.MatchString(c) {
		return Errorf(CodeInvalidArgument, "regulatory country %q must be a 2-letter code (e.g. US)", c)
	}
	return nil
}

// validateCIDR checks the address the node takes on its own access point. It must
// be a host address inside the prefix: the network and broadcast addresses are not
// addresses a gateway can hold, and using one produces an AP clients associate with
// and then cannot route through.
func validateCIDR(cidr string) error {
	pfx, err := netip.ParsePrefix(cidr)
	if err != nil {
		return Errorf(CodeInvalidArgument, "address %q is not a valid CIDR (want e.g. 192.168.66.1/24)", cidr)
	}
	if !pfx.Addr().Is4() {
		return Errorf(CodeInvalidArgument, "AP address %q must be IPv4", cidr)
	}
	if pfx.Bits() > 30 {
		return Errorf(CodeInvalidArgument, "prefix /%d leaves no room for clients", pfx.Bits())
	}
	if pfx.Addr() == pfx.Masked().Addr() {
		return Errorf(CodeInvalidArgument, "%s is the network address, not a host address", pfx.Addr())
	}
	if pfx.Addr() == broadcastOf(pfx) {
		return Errorf(CodeInvalidArgument, "%s is the broadcast address, not a host address", pfx.Addr())
	}
	return nil
}

// broadcastOf returns the all-ones address of an IPv4 prefix.
func broadcastOf(pfx netip.Prefix) netip.Addr {
	b := pfx.Masked().Addr().As4()
	host := uint32(0xffffffff) >> uint(pfx.Bits())
	v := binary.BigEndian.Uint32(b[:]) | host
	var out [4]byte
	binary.BigEndian.PutUint32(out[:], v)
	return netip.AddrFrom4(out)
}

func isAllDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// isControl reports whether r is a C0/C1 control character or DEL.
func isControl(r rune) bool {
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f)
}
