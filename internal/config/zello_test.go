package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func okAccount() ZelloAccount {
	return ZelloAccount{Name: "bridge", Username: "kn4oqw-gw", Password: "pw", AuthToken: "jwt", Enabled: true}
}

func mintingAccount() ZelloAccount {
	return ZelloAccount{Name: "bridge", Username: "kn4oqw-gw", Password: "pw",
		Issuer: "ISS.abc", PrivateKey: "-----BEGIN PRIVATE KEY-----x-----END PRIVATE KEY-----", Enabled: true}
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
			name:     "an enabled channel with no credentials at all",
			accounts: []ZelloAccount{func() ZelloAccount { a := okAccount(); a.AuthToken = ""; return a }()},
			channels: []ZelloChannel{okChannel()},
			wants:    "developers.zello.com",
		},
		{
			name:     "an account with no username",
			accounts: []ZelloAccount{func() ZelloAccount { a := okAccount(); a.Username = ""; return a }()},
			channels: []ZelloChannel{okChannel()},
			wants:    "dedicated Zello account",
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

// Corrected against the live service. API.md says an omitted username connects
// anonymously, and this test used to assert that a listen-only channel therefore
// needed no account. Measured with a valid freshly minted token, both an
// anonymous logon and an anonymous listen-only logon are refused with
// `invalid username`, a code the documented error table does not contain.
//
// So a bridge always needs a dedicated Zello account, and the validator says so
// while the operator is configuring rather than at the first connect.
func TestEveryChannelNeedsAnAccountIncludingListenOnly(t *testing.T) {
	a := okAccount()
	a.Username = ""
	c := okChannel()
	c.ListenOnly = true
	err := ValidateZello([]ZelloAccount{a}, []ZelloChannel{c}, buses())
	if err == nil {
		t.Fatal("an anonymous listen-only channel was accepted; Zello refuses that logon")
	}
	if !strings.Contains(err.Error(), "dedicated Zello account") {
		t.Errorf("error %q should tell the operator they need their own account", err)
	}
}

// Key material is the arrangement that does not expire, so an account carrying it
// needs no pre-minted token at all.
func TestAnAccountWithKeyMaterialNeedsNoToken(t *testing.T) {
	if err := ValidateZello([]ZelloAccount{mintingAccount()}, []ZelloChannel{okChannel()}, buses()); err != nil {
		t.Fatalf("an account that can mint its own tokens was refused: %v", err)
	}
	if !mintingAccount().CanMintTokens() {
		t.Error("CanMintTokens() is false for an account with an issuer and a private key")
	}
	if okAccount().CanMintTokens() {
		t.Error("CanMintTokens() is true for an account with only a pasted token")
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
	m.ZelloAccounts[0].Issuer = "ISS.abc"
	m.ZelloAccounts[0].PrivateKey = "-----BEGIN PRIVATE KEY-----SECRETKEYMATERIAL-----END PRIVATE KEY-----"
	b, err = json.Marshal(m.View(Sources{}))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{`"pw"`, `"jwt"`, `"password"`, `"auth_token"`, "SECRETKEYMATERIAL", `"private_key"`} {
		if strings.Contains(string(b), secret) {
			t.Errorf("the view carried %s: %s", secret, b)
		}
	}
	if !strings.Contains(string(b), `"has_password":true`) || !strings.Contains(string(b), `"has_auth_token":true`) {
		t.Errorf("the view did not report the secrets as present: %s", b)
	}
	if !strings.Contains(string(b), `"has_private_key":true`) || !strings.Contains(string(b), `"can_mint_tokens":true`) {
		t.Errorf("the view did not report the key material: %s", b)
	}
	// The issuer is not a secret and the panel needs it to show which key is in use.
	if !strings.Contains(string(b), `"issuer":"ISS.abc"`) {
		t.Errorf("the view dropped the issuer, which is public: %s", b)
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

// The panel editing an account never has the secrets, because the View does not
// carry them. Without blank-preserve, renaming a channel or toggling Enabled
// would silently erase the token and the bridge would stop connecting with
// nothing on screen to explain it.
func TestZelloAccountSecretsSurviveAnEditThatOmitsThem(t *testing.T) {
	st := newStore(t)
	if err := SetBuses(st, []byte(`[{"id":"b1","name":"Bus 1","enabled":true}]`), "test"); err != nil {
		t.Fatal(err)
	}
	first := `[{"name":"bridge","username":"gw","password":"pw","auth_token":"jwt","enabled":true}]`
	if err := SetZelloAccounts(st, []byte(first), "test"); err != nil {
		t.Fatal(err)
	}

	// The shape a UI would send back: everything it can see, nothing it cannot.
	second := `[{"name":"bridge","username":"gw2","password":"","auth_token":"","enabled":true}]`
	if err := SetZelloAccounts(st, []byte(second), "test"); err != nil {
		t.Fatal(err)
	}

	var got []ZelloAccount
	if _, err := st.GetInto("zello_accounts", &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d accounts", len(got))
	}
	if got[0].Username != "gw2" {
		t.Errorf("the edit did not apply: username = %q", got[0].Username)
	}
	if got[0].Password != "pw" || got[0].AuthToken != "jwt" {
		t.Errorf("secrets were erased by an edit that omitted them: %+v", got[0])
	}
}

// A non-blank secret still replaces, or rotating an expired token would be
// impossible — which it will be every 30 days on a sample development token.
func TestANonBlankZelloSecretReplaces(t *testing.T) {
	st := newStore(t)
	if err := SetBuses(st, []byte(`[{"id":"b1","name":"Bus 1","enabled":true}]`), "test"); err != nil {
		t.Fatal(err)
	}
	if err := SetZelloAccounts(st, []byte(`[{"name":"bridge","username":"gw","password":"pw","auth_token":"old","enabled":true}]`), "test"); err != nil {
		t.Fatal(err)
	}
	if err := SetZelloAccounts(st, []byte(`[{"name":"bridge","username":"gw","password":"","auth_token":"new","enabled":true}]`), "test"); err != nil {
		t.Fatal(err)
	}
	var got []ZelloAccount
	if _, err := st.GetInto("zello_accounts", &got); err != nil {
		t.Fatal(err)
	}
	if got[0].AuthToken != "new" {
		t.Errorf("token = %q, want the rotated value", got[0].AuthToken)
	}
	if got[0].Password != "pw" {
		t.Errorf("password = %q; the untouched secret should have been preserved", got[0].Password)
	}
}

// A channel that would dangle must be refused at the write, not discovered at
// render time when the daemon is already trying to start.
func TestWritingADanglingZelloChannelIsRefused(t *testing.T) {
	st := newStore(t)
	if err := SetBuses(st, []byte(`[{"id":"b1","name":"Bus 1","enabled":true}]`), "test"); err != nil {
		t.Fatal(err)
	}
	body := `[{"id":"z1","bus_id":"b1","channel":"Ham","account_ref":"missing","enabled":true}]`
	if err := SetZelloChannels(st, []byte(body), "test"); err == nil {
		t.Fatal("a channel referencing a non-existent account was persisted")
	}
}
