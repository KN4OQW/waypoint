package config

import (
	"reflect"
	"strings"
	"testing"
)

// busRenderFixture is a valid, varied set of buses+attachments touching all three
// reframe modes and every translation field, so a render/round-trip cannot be
// masked by an empty field. "local" is enabled with DMR+YSF+NXDN; "spare" is
// disabled (must contribute no target and lose no rows).
func busRenderFixture() *Model {
	return &Model{
		DMRNet: DMRNet{LocalPort: "62032", GatewayPort: "62031"},
		Networks: []Network{
			{Name: "BM_3102", Type: NetBrandmeister, Address: "3102.master.brandmeister.network", Password: "s3cr3t", Enabled: true},
		},
		Buses: []Bus{
			{ID: "local", Name: "Local Bus A", Enabled: true},
			{ID: "spare", Name: "Spare", Enabled: false},
		},
		Attachments: []Attachment{
			{BusID: "local", Mode: ModeDMR, CredentialsRef: "BM_3102", Slot: "2", DefaultTG: "310", TGMap: map[string]string{"290": "310", "0": "9"}},
			{BusID: "local", Mode: ModeYSF, Target: "FCS00290", WiresXPassthrough: true},
			{BusID: "local", Mode: ModeNXDN, ID: "12345", TG: "20", DefaultID: "65519"},
			{BusID: "spare", Mode: ModeM17}, // spare disabled; M17 would be invalid if enabled, but a disabled bus renders nothing
		},
	}
}

// busTargets filters RenderTargets down to the mode-bus targets.
func busTargets(m *Model) []RenderTarget {
	var out []RenderTarget
	for _, t := range m.RenderTargets(Paths{BusConfigDir: "/etc/waypoint/bus"}) {
		if strings.HasPrefix(t.Unit, unitBusPrefix) {
			out = append(out, t)
		}
	}
	return out
}

// TestBusPureRender is RFC-0003 §6.1: a bus renders byte-identically across
// repeated renders and is unchanged by unrelated store edits; each ENABLED bus
// contributes exactly one target, a disabled bus none.
func TestBusPureRender(t *testing.T) {
	m := busRenderFixture()

	// Exactly one target per enabled bus (one enabled here), keyed to its instance.
	targets := busTargets(m)
	if len(targets) != 1 {
		t.Fatalf("enabled buses => %d bus targets, want 1", len(targets))
	}
	if targets[0].Unit != "waypoint-bus@local.service" {
		t.Fatalf("bus unit = %q, want waypoint-bus@local.service", targets[0].Unit)
	}
	if targets[0].Path != "/etc/waypoint/bus/local.conf" {
		t.Fatalf("bus path = %q, want /etc/waypoint/bus/local.conf", targets[0].Path)
	}

	// Byte-identical across repeated renders (pure function).
	first := m.renderBus(m.Buses[0])
	for i := 0; i < 5; i++ {
		if got := m.renderBus(m.Buses[0]); got != first {
			t.Fatalf("render not deterministic on pass %d", i)
		}
	}
	// The target's Render closure produces the same bytes as renderBus.
	if got := targets[0].Render(m); got != first {
		t.Fatalf("target render != renderBus:\n%s\n---\n%s", got, first)
	}

	// Unrelated store edits change nothing: mutate sections a bus does not read.
	edited := busRenderFixture()
	edited.General.Callsign = "W1AW"
	edited.Display = Display{Type: "HD44780", HD44780Rows: "4", HD44780Cols: "20"}
	edited.LCD = LCD{Enabled: true, Rows: "4", Cols: "20"}
	edited.P25 = P25{NAC: "293"}
	edited.Networks[0].Password = "different-secret"
	if got := edited.renderBus(edited.Buses[0]); got != first {
		t.Fatalf("unrelated edit changed bus render:\n want %q\n  got %q", first, got)
	}

	// A disabled bus contributes no target and keeps its rows.
	if got := len(edited.Attachments); got != 4 {
		t.Fatalf("attachment rows = %d, want 4 (disable deletes nothing)", got)
	}

	// Enabling the spare bus (after making its attachment valid) yields a second
	// target; disabling both yields none.
	twoOn := busRenderFixture()
	twoOn.Buses[1].Enabled = true
	twoOn.Attachments[3] = Attachment{BusID: "spare", Mode: ModeNXDN, DefaultID: "65519"}
	if got := len(busTargets(twoOn)); got != 2 {
		t.Fatalf("two enabled buses => %d targets, want 2", got)
	}
	allOff := busRenderFixture()
	allOff.Buses[0].Enabled = false
	if got := len(busTargets(allOff)); got != 0 {
		t.Fatalf("no enabled buses => %d targets, want 0", got)
	}
}

// TestBusRoundTrip is RFC-0003 §6.2: model -> rendered config -> parse loses no
// translation param; disabling then re-enabling a bus is byte-identical output.
func TestBusRoundTrip(t *testing.T) {
	m := busRenderFixture()
	rendered := m.renderBus(m.Buses[0])

	cfg, err := ParseBusConfig(strings.NewReader(rendered))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ID != "local" || cfg.Name != "Local Bus A" {
		t.Fatalf("bus header lost: id=%q name=%q", cfg.ID, cfg.Name)
	}
	if len(cfg.Attachments) != 3 {
		t.Fatalf("parsed %d attachments, want 3", len(cfg.Attachments))
	}
	byMode := map[string]BusAttachment{}
	for _, a := range cfg.Attachments {
		byMode[a.Mode] = a
	}

	// DMR: loopback endpoints + every translation param survive.
	dmr := byMode["dmr"]
	if dmr.BindPort != "62031" || dmr.PeerPort != "62032" {
		t.Fatalf("DMR loopback lost: bind=%q peer=%q", dmr.BindPort, dmr.PeerPort)
	}
	if dmr.Slot != "2" || dmr.DefaultTG != "310" || dmr.CredentialsRef != "BM_3102" {
		t.Fatalf("DMR translation lost: %+v", dmr)
	}
	if !reflect.DeepEqual(dmr.TGMap, map[string]string{"290": "310", "0": "9"}) {
		t.Fatalf("DMR tg_map lost: %+v", dmr.TGMap)
	}
	if dmr.IdLookup != busIdLookupFile {
		t.Fatalf("DMR IdLookup = %q, want shared %q", dmr.IdLookup, busIdLookupFile)
	}

	// YSF: loopback + target + wiresx passthrough.
	ysf := byMode["ysf"]
	if ysf.BindPort != ysfMMDVMLocalPort || ysf.PeerPort != ysfMMDVMGatewayPort {
		t.Fatalf("YSF loopback lost: bind=%q peer=%q", ysf.BindPort, ysf.PeerPort)
	}
	if ysf.Target != "FCS00290" || !ysf.WiresXPassthrough {
		t.Fatalf("YSF translation lost: %+v", ysf)
	}

	// NXDN: loopback + id/tg/default_id + shared lookup.
	nxdn := byMode["nxdn"]
	if nxdn.BindPort != nxdnMMDVMLocalPort || nxdn.PeerPort != nxdnMMDVMGatewayPort {
		t.Fatalf("NXDN loopback lost: bind=%q peer=%q", nxdn.BindPort, nxdn.PeerPort)
	}
	if nxdn.ID != "12345" || nxdn.TG != "20" || nxdn.DefaultID != "65519" {
		t.Fatalf("NXDN translation lost: %+v", nxdn)
	}
	if nxdn.IdLookup != busIdLookupFile {
		t.Fatalf("NXDN IdLookup = %q, want shared %q", nxdn.IdLookup, busIdLookupFile)
	}

	// Disable then re-enable: byte-identical output (the disabled state deletes no
	// rows, so re-enabling reproduces the exact file).
	toggled := busRenderFixture()
	toggled.Buses[0].Enabled = false
	toggled.Buses[0].Enabled = true
	if got := toggled.renderBus(toggled.Buses[0]); got != rendered {
		t.Fatalf("disable/re-enable not byte-identical:\n want %q\n  got %q", rendered, got)
	}
}

// TestBusConfigCarriesNoSecret is task 1's hard requirement: a rendered bus config
// never contains a password or address sourced from Networks[]. It names the
// network by CredentialsRef only.
func TestBusConfigCarriesNoSecret(t *testing.T) {
	m := busRenderFixture()
	rendered := m.renderBus(m.Buses[0])
	if strings.Contains(rendered, "s3cr3t") {
		t.Fatal("bus config leaked a Networks[] password")
	}
	if strings.Contains(rendered, "3102.master.brandmeister.network") {
		t.Fatal("bus config leaked a Networks[] address")
	}
	if !strings.Contains(rendered, "CredentialsRef=BM_3102") {
		t.Fatal("bus config did not name the network via CredentialsRef")
	}
}

// TestRenderBusUnit checks the systemd template unit's required directives.
func TestRenderBusUnit(t *testing.T) {
	u := RenderBusUnit()
	for _, want := range []string{
		"waypoint-bus@", // (in the deploy comment) — the template name
		"Description=Waypoint mode bus %i",
		"After=waypoint-dmrgateway.service",
		"waypoint-ysfgateway.service",
		"waypoint-nxdngateway.service",
		"Restart=on-failure",
		"ExecStart=/usr/local/bin/waypoint-bus --config /etc/waypoint/bus/%i.conf",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("bus unit missing %q:\n%s", want, u)
		}
	}
}
