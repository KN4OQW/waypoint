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

// SetCrossBridge writes one cross-mode bridge section (ysf2dmr, dmr2ysf, …) with
// the same write-only-secret rule the DMR networks and the D-Star gateway use: a
// blank "password" in the body means "keep the stored one". The redacted view
// never carries the DMR-master password, so the UI PUTs the section without it;
// this strips a blank password from the body before delegating to SetSection, so
// its merge keeps the stored secret. A non-blank password replaces it. Bridges
// with no secret (dmr2ysf, ysf2nxdn, dmr2nxdn) simply carry no "password" key and
// pass straight through; an unexpected password on one of those is rejected by
// SetSection's DisallowUnknownFields exactly as before. Returns known=false for
// an unrecognized section, mirroring SetSection.
func SetCrossBridge(s *store.Store, section string, raw []byte, by string) (known bool, err error) {
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		return true, err
	}
	if pw, ok := body["password"]; ok && blankJSONString(pw) {
		delete(body, "password")
		if raw, err = json.Marshal(body); err != nil {
			return true, err
		}
	}
	return SetSection(s, section, raw, by)
}

// blankJSONString reports whether a JSON value is the empty (or whitespace-only)
// string. A non-string value is not blank — SetSection will reject it downstream.
func blankJSONString(raw json.RawMessage) bool {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return false
	}
	return len(bytes.TrimSpace([]byte(s))) == 0
}
