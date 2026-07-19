package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/KN4OQW/waypoint/internal/store"
)

// This file is the model/store half of RFC-0003 (mode buses): the two store
// sections (buses[], attachments[]), the attach-time validator, and the
// validating write paths. There is no renderer or daemon here — that is a
// follow-up; this is the seam the rest of the bus work builds against.

// reframeModes is RFC-0003's committed scope: the AMBE+2 2450x1150 family that
// converts by packet reframing, with no vocoder, no firmware, and no licensing
// question (§2, Reframe tier). A bus is valid iff every attached mode is in this
// set.
var reframeModes = map[Mode]bool{ModeDMR: true, ModeYSF: true, ModeNXDN: true}

// transcodeModes are the distinct-codec modes MMDVM_CM can only convert through a
// licensed software/hardware vocoder (§2, Transcode tier — deferred). They are
// enumerated so a refusal can say "transcode tier not available" instead of a
// generic error, and so a mode like D-Star is classified (transcode) rather than
// mistaken for unsupported.
var transcodeModes = map[Mode]bool{ModeDStar: true, ModeP25: true, ModeM17: true}

// converterPairs are the unordered mode pairs for which juribeparada/MMDVM_CM
// ships a converter tool (RFC-0003 §2 rule 2 enumerates the 16-tool inventory).
// A pair absent here has no converter at all — the strongest refusal. A pair
// present here but touching a transcode-tier mode has a converter that only the
// deferred transcode tier can run. Keyed by canonicalPair(a, b).
//
// Derived tool-by-tool: DMR2M17{dmr,m17}, DMR2NXDN{dmr,nxdn}, DMR2P25{dmr,p25},
// DMR2YSF/YSF2DMR{dmr,ysf}, DSTAR2YSF{dstar,ysf}, M172YSF{m17,ysf},
// YSF2NXDN{nxdn,ysf}, YSF2P25{p25,ysf}. (M172DMR and P252DMR duplicate
// {dmr,m17}/{dmr,p25}; USRP2* bridges analog AllStar, not a digital mode pair.)
var converterPairs = map[string]bool{
	canonicalPair(ModeDMR, ModeM17):   true,
	canonicalPair(ModeDMR, ModeNXDN):  true,
	canonicalPair(ModeDMR, ModeP25):   true,
	canonicalPair(ModeDMR, ModeYSF):   true,
	canonicalPair(ModeDStar, ModeYSF): true,
	canonicalPair(ModeM17, ModeYSF):   true,
	canonicalPair(ModeNXDN, ModeYSF):  true,
	canonicalPair(ModeP25, ModeYSF):   true,
}

// modeRank orders modes for deterministic pair display. Transcode/unsupported
// modes rank ahead of reframe modes so a no-converter reason reads naturally in
// the direction RFC-0003 writes it ("no converter for D-Star<->DMR", not
// "DMR<->D-Star").
func modeRank(m Mode) int {
	switch m {
	case ModeDStar:
		return 0
	case ModeP25:
		return 1
	case ModeM17:
		return 2
	case ModeYSFVW:
		return 3
	case ModeDMR:
		return 10
	case ModeYSF:
		return 11
	case ModeNXDN:
		return 12
	default:
		return 100
	}
}

// canonicalPair keys a mode pair independent of order (lower rank first).
func canonicalPair(a, b Mode) string {
	if modeRank(b) < modeRank(a) {
		a, b = b, a
	}
	return string(a) + "|" + string(b)
}

// modeLabel is the human-readable mode name used verbatim in refusal reasons
// (RFC-0003 §2: reasons are data the UI surfaces, not log lines).
func modeLabel(m Mode) string {
	switch m {
	case ModeDMR:
		return "DMR"
	case ModeYSF:
		return "YSF"
	case ModeNXDN:
		return "NXDN"
	case ModeDStar:
		return "D-Star"
	case ModeP25:
		return "P25"
	case ModeM17:
		return "M17"
	case ModeYSFVW:
		return "YSF VW"
	case ModeFM:
		return "FM"
	case ModePOCSAG:
		return "POCSAG"
	default:
		return string(m)
	}
}

// busModeSetReason is the heart of the attach-time validator: it decides whether
// a set of modes may share one bus and, if not, returns the human-readable reason
// RFC-0003 §2 specifies. It is a pure function of the mode set — the table-driven
// validity matrix (§6.3) drives it directly.
//
// Rules, in refusal priority (most specific first):
//  1. An unsupported mode (FM/POCSAG data/analog, YSF-VW full-rate) can never
//     attach — named individually.
//  2. Committed scope is the reframe tier: any subset of {DMR,YSF,NXDN} is valid.
//  3. Otherwise a transcode-tier mode is present. A pair with no MMDVM_CM
//     converter at all refuses "no converter for A<->B"; a pair whose converter
//     exists but needs the deferred transcode tier refuses "transcode tier not
//     available".
func busModeSetReason(modes []Mode) (ok bool, reason string) {
	// Sort by rank so both the unsupported scan and the pair scan are deterministic
	// (the first offending pair reported is stable across calls).
	sorted := append([]Mode(nil), modes...)
	sort.Slice(sorted, func(i, j int) bool { return modeRank(sorted[i]) < modeRank(sorted[j]) })

	// Rule 1: reject any mode that is neither reframe nor transcode tier, naming it.
	for _, m := range sorted {
		if !reframeModes[m] && !transcodeModes[m] {
			if m == ModeYSFVW {
				return false, "YSF VW is outside the reframe envelope (DN only)"
			}
			return false, fmt.Sprintf("%s is not a bus-capable mode", modeLabel(m))
		}
	}

	// Rule 3: scan every unordered pair. Prefer the no-converter refusal (more
	// specific) over the transcode-tier refusal.
	transcodeSeen := false
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			a, b := sorted[i], sorted[j]
			if reframeModes[a] && reframeModes[b] {
				continue // reframe pair — committed scope
			}
			if converterPairs[canonicalPair(a, b)] {
				transcodeSeen = true // converter exists, but transcode tier is deferred
				continue
			}
			return false, fmt.Sprintf("no converter for %s<->%s", modeLabel(a), modeLabel(b))
		}
	}
	if transcodeSeen {
		return false, "transcode tier not available"
	}

	// No offending pair. Either all modes are reframe (valid) or a single
	// transcode-tier mode sits alone on the bus (still outside committed scope).
	for _, m := range sorted {
		if !reframeModes[m] {
			return false, "transcode tier not available"
		}
	}
	return true, ""
}

// ValidateBuses is the RFC-0003 attach-time validator (§2, §5) as a pure function
// of the three store sections it spans. It never fails at runtime: an invalid
// (buses, attachments) pair is refused on save, so a persisted bus is always
// startable. The error message is the human-readable reason the UI surfaces
// verbatim (Prompt 5).
//
// It enforces:
//   - every attachment's bus_id references an existing bus (§4);
//   - every non-blank credentials_ref resolves to a Networks[] entry (§3) — the
//     attachment never embeds a secret;
//   - a mode appears in at most one attachment across ALL buses (§5 rule 3);
//   - each bus's mode set is attachable together (busModeSetReason, §2).
func ValidateBuses(buses []Bus, attachments []Attachment, networks []Network) error {
	busByID := make(map[string]bool, len(buses))
	for _, b := range buses {
		if b.ID == "" {
			return fmt.Errorf("bus has an empty id")
		}
		if busByID[b.ID] {
			return fmt.Errorf("duplicate bus id %q", b.ID)
		}
		busByID[b.ID] = true
	}

	netByName := make(map[string]bool, len(networks))
	for _, n := range networks {
		netByName[n.Name] = true
	}

	seenMode := make(map[Mode]string, len(attachments)) // mode -> bus_id it is already on
	modesByBus := make(map[string][]Mode, len(buses))
	for _, a := range attachments {
		if a.Mode == "" {
			return fmt.Errorf("attachment has an empty mode")
		}
		if !busByID[a.BusID] {
			return fmt.Errorf("attachment for %s references unknown bus %q", modeLabel(a.Mode), a.BusID)
		}
		if a.CredentialsRef != "" && !netByName[a.CredentialsRef] {
			return fmt.Errorf("attachment for %s references unknown network %q", modeLabel(a.Mode), a.CredentialsRef)
		}
		if prev, dup := seenMode[a.Mode]; dup {
			// §5 rule 3: one mode, one attachment, one bus — structurally prevents
			// cross-bus ping-pong. Reject the second attachment of the same mode.
			if prev == a.BusID {
				return fmt.Errorf("mode %s is attached to bus %q more than once", modeLabel(a.Mode), a.BusID)
			}
			return fmt.Errorf("mode %s is attached to more than one bus (%q and %q)", modeLabel(a.Mode), prev, a.BusID)
		}
		seenMode[a.Mode] = a.BusID
		modesByBus[a.BusID] = append(modesByBus[a.BusID], a.Mode)
	}

	// Validate each bus's mode set in a stable bus order so the reported refusal is
	// deterministic across calls.
	busIDs := make([]string, 0, len(modesByBus))
	for id := range modesByBus {
		busIDs = append(busIDs, id)
	}
	sort.Strings(busIDs)
	for _, id := range busIDs {
		if ok, reason := busModeSetReason(modesByBus[id]); !ok {
			return fmt.Errorf("bus %q: %s", id, reason)
		}
	}
	return nil
}

// DefaultBuses / DefaultAttachments seed a store that predates RFC-0003 with the
// empty sections, so Load never returns a nil surprise and a fresh node starts
// with no buses (the migration from the dormant bridge sections is a separate
// step).
func DefaultBuses() []Bus              { return []Bus{} }
func DefaultAttachments() []Attachment { return []Attachment{} }

// SetBuses writes the buses[] section, rejecting unknown fields like SetSection
// and re-running the attach-time validator against the CURRENTLY stored
// attachments and networks — so removing or renaming a bus that an attachment
// still references is refused here rather than persisting a dangling attachment.
func SetBuses(s *store.Store, raw []byte, by string) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var buses []Bus
	if err := dec.Decode(&buses); err != nil {
		return err
	}
	var attachments []Attachment
	if _, err := s.GetInto("attachments", &attachments); err != nil {
		return err
	}
	var networks []Network
	if _, err := s.GetInto("networks", &networks); err != nil {
		return err
	}
	if err := ValidateBuses(buses, attachments, networks); err != nil {
		return err
	}
	return s.Set("buses", buses, by)
}

// SetAttachments writes the attachments[] section through the attach-time
// validator (RFC-0003 §2, §5): the incoming list is validated against the stored
// buses and networks, so an invalid bus can never be persisted (dangling bus_id,
// dangling credentials_ref, a mode on two buses, or a non-reframe mode set are
// all refused here). Unknown fields are rejected, matching SetSection.
func SetAttachments(s *store.Store, raw []byte, by string) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var attachments []Attachment
	if err := dec.Decode(&attachments); err != nil {
		return err
	}
	var buses []Bus
	if _, err := s.GetInto("buses", &buses); err != nil {
		return err
	}
	var networks []Network
	if _, err := s.GetInto("networks", &networks); err != nil {
		return err
	}
	if err := ValidateBuses(buses, attachments, networks); err != nil {
		return err
	}
	return s.Set("attachments", attachments, by)
}
