package config

import (
	"bytes"
	"encoding/json"

	"github.com/KN4OQW/waypoint/internal/store"
)

// SetNetworks replaces the DMR-network list. Because the API View never exposes
// passwords, an incoming network that leaves its password blank means "keep the
// stored one" — matched to the existing network by Name. A non-blank password
// replaces it; a network absent from the list is removed; a new one is added.
// This lets the UI edit the network array without ever handling secrets.
func SetNetworks(s *store.Store, raw []byte, by string) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var incoming []Network
	if err := dec.Decode(&incoming); err != nil {
		return err
	}

	var existing []Network
	if _, err := s.GetInto("networks", &existing); err != nil {
		return err
	}
	prior := make(map[string]string, len(existing))
	for _, n := range existing {
		prior[n.Name] = n.Password
	}
	for i := range incoming {
		if incoming[i].Password == "" {
			incoming[i].Password = prior[incoming[i].Name]
		}
	}
	return s.Set("networks", incoming, by)
}
