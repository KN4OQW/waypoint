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

// SetDStarGateway writes the D-Star gateway section (dstargw) with the same
// write-only-secret rule the DMR networks use: the API View never exposes the
// ircDDB password, so a blank incoming IRCDDBPassword means "keep the stored
// one", and a non-blank one replaces it. Like SetSection this is a merge — the
// body is decoded over the stored section, so a UI that sends only the fields it
// manages never drops the rest — but the secret is reconciled after the merge so
// a blank field can never wipe the stored password (defense in depth: the UI also
// omits the field when blank). Unknown fields are rejected, matching SetSection.
func SetDStarGateway(s *store.Store, raw []byte, by string) error {
	var existing DStarGateway
	if _, err := s.GetInto("dstargw", &existing); err != nil {
		return err
	}
	// Merge onto a copy of the stored section so unspecified fields survive.
	incoming := existing
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&incoming); err != nil {
		return err
	}
	if incoming.IRCDDBPassword == "" {
		incoming.IRCDDBPassword = existing.IRCDDBPassword
	}
	return s.Set("dstargw", &incoming, by)
}
