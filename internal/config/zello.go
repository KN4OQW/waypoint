package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/KN4OQW/waypoint/internal/store"
)

// This file is the model/store half of Zello bridging: two sections
// (zello_accounts[], zello_channels[]) and their validator.
//
// A Zello channel is NOT an entry in attachments[]. That was the tempting shape
// and it is the wrong one. attachments[] carries a *mode* onto a bus, and every
// rule over it — the reframe tier, the converter-pair table, "a mode appears on
// at most one bus" — is about modes converting to other modes. A Zello channel
// converts to nothing; it leaves the AMBE world entirely at the endpoint edge.
// Putting it in attachments[] would mean teaching busModeSetReason about a
// pseudo-mode that satisfies none of its rules, and every existing bus would be
// revalidated against a table that had grown a member it cannot reason about.
//
// Kept separate, the mode-set validator is untouched and the headline case works
// out of the box: a bus with one DMR attachment is already valid today (the pair
// scan finds no pairs), so "DMR talkgroup <-> one Zello channel" needs no change
// to what a legal bus is.
//
// The account/channel split mirrors networks[]/attachments[]: the secret lives in
// one place and is referenced by name, so a token is stored once however many
// channels use it, and rotating it is one edit.

// ZelloAccount is one Zello identity the node can talk as.
//
// Waypoint never ships an account or a token. The operator creates a dedicated
// bridge account and obtains a key from developers.zello.com; the sample
// development token that yields expires after 30 days, which is an operational
// fact the UI has to surface rather than a one-time setup step.
type ZelloAccount struct {
	// Name identifies the account within the node. Referenced by
	// ZelloChannel.AccountRef.
	Name string `json:"name"`

	// Username is the Zello account the gateway talks as. Without it the
	// connection is anonymous, which Zello treats as listen-only.
	Username string `json:"username"`

	// Password is the account password. Write-only in projections: the View
	// carries HasPassword, never this.
	Password string `json:"password"`

	// AuthToken is the JWT. Write-only in projections for the same reason —
	// it is a bearer credential for the operator's Zello account.
	AuthToken string `json:"auth_token"`

	Enabled bool `json:"enabled"`
}

// ZelloChannel bridges one bus to one Zello channel.
//
// One row is one WebSocket connection, because consumer Zello supports exactly
// one channel per logon — API.md: "Connecting to multiple channels (up to 100) is
// currently supported for Zello Work only." A bus that fans out to three channels
// is three rows and three connections.
type ZelloChannel struct {
	// ID is the row's stable identifier.
	ID string `json:"id"`

	// BusID names the bus this channel attaches to (RFC-0003 §4).
	BusID string `json:"bus_id"`

	// Channel is the Zello channel name, as it appears in the Zello app.
	Channel string `json:"channel"`

	// AccountRef names the ZelloAccount to log on with. Never the credential
	// itself — the channel row is safe to display.
	AccountRef string `json:"account_ref"`

	// ListenOnly joins without transmitting. Set it deliberately rather than by
	// leaving the account's username blank, so a receive-only bridge is visible
	// as a choice and a misconfigured account is not mistaken for one.
	ListenOnly bool `json:"listen_only"`

	// PacketMS is the Opus packet duration. Zero means DefaultZelloPacketMS.
	// Zello documents 2.5-60 ms; Opus itself only has 5, 10, 20, 40 and 60, and
	// the validator holds to the intersection.
	PacketMS int `json:"packet_ms,omitempty"`

	Enabled bool `json:"enabled"`
}

// DefaultZelloPacketMS is Zello's own default, from the codec_header example
// gD4BPA== in their documentation: 60 ms. It costs latency and saves bandwidth
// against 20 ms, and matching the vendor's default is the safer starting point
// for a bridge whose far end is every Zello client in the channel.
const DefaultZelloPacketMS = 60

// zelloPacketSizes is the intersection of Zello's documented 2.5-60 ms range and
// the frame sizes Opus actually has. A duration inside Zello's range but not an
// Opus frame size — 30 ms is the obvious one — is refused here, because libopus
// rejects it later with an error that does not say which value was wrong.
var zelloPacketSizes = map[int]bool{5: true, 10: true, 20: true, 40: true, 60: true}

// DefaultZelloAccounts and DefaultZelloChannels are empty: a fresh node bridges
// nothing, and the feature is inert until an operator supplies an account.
func DefaultZelloAccounts() []ZelloAccount { return []ZelloAccount{} }
func DefaultZelloChannels() []ZelloChannel { return []ZelloChannel{} }

// ValidateZello is the save-time validator, a pure function of the sections it
// spans so an invalid pair can never persist.
//
// It enforces:
//   - a channel's bus_id references an existing bus;
//   - a channel's account_ref resolves to a ZelloAccount — the channel row never
//     embeds a secret;
//   - account names and channel ids are unique;
//   - one row per (bus, channel): the same channel twice on one bus would be two
//     connections echoing each other's traffic back onto the bus;
//   - a packet duration Opus and Zello both accept;
//   - an enabled channel names a channel and an enabled account that can do what
//     the row asks — an account with no username cannot transmit, so pairing one
//     with a non-listen-only channel is refused rather than discovered at the
//     first key-up as "listen only connection".
func ValidateZello(accounts []ZelloAccount, channels []ZelloChannel, buses []Bus) error {
	byName := make(map[string]ZelloAccount, len(accounts))
	for _, a := range accounts {
		name := strings.TrimSpace(a.Name)
		if name == "" {
			return fmt.Errorf("a Zello account has no name; give it one so channels can reference it")
		}
		if _, dup := byName[name]; dup {
			return fmt.Errorf("two Zello accounts are both named %q; names must be unique", name)
		}
		byName[name] = a
	}

	busIDs := make(map[string]bool, len(buses))
	for _, b := range buses {
		busIDs[b.ID] = true
	}

	seenID := make(map[string]bool, len(channels))
	seenPair := make(map[string]bool, len(channels))
	for _, c := range channels {
		id := strings.TrimSpace(c.ID)
		if id == "" {
			return fmt.Errorf("a Zello channel row has no id")
		}
		if seenID[id] {
			return fmt.Errorf("two Zello channel rows share the id %q", id)
		}
		seenID[id] = true

		if !busIDs[c.BusID] {
			return fmt.Errorf("Zello channel %q references bus %q, which does not exist", id, c.BusID)
		}

		name := strings.TrimSpace(c.Channel)
		if name == "" {
			return fmt.Errorf("Zello channel %q has no channel name; enter the channel as it appears in Zello", id)
		}
		pair := c.BusID + "\x00" + name
		if seenPair[pair] {
			return fmt.Errorf("bus %q is bridged to the Zello channel %q twice; "+
				"two connections to one channel would echo each other's traffic back onto the bus", c.BusID, name)
		}
		seenPair[pair] = true

		if c.PacketMS != 0 && !zelloPacketSizes[c.PacketMS] {
			return fmt.Errorf("Zello channel %q has a packet size of %d ms; Opus supports 5, 10, 20, 40 or 60",
				id, c.PacketMS)
		}

		ref := strings.TrimSpace(c.AccountRef)
		if ref == "" {
			return fmt.Errorf("Zello channel %q names no account; add a Zello account and select it", id)
		}
		acct, ok := byName[ref]
		if !ok {
			return fmt.Errorf("Zello channel %q references the account %q, which does not exist", id, ref)
		}

		if !c.Enabled {
			continue
		}
		if !acct.Enabled {
			return fmt.Errorf("Zello channel %q is enabled but its account %q is not; enable the account or disable the channel", id, ref)
		}
		if strings.TrimSpace(acct.AuthToken) == "" {
			return fmt.Errorf("Zello account %q has no auth token; obtain one from developers.zello.com "+
				"(a sample development token expires after 30 days)", ref)
		}
		if !c.ListenOnly && strings.TrimSpace(acct.Username) == "" {
			return fmt.Errorf("Zello channel %q transmits but its account %q has no username; "+
				"an anonymous Zello connection can only listen, so add a username or mark the channel listen-only", id, ref)
		}
	}
	return nil
}

// EffectivePacketMS resolves the channel's Opus packet duration.
func (c ZelloChannel) EffectivePacketMS() int {
	if c.PacketMS == 0 {
		return DefaultZelloPacketMS
	}
	return c.PacketMS
}

// SetZelloAccounts writes the zello_accounts[] section through the validator,
// against the currently stored channels and buses.
func SetZelloAccounts(s *store.Store, raw []byte, by string) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var accounts []ZelloAccount
	if err := dec.Decode(&accounts); err != nil {
		return err
	}
	var channels []ZelloChannel
	if _, err := s.GetInto("zello_channels", &channels); err != nil {
		return err
	}
	var buses []Bus
	if _, err := s.GetInto("buses", &buses); err != nil {
		return err
	}
	if err := ValidateZello(accounts, channels, buses); err != nil {
		return err
	}
	return s.Set("zello_accounts", accounts, by)
}

// SetZelloChannels writes the zello_channels[] section through the validator,
// against the currently stored accounts and buses.
func SetZelloChannels(s *store.Store, raw []byte, by string) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var channels []ZelloChannel
	if err := dec.Decode(&channels); err != nil {
		return err
	}
	var accounts []ZelloAccount
	if _, err := s.GetInto("zello_accounts", &accounts); err != nil {
		return err
	}
	var buses []Bus
	if _, err := s.GetInto("buses", &buses); err != nil {
		return err
	}
	if err := ValidateZello(accounts, channels, buses); err != nil {
		return err
	}
	return s.Set("zello_channels", channels, by)
}
