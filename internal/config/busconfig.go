package config

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// BusConfig is a parsed bus config file — the reader half of the format defined
// in busrender.go. The bus daemon (Prompt 3) imports this to load its endpoints;
// the round-trip test uses it to prove no translation param is lost across
// render -> parse. Fields mirror the rendered keys one-for-one.
type BusConfig struct {
	ID          string
	Name        string
	Attachments []BusAttachment
}

// BusAttachment is one attachment row inside a bus config: its mode, the loopback
// endpoints it binds/peers, and its translation params. Only the fields
// meaningful for the mode are populated (the others stay empty), matching what
// renderBus emitted.
type BusAttachment struct {
	Mode        string
	BindAddress string
	BindPort    string
	PeerAddress string
	PeerPort    string

	// DMR
	Slot           string
	DefaultTG      string
	TGMap          map[string]string
	CredentialsRef string

	// YSF
	Target            string
	WiresXPassthrough bool

	// NXDN
	ID        string
	TG        string
	DefaultID string

	HangTime string
	IdLookup string
}

// busAttachmentSections is the fixed set of attachment section names the reader
// recognises, in render order (mode rank). Only the reframe tier is expressible.
var busAttachmentSections = []struct {
	section string
	mode    string
}{
	{"DMR", string(ModeDMR)},
	{"YSF", string(ModeYSF)},
	{"NXDN", string(ModeNXDN)},
}

// ParseBusConfig reads a rendered bus config back into a BusConfig. It is the
// inverse of renderBus for every semantic field, so render -> parse loses no
// translation param. Unknown/extra keys are ignored (forward-compatible); a
// missing [Bus] section is an error.
func ParseBusConfig(r io.Reader) (*BusConfig, error) {
	ini, err := ParseINI(r)
	if err != nil {
		return nil, err
	}
	if !ini.Has("Bus") {
		return nil, fmt.Errorf("bus config: missing [Bus] section")
	}
	cfg := &BusConfig{
		ID:   ini.Get("Bus", "Id"),
		Name: ini.Get("Bus", "Name"),
	}
	for _, sm := range busAttachmentSections {
		if !ini.Has(sm.section) {
			continue
		}
		a := BusAttachment{
			Mode:              sm.mode,
			BindAddress:       ini.Get(sm.section, "BindAddress"),
			BindPort:          ini.Get(sm.section, "BindPort"),
			PeerAddress:       ini.Get(sm.section, "PeerAddress"),
			PeerPort:          ini.Get(sm.section, "PeerPort"),
			Slot:              ini.Get(sm.section, "Slot"),
			DefaultTG:         ini.Get(sm.section, "DefaultTG"),
			TGMap:             parseBusTGMap(ini.Get(sm.section, "TGMap")),
			CredentialsRef:    ini.Get(sm.section, "CredentialsRef"),
			Target:            ini.Get(sm.section, "Target"),
			WiresXPassthrough: ini.Bool(sm.section, "WiresXPassthrough"),
			ID:                ini.Get(sm.section, "Id"),
			TG:                ini.Get(sm.section, "TG"),
			DefaultID:         ini.Get(sm.section, "DefaultID"),
			HangTime:          ini.Get(sm.section, "HangTime"),
			IdLookup:          ini.Get(sm.section, "IdLookup"),
		}
		cfg.Attachments = append(cfg.Attachments, a)
	}
	return cfg, nil
}

// parseBusTGMap is the inverse of busTGMap: "src:dst,src:dst" -> map. An empty
// value yields a nil map (matching an attachment with no tg_map).
func parseBusTGMap(v string) map[string]string {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	out := map[string]string{}
	for _, pair := range strings.Split(v, ",") {
		i := strings.IndexByte(pair, ':')
		if i < 0 {
			continue
		}
		out[strings.TrimSpace(pair[:i])] = strings.TrimSpace(pair[i+1:])
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// SortedTGMapKeys returns a tg_map's source keys sorted — a small helper so the
// daemon (Prompt 3) can iterate a parsed map deterministically.
func SortedTGMapKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
