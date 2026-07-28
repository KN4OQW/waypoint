package modem

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// Detection has to be arbitrated, and that is the hard part of it.
//
// MMDVM-Host holds the UART open exclusively for as long as it runs, so a probe
// that fires while the stack is up either finds nothing or takes the port away
// from a node that is on the air. Worse, internal/stackupdate gates an update's
// health on "MMDVM-Host stayed running", because the host exits non-zero when
// its modem will not open — so a probe that stole the port during an update
// window would look exactly like a bad update and trigger a rollback.
//
// The rule, enforced here rather than left to callers to remember:
//
//   - The host is not running: probe freely. This is first boot, post-flash,
//     and any node whose modem has never been configured — the moments
//     detection is worth most.
//   - The host is running: refuse, unless the caller explicitly asked to stop
//     it. Detection is never worth silently keying a node off the air.
//   - Having stopped it, restart it no matter what happens next — probe
//     failure, panic, cancelled request. A node that ends a detection off the
//     air because someone closed a browser tab is a worse bug than no
//     detection at all.
//
// There is a tempting alternative: MMDVM-Host publishes its startup log to
// MQTT, version line and all, so this could be read passively with no
// arbitration whatsoever. It is not done, because RFC-0008's no-log-scraping
// property is structurally guarded (internal/status/noscrape_test.go) and
// hardware identity is not the place to put the first crack in it. Waypoint
// asks the modem itself.

var (
	// ErrNoModem means every candidate port was tried and none answered.
	ErrNoModem = errors.New("modem: no MMDVM modem answered on any port")
	// ErrHostRunning means MMDVM-Host holds the port and the caller did not
	// authorise stopping it.
	ErrHostRunning = errors.New("modem: MMDVM-Host is running and holds the modem port")
)

// Holder is the service that owns the modem port in normal operation —
// MMDVM-Host, under systemd. It is an interface so this package never imports
// systemd, and so the arbitration rule can be tested without one.
type Holder interface {
	Active(ctx context.Context) (bool, error)
	Stop(ctx context.Context) error
	Start(ctx context.Context) error
}

// Outcome is what happened on one port.
type Outcome string

const (
	OutcomeModem      Outcome = "modem"      // it answered; this is the modem
	OutcomeSilent     Outcome = "silent"     // opened fine, said nothing
	OutcomeBusy       Outcome = "busy"       // something else holds it
	OutcomeBootloader Outcome = "bootloader" // a board sitting in DFU, unable to answer
	OutcomeError      Outcome = "error"      // could not be opened or configured
)

// Scanned records one port the sweep looked at. The whole list is reported, not
// just the winner: "nothing found" is the outcome an operator most needs
// explained, and "I tried these four and here is what each did" is the
// difference between a useful Detect button and a shrug.
type Scanned struct {
	Port      string  `json:"port"`
	Transport string  `json:"transport"`
	USBID     string  `json:"usb_id,omitempty"`
	Known     string  `json:"known,omitempty"`
	Outcome   Outcome `json:"outcome"`
	Detail    string  `json:"detail,omitempty"`
}

// Identity is a modem Waypoint found and recognised. It is the record stored in
// the config store's hardware section and carried in a profile's fingerprint.
type Identity struct {
	Port      string `json:"port"`
	Transport string `json:"transport"`
	Baud      int    `json:"baud"`
	USBID     string `json:"usb_id,omitempty"`

	Protocol    uint8  `json:"protocol"`
	Description string `json:"description"`
	HWType      string `json:"hw_type"`
	Firmware    string `json:"firmware,omitempty"`
	Built       string `json:"built,omitempty"`
	Author      string `json:"author,omitempty"`
	GitID       string `json:"git_id,omitempty"`
	UDID        string `json:"udid,omitempty"`
	MCU         string `json:"mcu,omitempty"`

	TCXOHz      int      `json:"tcxo_hz,omitempty"`
	TCXOAssumed bool     `json:"tcxo_assumed,omitempty"`
	Duplex      bool     `json:"duplex"`
	BoardID     string   `json:"board_id,omitempty"`
	Candidates  []string `json:"candidates,omitempty"`

	Modes      Modes  `json:"modes"`
	DetectedAt string `json:"detected_at,omitempty"`
}

// Result is one detection run.
type Result struct {
	Identity   *Identity `json:"identity"` // nil when nothing answered
	Scanned    []Scanned `json:"scanned"`
	Bootloader string    `json:"bootloader,omitempty"` // a port holding a board in DFU
}

// Detector probes for a modem. The zero value is usable; every field is a
// default a caller may override, and the ones that touch the OS are injectable
// so the whole flow is testable without a hat on the bench.
type Detector struct {
	Scanner     Scanner
	Holder      Holder        // nil means "nothing holds the port" (tests, first boot)
	Bauds       []int         // default Bauds[:2]
	ReadTimeout time.Duration // per-read silence window; default 400ms
	Attempts    int           // GET_VERSION tries per port per baud; default 3
	Settle      time.Duration // post-open wait; 0 means the per-transport default
	Now         func() time.Time

	// open is the serial layer, injectable for tests.
	open func(path string, baud int, readTimeout time.Duration) (io.ReadWriteCloser, error)
}

func (d *Detector) opener() func(string, int, time.Duration) (io.ReadWriteCloser, error) {
	if d.open != nil {
		return d.open
	}
	return func(path string, baud int, to time.Duration) (io.ReadWriteCloser, error) {
		return openSerial(path, baud, to)
	}
}

func (d *Detector) bauds() []int {
	if len(d.Bauds) > 0 {
		return d.Bauds
	}
	// Two speeds, not three. 115200 covers the entire launch tier; the second
	// is for reflashed boards. Each extra speed is another second of spinner on
	// every dead port in the sweep.
	return Bauds[:2]
}

func (d *Detector) readTimeout() time.Duration {
	if d.ReadTimeout > 0 {
		return d.ReadTimeout
	}
	return 400 * time.Millisecond
}

func (d *Detector) attempts() int {
	if d.Attempts > 0 {
		return d.Attempts
	}
	return 3
}

func (d *Detector) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// settle is how long to wait after opening before asking. Opening a USB CDC
// port asserts DTR, which resets an STM32 board into a fresh firmware boot;
// asking it who it is before it has finished starting gets silence. A hat on
// the GPIO UART has no such line and needs only enough time for the driver to
// come up.
func (d *Detector) settle(t Transport) time.Duration {
	if d.Settle > 0 {
		return d.Settle
	}
	if t == TransportUSB {
		return 1500 * time.Millisecond
	}
	return 250 * time.Millisecond
}

// Detect sweeps for a modem, honouring the arbitration rule. stopHost
// authorises taking the port away from a running MMDVM-Host; without it, a
// running host is a refusal, not a silent failure.
func (d *Detector) Detect(ctx context.Context, stopHost bool) (Result, error) {
	restore, err := d.arbitrate(ctx, stopHost)
	if err != nil {
		return Result{}, err
	}
	defer restore()

	ports, err := d.Scanner.Scan()
	if err != nil {
		return Result{}, fmt.Errorf("modem: enumerate ports: %w", err)
	}

	var res Result
	for _, p := range ports {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		rec := Scanned{Port: p.Path, Transport: p.Transport.String(), USBID: p.ID(), Known: p.Known}
		if p.Bootloader() {
			// It cannot answer — but "your board is in its bootloader" is a far
			// more useful thing to tell an operator than "no modem found", and
			// it is one firmware flash from a working node.
			rec.Outcome, rec.Detail = OutcomeBootloader, p.Known
			res.Bootloader = p.Path
			res.Scanned = append(res.Scanned, rec)
			continue
		}

		id, err := d.probePort(ctx, p)
		switch {
		case err == nil:
			rec.Outcome = OutcomeModem
			res.Identity = &id
			res.Scanned = append(res.Scanned, rec)
			return res, nil
		case errors.Is(err, ErrNoModem):
			rec.Outcome = OutcomeSilent
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return res, err
		default:
			rec.Outcome, rec.Detail = OutcomeError, err.Error()
			if isBusy(err) {
				rec.Outcome = OutcomeBusy
			}
		}
		res.Scanned = append(res.Scanned, rec)
	}
	return res, ErrNoModem
}

// arbitrate applies the port-ownership rule and returns the restore step. The
// restore always runs, and it deliberately does not inherit the caller's
// context: the request that started a detection may well be gone by the time it
// finishes, and the node must come back on the air regardless.
func (d *Detector) arbitrate(ctx context.Context, stopHost bool) (func(), error) {
	noop := func() {}
	if d.Holder == nil {
		return noop, nil
	}
	active, err := d.Holder.Active(ctx)
	if err != nil {
		return nil, fmt.Errorf("modem: check MMDVM-Host: %w", err)
	}
	if !active {
		return noop, nil
	}
	if !stopHost {
		return nil, ErrHostRunning
	}
	if err := d.Holder.Stop(ctx); err != nil {
		return nil, fmt.Errorf("modem: stop MMDVM-Host: %w", err)
	}
	return func() {
		restart, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_ = d.Holder.Start(restart)
	}, nil
}

// probePort tries each line speed on one port. An open failure is fatal for the
// port — it will fail identically at every speed — while silence is not, because
// silence is exactly what the wrong speed sounds like.
func (d *Detector) probePort(ctx context.Context, p Port) (Identity, error) {
	for _, baud := range d.bauds() {
		id, err := d.probeAt(ctx, p, baud)
		switch {
		case err == nil:
			return id, nil
		case errors.Is(err, ErrNoModem):
			continue
		default:
			return Identity{}, err
		}
	}
	return Identity{}, ErrNoModem
}

func (d *Detector) probeAt(ctx context.Context, p Port, baud int) (Identity, error) {
	port, err := d.opener()(p.Path, baud, d.readTimeout())
	if err != nil {
		return Identity{}, err
	}
	defer port.Close()

	if err := sleepCtx(ctx, d.settle(p.Transport)); err != nil {
		return Identity{}, err
	}
	for attempt := 0; attempt < d.attempts(); attempt++ {
		if err := ctx.Err(); err != nil {
			return Identity{}, err
		}
		if _, err := port.Write(VersionRequest()); err != nil {
			return Identity{}, fmt.Errorf("modem: write to %s: %w", p.Path, err)
		}
		// Read a few frames, not one: a port only just released by MMDVM-Host
		// can still have its traffic in flight, and the answer we want may be
		// behind it.
		for frames := 0; frames < 4; frames++ {
			f, err := ReadFrame(port)
			if err != nil {
				break // silence or noise: send the question again
			}
			v, err := ParseVersion(f)
			if errors.Is(err, ErrNotVersion) {
				continue // someone else's frame; keep reading
			}
			if err != nil {
				break
			}
			return d.identify(p, baud, v), nil
		}
	}
	return Identity{}, ErrNoModem
}

// identify folds everything known about a port and its answer into the record
// that gets stored.
func (d *Detector) identify(p Port, baud int, v Version) Identity {
	desc := ParseDescription(v.Description)
	r := Resolve(desc, p.Transport)
	return Identity{
		Port:      p.Path,
		Transport: p.Transport.String(),
		Baud:      baud,
		USBID:     p.ID(),

		Protocol:    v.Protocol,
		Description: v.Description,
		HWType:      desc.HWType,
		Firmware:    desc.Firmware,
		Built:       desc.Built,
		Author:      desc.Author,
		GitID:       desc.GitID,
		UDID:        v.UDID,
		MCU:         desc.MCU,

		TCXOHz:      r.TCXOHz,
		TCXOAssumed: r.TCXOAssumed,
		Duplex:      r.Dual,
		BoardID:     r.BoardID,
		Candidates:  r.Candidates,

		Modes:      v.Modes(),
		DetectedAt: d.now().UTC().Format(time.RFC3339),
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// isBusy separates "something else already has this port" from a real fault, so
// the report can say which. Both are common and they mean different things: busy
// is usually another daemon, error is usually permissions or a dead device.
func isBusy(err error) bool {
	if errors.Is(err, unix.EBUSY) || errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EACCES) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, s := range []string{"device or resource busy", "resource temporarily unavailable", "permission denied"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}
