package netconfig

import (
	"bytes"
	"encoding/json"

	"github.com/KN4OQW/waypoint/internal/store"
)

// Set writes the netconfig Model from a partial JSON body with the same
// write-only-secret rule the radio family uses for passwords: the API View never
// exposes a Wi-Fi PSK, so an incoming connection that leaves its PSK blank means
// "keep the stored one", matched to the existing connection by Name. A non-blank
// PSK replaces it; a connection absent from the body is removed; a new one is
// added. This lets the UI edit the connection array without ever handling a secret.
//
// Top-level fields (hostname, timezone, ntp) are merged over the stored model, so
// a body that sends only the connection list never drops the hostname, and vice
// versa. Unknown JSON fields are rejected (parity with internal/config.SetSection).
// The merged model is validated before it is committed, so a malformed connection
// set is refused at save time rather than at apply.
func Set(s *store.Store, raw []byte, by string) error {
	existing, err := Load(s)
	if err != nil {
		return err
	}

	// Snapshot the stored PSKs (by connection name) BEFORE decoding. Decoding the
	// body's connection array into a copy of the stored model reuses the stored
	// slice's backing array (encoding/json decodes into existing elements), which
	// would wipe existing.Connections[i].WiFi.PSK to the body's blank — so the
	// prior secrets must be captured first.
	prior := make(map[string]string, len(existing.Connections))
	for _, c := range existing.Connections {
		prior[c.Name] = c.WiFi.PSK
	}

	// Merge onto a copy of the stored model so unspecified top-level fields (and,
	// when the body omits "connections" entirely, the whole connection list)
	// survive.
	merged := existing
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&merged); err != nil {
		return err
	}

	// Reconcile PSK secrets: a blank incoming PSK inherits the stored one for the
	// same connection name. Done after the merge so a blank field can never wipe a
	// stored credential (defense in depth: the UI also omits it when blank).
	if bodyHasConnections(raw) {
		for i := range merged.Connections {
			if merged.Connections[i].WiFi.PSK == "" {
				merged.Connections[i].WiFi.PSK = prior[merged.Connections[i].Name]
			}
		}
	}

	if err := merged.Validate(); err != nil {
		return err
	}
	return s.Set(storeKey, &merged, by)
}

// bodyHasConnections reports whether the raw body carried a "connections" key at
// all. Without this check, a body that only updates the hostname would decode
// into merged.Connections == existing.Connections (preserved by the merge) and the
// PSK reconciliation would still run harmlessly — but gating it keeps the intent
// explicit and avoids re-walking the list on unrelated writes.
func bodyHasConnections(raw []byte) bool {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return false
	}
	_, ok := probe["connections"]
	return ok
}
