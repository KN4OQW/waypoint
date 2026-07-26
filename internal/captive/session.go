package captive

import (
	"bufio"
	"net"
	"os"
	"strings"
	"sync"
	"time"
)

// DefaultIdleTimeout releases the setup session after this long with no request
// from the holding device. Ten minutes is long enough to read the page, find the
// SSH key on a laptop, and type a passphrase, and short enough that an operator
// whose phone died is not locked out for the rest of the AP's life.
const DefaultIdleTimeout = 10 * time.Minute

// Holder is the device that owns the setup session.
type Holder struct {
	IP        string
	MAC       string
	StartedAt time.Time
	LastSeen  time.Time
}

// Lock admits one device to the wizard and turns every other one away.
//
// The setup AP is open by default, so anyone in radio range can associate and
// reach this server. The lock is what stops the second person from walking
// through a half-finished setup that someone else started — a race that would
// otherwise be won by whoever refreshed last, on a node neither of them could
// then trust.
//
// It is not authentication and does not pretend to be: an attacker who
// associates first holds the session. It bounds the damage to "first device
// wins", which is the same property a physical setup button has, and it is the
// strongest thing available before any credential exists.
type Lock struct {
	// Idle releases the session after this long without a request. Zero means
	// DefaultIdleTimeout.
	Idle time.Duration
	// Now is injectable for tests.
	Now func() time.Time
	// LookupMAC resolves a client IP to a hardware address. Nil uses the kernel's
	// ARP table.
	LookupMAC func(ip string) string
	// Logf receives one line per admitted device and per rejection.
	Logf func(format string, args ...any)

	mu     sync.Mutex
	holder *Holder
	// done marks setup complete: the lock stops admitting anyone, because there is
	// no longer a wizard to hold.
	done bool
}

func (l *Lock) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

func (l *Lock) idle() time.Duration {
	if l.Idle > 0 {
		return l.Idle
	}
	return DefaultIdleTimeout
}

func (l *Lock) logf(format string, args ...any) {
	if l.Logf != nil {
		l.Logf(format, args...)
	}
}

func (l *Lock) lookupMAC(ip string) string {
	if l.LookupMAC != nil {
		return l.LookupMAC(ip)
	}
	return LookupMACFromARP(ARPPath, ip)
}

// Admit reports whether the client at addr may use the wizard, and who holds the
// session otherwise.
//
// A device is admitted when it is the holder, or when there is no live holder. It
// is matched on IP *or* MAC: a phone's DHCP lease can be renewed into a different
// address mid-setup, and an operator who has to start over because their lease
// moved would rightly call that a bug. The MAC is the stabler identity of the two
// and is checked first.
func (l *Lock) Admit(addr string) (bool, Holder) {
	ip := hostOf(addr)
	if ip == "" {
		return false, Holder{}
	}
	mac := l.lookupMAC(ip)
	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.done {
		return false, Holder{}
	}
	if l.holder != nil && now.Sub(l.holder.LastSeen) >= l.idle() {
		l.logf("captive: setup session from %s expired after %s idle", l.holder.IP, l.idle())
		l.holder = nil
	}
	if l.holder == nil {
		l.holder = &Holder{IP: ip, MAC: mac, StartedAt: now, LastSeen: now}
		l.logf("captive: setup session claimed by %s (%s)", ip, macOrUnknown(mac))
		return true, *l.holder
	}
	// MAC first: it survives a lease renewal, which the address does not.
	if (mac != "" && mac == l.holder.MAC) || ip == l.holder.IP {
		if ip != l.holder.IP {
			l.logf("captive: setup session moved from %s to %s (same device)", l.holder.IP, ip)
			l.holder.IP = ip
		}
		if l.holder.MAC == "" && mac != "" {
			l.holder.MAC = mac
		}
		l.holder.LastSeen = now
		return true, *l.holder
	}
	return false, *l.holder
}

// Holder returns the current session holder, if there is one.
func (l *Lock) Holder() (Holder, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.holder == nil {
		return Holder{}, false
	}
	if l.now().Sub(l.holder.LastSeen) >= l.idle() {
		return Holder{}, false
	}
	return *l.holder, true
}

// Release drops the session, letting another device claim it. It is what an
// abandoned setup needs when the operator switches from a phone to a laptop
// rather than waiting out the idle timer.
func (l *Lock) Release() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.holder != nil {
		l.logf("captive: setup session from %s released", l.holder.IP)
	}
	l.holder = nil
}

// Complete marks setup finished. The lock admits nobody afterwards: there is no
// wizard left to hold, and the node has moved on to the claim flow, which has
// real authentication of its own.
func (l *Lock) Complete() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.done = true
	l.holder = nil
}

// ARPPath is the kernel's ARP table.
const ARPPath = "/proc/net/arp"

// LookupMACFromARP resolves an IP to a hardware address from the kernel's ARP
// table.
//
// The ARP table rather than a DHCP lease file because it is the kernel's own
// answer and needs no cooperation from whichever DHCP server NetworkManager
// happened to start. An empty result is normal — a client that has only just
// associated may not be in the table yet — and the lock treats the address as the
// identity in that case.
func LookupMACFromARP(path, ip string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close() //nolint:errcheck // read-only

	sc := bufio.NewScanner(f)
	if sc.Scan() { // header row
		_ = sc.Text()
	}
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 4 || fields[0] != ip {
			continue
		}
		mac := strings.ToLower(fields[3])
		// 00:00:00:00:00:00 is an incomplete entry: the kernel has an address it
		// has not resolved yet, which is not an identity.
		if mac == "00:00:00:00:00:00" {
			return ""
		}
		if _, err := net.ParseMAC(mac); err != nil {
			return ""
		}
		return mac
	}
	return ""
}

// hostOf strips the port from a RemoteAddr, tolerating an address that has none.
func hostOf(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return strings.TrimSpace(addr)
}

func macOrUnknown(mac string) string {
	if mac == "" {
		return "MAC not yet known"
	}
	return mac
}
