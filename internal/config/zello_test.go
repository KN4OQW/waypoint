package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func okAccount() ZelloAccount {
	return ZelloAccount{Name: "bridge", Username: "kn4oqw-gw", Password: "pw", AuthToken: "jwt", Enabled: true}
}

func okChannel() ZelloChannel {
	return ZelloChannel{ID: "z1", BusID: "b1", Channel: "Ham Radio", AccountRef: "bridge", Enabled: true}
}

func buses() []Bus { return []Bus{{ID: "b1", Name: "Bus 1", Enabled: true}} }

func TestAValidZelloPairIsAccepted(t *testing.T) {
	if err := ValidateZello([]ZelloAccount{okAccount()}, []ZelloChannel{okChannel()}, buses()); err != nil {
		t.Fatalf("a good configuration was refused: %v", err)
	}
}

// The headline case is a bus with one DMR attachment and one Zello channel.
// Keeping Zello out of attachments[] is what makes that work without touching the
// mode-set validator, so this asserts the validator still accepts it.
func TestASingleModeBusWithAZelloChannelIsValid(t *testing.T) {
	bs := buses()
	at := []Attachment{{BusID: "b1", Mode: ModeDMR}}
	if err := ValidateBuses(bs, at, nil); err != nil {
		t.Fatalf("one DMR attachment on a bus was refused: %v", err)
	}
	if err := ValidateZello([]ZelloAccount{okAccount()}, []ZelloChannel{okChannel()}, bs); err != nil {
		t.Fatalf("adding a Zello channel to it was refused: %v", err)
	}
}

func TestZelloValidationRejects(t *testing.T) {
	cases := []struct {
		name     string
		accounts []ZelloAccount
		channels []ZelloChannel
		wants    string
	}{
		{
			name:     "a channel on a bus that does not exist",
			accounts: []ZelloAccount{okAccount()},
			channels: []ZelloChannel{func() ZelloChannel { c := okChannel(); c.BusID = "nope"; return c }()},
			wants:    "does not exist",
		},
		{
			name:     "a channel naming an account that does not exist",
			accounts: []ZelloAccount{okAccount()},
			channels: []ZelloChannel{func() ZelloChannel { c := okChannel(); c.AccountRef = "other"; return c }()},
			wants:    "does not exist",
		},
		{
			name:     "the same channel twice on one bus",
			accounts: []ZelloAccount{okAccount()},
			channels: []ZelloChannel{okChannel(), func() ZelloChannel { c := okChannel(); c.ID = "z2"; return c }()},
			wants:    "echo each other",
		},
		{
			name:     "two accounts with one name",
			accounts: []ZelloAccount{okAccount(), okAccount()},
			channels: nil,
			wants:    "must be unique",
		},
		{
			name:     "a packet size Opus does not have",
			accounts: []ZelloAccount{okAccount()},
			channels: []ZelloChannel{func() ZelloChannel { c := okChannel(); c.PacketMS = 30; return c }()},
			wants:    "5, 10, 20, 40 or 60",
		},
		{
			name:     "an enabled channel on a disabled account",
			accounts: []ZelloAccount{func() ZelloAccount { a := okAccount(); a.Enabled = false; return a }()},
			channels: []ZelloChannel{okChannel()},
			wants:    "enable the account",
		},
		{
			name:     "an enabled channel with no token",
			accounts: []ZelloAccount{func() ZelloAccount { a := okAccount(); a.AuthToken = ""; return a }()},
			channels: []ZelloChannel{okChannel()},
			wants:    "developers.zello.com",
		},
		{
			// The failure this prevents is a transmit attempt that Zello refuses
			// with "listen only connection" at the first key-up — long after the
			// operator configured it and with nothing pointing at the cause.
			name:     "a transmitting channel on an account with no username",
			accounts: []ZelloAccount{func() ZelloAccount { a := okAccount(); a.Username = ""; return a }()},
			channels: []ZelloChannel{okChannel()},
			wants:    "can only listen",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateZello(c.accounts, c.channels, buses())
			if err == nil {
				t.Fatal("accepted")
			}
			if !strings.Contains(err.Error(), c.wants) {
				t.Errorf("error %q does not contain %q; it must say what to do", err, c.wants)
			}
		})
	}
}

// A listen-only channel needs no username, because an anonymous Zello connection
// can still receive. Refusing it would block a legitimate receive-only bridge.
func TestAListenOnlyChannelNeedsNoUsername(t *testing.T) {
	a := okAccount()
	a.Username = ""
	c := okChannel()
	c.ListenOnly = true
	if err := ValidateZello([]ZelloAccount{a}, []ZelloChannel{c}, buses()); err != nil {
		t.Fatalf("a listen-only channel with an anonymous account was refused: %v", err)
	}
}

// A disabled channel is not held to the rules an enabled one is: an operator must
// be able to save a half-configured row and come back to it.
func TestADisabledChannelIsNotHeldToTheRunningRules(t *testing.T) {
	a := okAccount()
	a.AuthToken = ""
	c := okChannel()
	c.Enabled = false
	if err := ValidateZello([]ZelloAccount{a}, []ZelloChannel{c}, buses()); err != nil {
		t.Fatalf("a disabled, half-configured channel was refused: %v", err)
	}
}

func TestPacketSizeDefaultsToZellosOwn(t *testing.T) {
	if got := (ZelloChannel{}).EffectivePacketMS(); got != DefaultZelloPacketMS {
		t.Errorf("EffectivePacketMS() = %d, want %d", got, DefaultZelloPacketMS)
	}
	if got := (ZelloChannel{PacketMS: 20}).EffectivePacketMS(); got != 20 {
		t.Errorf("an explicit 20 ms became %d", got)
	}
}

// The two secrets are write-only in projections. Reporting that one is unset must
// never disclose the other, so the view carries presence and nothing else.
func TestTheViewNeverCarriesAZelloSecret(t *testing.T) {
	m := &Model{
		ZelloAccounts: []ZelloAccount{okAccount()},
		ZelloChannels: []ZelloChannel{okChannel()},
	}
	b, err := json.Marshal(m.View(Sources{}))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{`"pw"`, `"jwt"`, `"password"`, `"auth_token"`} {
		if strings.Contains(string(b), secret) {
			t.Errorf("the view carried %s: %s", secret, b)
		}
	}
	if !strings.Contains(string(b), `"has_password":true`) || !strings.Contains(string(b), `"has_auth_token":true`) {
		t.Errorf("the view did not report the secrets as present: %s", b)
	}
}

// A profile is portable and shareable, so it must carry the channels (topology)
// and never the accounts (bearer credentials).
func TestProfilesCarryChannelsButNotAccounts(t *testing.T) {
	inProfile := func(section string) bool {
		for _, s := range profileSections {
			if s == section {
				return true
			}
		}
		return false
	}
	if !inProfile("zello_channels") {
		t.Error("zello_channels is not captured by a profile; it is connection topology like the bus it attaches to")
	}
	if inProfile("zello_accounts") {
		t.Error("zello_accounts is captured by a profile; it holds a bearer token and must never travel")
	}
}

// The rendered bus config is what the daemon reads, and it must join each enabled
// channel to its account. A disabled channel or a disabled account renders
// nothing, so a bus with no live channel renders exactly as it did before this
// feature existed.
func TestRenderedBusConfigJoinsChannelsToAccounts(t *testing.T) {
	m := &Model{
		Buses:         buses(),
		Attachments:   []Attachment{{BusID: "b1", Mode: ModeDMR}},
		ZelloAccounts: []ZelloAccount{okAccount()},
		ZelloChannels: []ZelloChannel{
			okChannel(),
			{ID: "z2", BusID: "b1", Channel: "Off", AccountRef: "bridge", Enabled: false},
		},
	}
	got := m.busZelloFor("b1")
	if len(got) != 1 {
		t.Fatalf("rendered %d endpoints, want 1 (the disabled row must not render)", len(got))
	}
	if got[0].Channel != "Ham Radio" || got[0].AuthToken != "jwt" || got[0].Username != "kn4oqw-gw" {
		t.Errorf("endpoint did not resolve its account: %+v", got[0])
	}
	if got[0].PacketMS != DefaultZelloPacketMS {
		t.Errorf("packet size = %d, want the default %d", got[0].PacketMS, DefaultZelloPacketMS)
	}

	m.ZelloAccounts[0].Enabled = false
	if len(m.busZelloFor("b1")) != 0 {
		t.Error("a channel whose account is disabled still rendered")
	}
}

func TestABusWithNoZelloChannelRendersNothingExtra(t *testing.T) {
	m := &Model{Buses: buses(), Attachments: []Attachment{{BusID: "b1", Mode: ModeDMR}}}
	if got := m.busZelloFor("b1"); got != nil {
		t.Errorf("a bus with no Zello channel rendered %+v", got)
	}
}
