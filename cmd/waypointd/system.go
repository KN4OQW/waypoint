package main

import (
	"flag"
	"log"
	"sort"
	"strings"

	"github.com/KN4OQW/waypoint/internal/config"
)

// The MQTT data plane is store-owned (#29 System tab), but it was command-line
// flags first and every dev invocation, packaging unit and test harness in the
// tree still passes them. RFC-0005's override semantics settle the conflict the
// same way the INI override layer does — the human's explicit instruction wins
// over the generated value, and the shadowing is SURFACED rather than silent:
//
//	explicitly-set flag  >  store section  >  compiled default
//
// "Explicitly set" is flag.Visit, not "differs from the default". A flag left
// alone must take the store value even when the store happens to hold the flag's
// own default, or the store could never move a value off its default — and
// `-mqtt-broker 127.0.0.1:1883` typed by hand must still shadow, so comparing
// against the default would be wrong in both directions.
//
// The practical effect: a fresh dev run and `-demo` keep working with no mqtt row
// at all, an operator who edits the System tab sees it take effect, and an
// operator who ALSO passes the flag is told, by name, which store key their flag
// is overriding rather than filing a bug about a setting that "does not save".

// mqttFlagNames maps each command-line flag to the store key it shadows. The map
// is the single place the two vocabularies meet; the warning text is generated
// from it, so a flag added without a store key (or vice versa) is visible here
// rather than discovered by an operator.
var mqttFlagNames = map[string]string{
	"mqtt-broker":         "mqtt.host + mqtt.port",
	"mqtt-name":           "mqtt.name",
	"mqtt-user":           "mqtt.username",
	"mqtt-pass":           "mqtt.password",
	"status-topic-prefix": "mqtt.status_prefix",
	"bus-topic-prefix":    "mqtt.bus_prefix",
}

// mqttFlags is the raw command-line MQTT surface plus which of those flags the
// operator actually typed. It is captured once, after flag.Parse, and never
// changes for the life of the process — flags are static, the store is not.
type mqttFlags struct {
	broker       string
	name         string
	user         string
	pass         string
	statusPrefix string
	busPrefix    string
	set          map[string]bool // flag names seen by flag.Visit
}

// newMQTTFlags snapshots the parsed flag values and records which were set
// explicitly. Call it after flag.Parse.
func newMQTTFlags(broker, name, user, pass, statusPrefix, busPrefix string) mqttFlags {
	set := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { set[f.Name] = true })
	return mqttFlags{
		broker: broker, name: name, user: user, pass: pass,
		statusPrefix: statusPrefix, busPrefix: busPrefix, set: set,
	}
}

// resolve applies D1 precedence to a loaded model: every explicitly-set flag is
// written OVER the model's mqtt section, so everything downstream — the INI
// renderers, the bus configs, the status republisher, the HA discovery prefix —
// reads one already-reconciled value and no component re-implements the rule.
//
// It mutates the caller's in-memory model and never writes to the store: the
// store keeps what the operator typed in the UI, so removing the flag from the
// unit file restores their setting instead of silently having overwritten it.
//
// warn reports each shadowed key once per call (the caller logs it); a run with
// no MQTT flags set produces none.
func (f mqttFlags) resolve(m *config.Model) (shadowed []string) {
	if f.set["mqtt-broker"] {
		host, port := splitBroker(f.broker)
		m.MQTT.Host, m.MQTT.Port = host, port
		shadowed = append(shadowed, "mqtt-broker")
	}
	if f.set["mqtt-name"] {
		m.MQTT.Name = f.name
		shadowed = append(shadowed, "mqtt-name")
	}
	if f.set["mqtt-user"] {
		m.MQTT.Username = f.user
		shadowed = append(shadowed, "mqtt-user")
	}
	if f.set["mqtt-pass"] {
		m.MQTT.Password = f.pass
		// A password on the command line means the operator intends to
		// authenticate; leaving Auth off would render Username/Password into no
		// file at all and the shadow would do nothing.
		m.MQTT.Auth = true
		shadowed = append(shadowed, "mqtt-pass")
	}
	if f.set["status-topic-prefix"] {
		m.MQTT.StatusPrefix = f.statusPrefix
		shadowed = append(shadowed, "status-topic-prefix")
	}
	if f.set["bus-topic-prefix"] {
		m.MQTT.BusPrefix = f.busPrefix
		shadowed = append(shadowed, "bus-topic-prefix")
	}
	sort.Strings(shadowed)
	return shadowed
}

// logShadowed prints one warning per shadowed flag, naming the store key the
// operator's System-tab edit is losing to. Silence would turn "I changed the
// broker and nothing happened" into an unfalsifiable bug report.
func logShadowed(shadowed []string) {
	for _, name := range shadowed {
		log.Printf("mqtt: -%s was set on the command line and overrides the store key %s "+
			"(System tab); remove the flag from the unit file to let the stored value apply",
			name, mqttFlagNames[name])
	}
}

// splitBroker splits a "host:port" broker flag. A value with no colon is taken as
// a bare host and keeps the stored/compiled port, which is the reading that
// surprises nobody; an empty host or port falls back the same way.
func splitBroker(v string) (host, port string) {
	v = strings.TrimSpace(v)
	i := strings.LastIndex(v, ":")
	if i < 0 {
		return v, ""
	}
	return v[:i], v[i+1:]
}

// viewSources names the deployment-owned locations the settings page displays but
// never edits: the store file and the HTTPS listen address (#29 scope amendment —
// the address is read-only because moving it live can strand the operator).
func (s *server) viewSources() config.Sources {
	return config.Sources{Store: s.storePath, Listen: s.listenAddr}
}

// resolvedModel loads the model and applies the flag overrides (D1), returning the
// model every render and every MQTT consumer should read. The shadow warnings are
// logged once at startup, not on every apply, so a node running with a pinned flag
// does not fill its journal with the same line on each Apply.
func (s *server) resolvedModel() (*config.Model, error) {
	m, err := config.Load(s.store)
	if err != nil {
		return nil, err
	}
	s.mqttFlags.resolve(m)
	return m, nil
}

// effectivePaths returns the deployment Paths with the MQTT broker and bus topic
// prefix refreshed from the RESOLVED model. Paths is built once at startup, but
// the store is live: without this, editing the bus prefix in the System tab would
// re-render MMDVM.ini and the gateway INIs (which read the model directly) while
// leaving the bus configs on the startup value — exactly the half-applied state D5
// forbids. Demo mode keeps its empty broker, so a demo still renders no MQTT block.
func (s *server) effectivePaths(m *config.Model) config.Paths {
	p := s.paths
	if p.MQTTBroker != "" {
		p.MQTTBroker = m.MQTT.Broker()
	}
	p.BusTopicPrefix = m.MQTT.BusTopicPrefix()
	return p
}

// dataPlaneBroker is the broker the daemon actually connected to, for the startup
// banner. It falls back to the flag value in demo mode, where no data plane runs.
func (s *server) dataPlaneBroker(fallback string) string {
	if s.dp == nil {
		return fallback
	}
	broker, _ := s.dp.brokerAndPrefix()
	return broker
}
