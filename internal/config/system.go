package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/KN4OQW/waypoint/internal/store"
)

// The System tab's two store sections: the MQTT data plane every daemon shares,
// and the per-daemon log levels. Both were command-line flags and render-time
// constants before this file existed, which is why they were the last two
// domains on the configuration page's checklist (#29) that an operator could not
// edit without a text editor and a systemd drop-in.
//
// Key names here are transcribed from the pinned upstream sources (pins.env), not
// from a sample INI — the MQTT-era trees drifted from the published MMDVM.ini and
// from each other:
//
//	MMDVM-Host   fd4a6a4  Conf.cpp:600-619  [Log] MQTTLevel DisplayLevel
//	                                        [MQTT] Host Port Keepalive Name Auth Username Password
//	DMRGateway   79edbc4  Conf.cpp:199-203,402-415   same key set as MMDVM-Host
//	YSFClients   2b480aa  YSFGateway/Conf.cpp:220-247, DGIdGateway/Conf.cpp:218-237   same
//	P25Clients   9751c6e  P25Gateway/Conf.cpp:171-189                                 same
//	NXDNClients  18b4e9a  NXDNGateway/Conf.cpp:221-248                                same
//	DStarGateway 612f388  DStarGatewayConfig.cpp:161-179  Log DisplayLevel/MQTTLevel (0..6)
//	                                        MQTT *Address* Port Keepalive *Authenticate* Username Password Name
//	M17Gateway   c72b989  Conf.cpp:190-197  [Log] DisplayLevel FileLevel — PRE-MQTT, no [MQTT] at all
//
// D-Star is the outlier twice over (Address not Host, Authenticate not Auth) and
// M17Gateway has no broker keys to set. Both facts are encoded in the renderers,
// not papered over here.

// Default MQTT data-plane settings. These are exactly the compiled flag defaults
// waypointd shipped before the section existed, so a node with no mqtt row
// renders byte-identically to one running the stock flags.
const (
	DefaultMQTTHost         = "127.0.0.1"
	DefaultMQTTPort         = "1883"
	DefaultMQTTName         = "mmdvm"        // MMDVM-Host [MQTT] Name — the status topic root
	DefaultStatusPrefix     = "waypoint/status"
	DefaultMQTTKeepaliveSec = "60"
	// DefaultBusTopicPrefix lives in peering_render.go beside the BusMQTT type it
	// renders into; it is the third prefix this section owns.
)

// MaxLogLevel is the highest level any pinned daemon accepts. MMDVM-Host's Log.h
// defines the ladder — 1 DEBUG, 2 MESSAGE, 3 INFO, 4 WARNING, 5 ERROR, 6 FATAL —
// and 0 disables that sink entirely (Log.cpp: `level >= m_displayLevel &&
// m_displayLevel != 0U`). DStarGateway clamps to the same 0..6 explicitly
// (DStarGatewayConfig.cpp:161). Nothing upstream reads a 7.
const MaxLogLevel = 6

// MQTT is the data plane every daemon on the node shares: the broker they connect
// to, the credentials they use, and the three topic roots Waypoint publishes and
// consumes under. It is one section rather than one per daemon because there is
// one broker — mosquitto on localhost — and a node whose gateways disagreed about
// where it lives would simply be broken.
//
// Password is a secret with the write-only rule the DMR network passwords and the
// ircDDB password already use: the API View never serializes it, so a blank
// incoming value means "keep the stored one" (SetMQTT).
//
// Name is MMDVM-Host's [MQTT] Name, which is also the root of every topic the
// host publishes (<Name>/status, <Name>/log …) and therefore what waypointd's
// consumer subscribes to. Renaming it moves the whole modem status tree, so the
// consumer takes it from here rather than from a flag nobody remembers to match.
// The GATEWAY names (ysf-gateway, dmr-gateway, …) are deliberately NOT modeled:
// they are per-daemon identities the status pipeline matches on, not operator
// preferences, and an operator who renamed one would only make their own gateway
// vanish from the dashboard.
//
// Empty string means "use the compiled default" throughout, the same `def()`
// idiom the renderers use everywhere else — so a partially-populated row from an
// older schema still renders a working file.
type MQTT struct {
	Host     string `json:"host"`     // broker host ([MQTT] Host / Address)
	Port     string `json:"port"`     // broker port ([MQTT] Port)
	Auth     bool   `json:"auth"`     // authenticate to the broker ([MQTT] Auth / Authenticate)
	Username string `json:"username"` // [MQTT] Username
	Password string `json:"password"` // [MQTT] Password (secret; blank on write = keep stored)
	Name     string `json:"name"`     // MMDVM-Host [MQTT] Name — the modem status topic root

	// StatusPrefix and BusPrefix are Waypoint's own topic roots, not daemon INI
	// keys: waypointd republishes the normalized status under StatusPrefix
	// (RFC-0008) and the bus daemons publish their events under BusPrefix
	// (RFC-0003 D4, rendered into each bus config). They live here because they
	// are the same operator decision as Name — where this node's data plane sits.
	StatusPrefix string `json:"status_prefix"`
	BusPrefix    string `json:"bus_prefix"`
}

// LogLevels is one MQTT-era daemon's pair of sinks: Display goes to stdout (and
// so to the systemd journal, since every unit runs in the foreground) and MQTT
// publishes to <name>/log for the dashboard. Both are 0..6, 0 meaning off.
type LogLevels struct {
	Display string `json:"display"`
	MQTT    string `json:"mqtt"`
}

// FileLogLevels is the PRE-MQTT shape: a daemon that has no broker connection and
// writes a log file instead. Only M17Gateway is in this state (pins.env: the
// pinned c72b989 has no libmosquitto), and giving it an `mqtt` field the daemon
// cannot read would be a control that silently does nothing — the same objection
// the renderer already records against emitting dead keys.
type FileLogLevels struct {
	Display string `json:"display"`
	File    string `json:"file"`
}

// Logging is the per-daemon log-level policy. One field per render target, named
// with the same daemon keys the override layer uses (RFC-0005 daemonMMDVM,
// daemonDMRGateway, …) so an operator reading overrides.d/ and the System tab
// sees one vocabulary, not two.
//
// The defaults reproduce what the renderers hardcoded before this section
// existed: MQTT 1 / Display 0 for the MQTT-era daemons (everything on the data
// plane, nothing duplicated into the journal), and Display 1 / File 0 for
// pre-MQTT M17Gateway (journal only, no log file to rotate on an SD card).
type Logging struct {
	MMDVM         LogLevels     `json:"mmdvm"`
	DMRGateway    LogLevels     `json:"dmrgateway"`
	YSFGateway    LogLevels     `json:"ysfgateway"`
	DGIdGateway   LogLevels     `json:"dgidgateway"`
	P25Gateway    LogLevels     `json:"p25gateway"`
	NXDNGateway   LogLevels     `json:"nxdngateway"`
	DStarGateway  LogLevels     `json:"dstargateway"`
	DAPNETGateway LogLevels     `json:"dapnetgateway"`
	M17Gateway    FileLogLevels `json:"m17gateway"`
}

// --- effective values (empty ⇒ compiled default) --------------------------

func (q MQTT) host() string { return def(strings.TrimSpace(q.Host), DefaultMQTTHost) }
func (q MQTT) port() string { return def(strings.TrimSpace(q.Port), DefaultMQTTPort) }
func (q MQTT) name() string { return def(strings.TrimSpace(q.Name), DefaultMQTTName) }

// Broker is the host:port every daemon and waypointd's own consumer dials. It is
// exported because the daemon side (cmd/waypointd) resolves it against the flags
// before handing it to the MQTT bridge.
func (q MQTT) Broker() string { return q.host() + ":" + q.port() }

// StatusTopicPrefix and BusTopicPrefix are the effective topic roots, defaulted.
func (q MQTT) StatusTopicPrefix() string {
	return def(strings.TrimSpace(q.StatusPrefix), DefaultStatusPrefix)
}
func (q MQTT) BusTopicPrefix() string {
	return def(strings.TrimSpace(q.BusPrefix), DefaultBusTopicPrefix)
}

// HostName is MMDVM-Host's [MQTT] Name — the topic root the modem publishes under
// and waypointd's consumer subscribes to.
func (q MQTT) HostName() string { return q.name() }

// authKV renders the 0/1 an INI Auth/Authenticate key takes.
func (q MQTT) authKV() string {
	if q.Auth {
		return "1"
	}
	return "0"
}

// level resolves one configured level against its compiled default: blank keeps
// the default, so a section written by an older schema (or a UI that only sent
// the field it manages) never renders an empty key the daemon would atoi() to 0.
func level(v, fallback string) string { return def(strings.TrimSpace(v), fallback) }

func (l LogLevels) display(fallback string) string { return level(l.Display, fallback) }
func (l LogLevels) mqtt(fallback string) string    { return level(l.MQTT, fallback) }

func (l FileLogLevels) display(fallback string) string { return level(l.Display, fallback) }
func (l FileLogLevels) file(fallback string) string    { return level(l.File, fallback) }

// --- section renderers ----------------------------------------------------

// mqttSection renders one daemon's [MQTT] block from the store. The two key names
// that DRIFT between the pinned trees are parameters, not assumptions:
//
//	hostKey  "Host"    on MMDVM-Host / DMRGateway / YSF / DGId / P25 / NXDN
//	         "Address" on DStarGateway (DStarGatewayConfig.cpp:171)
//	authKey  "Auth"         on everything except
//	         "Authenticate" on DStarGateway (DStarGatewayConfig.cpp:175)
//
// This is the same schema-drift class as the [DMR Network] Address→GatewayAddress
// slip that motivated #29, so it is encoded once here rather than retyped into
// seven renderers.
//
// name is the daemon's own identity on the data plane (mmdvm, ysf-gateway, …) —
// a per-daemon constant the status pipeline matches on, not an operator setting.
//
// Username/Password are emitted ONLY when authentication is on. With Auth=0 the
// daemons never send them, so writing the broker password into seven rendered
// files would be exposure that buys nothing.
func (m *Model) mqttSection(hostKey, authKey, name string) []string {
	lines := []string{
		kv(hostKey, m.MQTT.host()),
		kv("Port", m.MQTT.port()),
		kv("Keepalive", DefaultMQTTKeepaliveSec),
		kv(authKey, m.MQTT.authKV()),
		kv("Name", name),
	}
	if m.MQTT.Auth {
		lines = append(lines, kv("Username", m.MQTT.Username), kv("Password", m.MQTT.Password))
	}
	return lines
}

// logSectionMQTT renders an MQTT-era daemon's [Log] block: the MQTT sink (the
// dashboard's data plane) and the display sink (stdout → the systemd journal).
// Defaults are the values the renderers hardcoded before the section existed.
func (m *Model) logSectionMQTT(l LogLevels) []string {
	return []string{
		kv("MQTTLevel", l.mqtt("1")),
		kv("DisplayLevel", l.display("0")),
	}
}

// --- validation -----------------------------------------------------------

// ValidateMQTT rejects a data-plane setting that would produce an unusable node:
// a port outside 1..65535, a blank-but-present host, or a topic prefix with the
// wildcard/level characters MQTT reserves. It deliberately does NOT require
// authentication credentials when Auth is on — mosquitto's own
// allow_anonymous/ACL config is the authority on whether the broker will take
// them, and refusing the save would strand an operator mid-migration.
func ValidateMQTT(q MQTT) error {
	if p := strings.TrimSpace(q.Port); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("mqtt: port %q is not a TCP port (1..65535)", q.Port)
		}
	}
	if strings.ContainsAny(q.Host, " \t") {
		return fmt.Errorf("mqtt: host %q contains whitespace", q.Host)
	}
	for _, p := range []struct{ key, val string }{
		{"name", q.Name}, {"status_prefix", q.StatusPrefix}, {"bus_prefix", q.BusPrefix},
	} {
		if err := validTopicPrefix(p.key, p.val); err != nil {
			return err
		}
	}
	return nil
}

// validTopicPrefix rejects a topic root MQTT itself cannot carry. '+' and '#' are
// subscription wildcards (a prefix containing one would make every publish land
// on a topic no subscriber can address unambiguously), NUL is illegal in a topic
// name, and a leading or trailing '/' produces the empty topic level that trips
// half the broker tooling. Blank is always fine — it means "use the default".
func validTopicPrefix(key, v string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	if strings.ContainsAny(v, "+#\x00") {
		return fmt.Errorf("mqtt: %s %q contains an MQTT wildcard (+ or #)", key, v)
	}
	if strings.ContainsAny(v, " \t") {
		return fmt.Errorf("mqtt: %s %q contains whitespace", key, v)
	}
	if strings.HasPrefix(v, "/") || strings.HasSuffix(v, "/") {
		return fmt.Errorf("mqtt: %s %q has a leading or trailing '/' (empty topic level)", key, v)
	}
	return nil
}

// ValidateLogging rejects any level outside the 0..6 ladder the pinned daemons
// accept. Blank is allowed and means "compiled default"; anything else must be a
// plain decimal in range, so a typo is refused at save rather than silently
// atoi()'d to 0 (which would turn a sink OFF — the failure mode where an operator
// asks for more logging and gets none).
func ValidateLogging(l Logging) error {
	pairs := []struct {
		daemon string
		levels [][2]string // {key, value}
	}{
		{daemonMMDVM, [][2]string{{"display", l.MMDVM.Display}, {"mqtt", l.MMDVM.MQTT}}},
		{daemonDMRGateway, [][2]string{{"display", l.DMRGateway.Display}, {"mqtt", l.DMRGateway.MQTT}}},
		{daemonYSFGateway, [][2]string{{"display", l.YSFGateway.Display}, {"mqtt", l.YSFGateway.MQTT}}},
		{daemonDGIdGateway, [][2]string{{"display", l.DGIdGateway.Display}, {"mqtt", l.DGIdGateway.MQTT}}},
		{daemonP25Gateway, [][2]string{{"display", l.P25Gateway.Display}, {"mqtt", l.P25Gateway.MQTT}}},
		{daemonNXDNGateway, [][2]string{{"display", l.NXDNGateway.Display}, {"mqtt", l.NXDNGateway.MQTT}}},
		{daemonDStarGateway, [][2]string{{"display", l.DStarGateway.Display}, {"mqtt", l.DStarGateway.MQTT}}},
		{daemonDAPNETGateway, [][2]string{{"display", l.DAPNETGateway.Display}, {"mqtt", l.DAPNETGateway.MQTT}}},
		{daemonM17Gateway, [][2]string{{"display", l.M17Gateway.Display}, {"file", l.M17Gateway.File}}},
	}
	for _, p := range pairs {
		for _, kv := range p.levels {
			if err := validLevel(p.daemon, kv[0], kv[1]); err != nil {
				return err
			}
		}
	}
	return nil
}

func validLevel(daemon, key, v string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 || n > MaxLogLevel {
		return fmt.Errorf("logging: %s %s level %q is not 0..%d", daemon, key, v, MaxLogLevel)
	}
	return nil
}

// --- store writers --------------------------------------------------------

// SetMQTT writes the mqtt section with the write-only-secret rule the DMR
// networks, the ircDDB password and the DAPNET AuthKey already use: the API View
// never exposes the broker password, so a blank incoming Password means "keep the
// stored one" and a non-blank one replaces it. Like SetSection this is a merge —
// the body is decoded over the stored section, so a UI sending only the fields it
// manages never drops the rest — and unknown fields are rejected, so schema drift
// is a caller error rather than a silently ignored key.
func SetMQTT(s *store.Store, raw []byte, by string) error {
	var existing MQTT
	if _, err := s.GetInto("mqtt", &existing); err != nil {
		return err
	}
	incoming := existing
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&incoming); err != nil {
		return err
	}
	if incoming.Password == "" {
		incoming.Password = existing.Password
	}
	if err := ValidateMQTT(incoming); err != nil {
		return err
	}
	return s.Set("mqtt", &incoming, by)
}

// SetLogging writes the logging section, rejecting unknown fields and any level
// outside 0..6 (ValidateLogging) before it reaches the store. No secret.
func SetLogging(s *store.Store, raw []byte, by string) error {
	var existing Logging
	if _, err := s.GetInto("logging", &existing); err != nil {
		return err
	}
	incoming := existing
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&incoming); err != nil {
		return err
	}
	if err := ValidateLogging(incoming); err != nil {
		return err
	}
	return s.Set("logging", &incoming, by)
}
