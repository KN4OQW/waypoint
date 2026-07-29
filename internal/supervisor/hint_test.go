package supervisor

import "testing"

// Every status message the pinned DMRGateway (79edbc4) can emit about a network,
// mapped to the verdict it carries. If upstream rewords one of these the match
// goes silent, which is why this is one signal of three and never the authority.
func TestDMRGatewayStatus(t *testing.T) {
	cases := []struct {
		message string
		network string
		login   Tri
		ok      bool
	}{
		{"Logged into DMR Network: BM_3102", "BM_3102", TriYes, true},
		{"Failed login into DMR Network: BM_3102", "BM_3102", TriNo, true},
		{"Failed connection into DMR Network: TGIF", "TGIF", TriNo, true},
		{"Connection closing into DMR Network: TGIF", "TGIF", TriNo, true},
		// An open in progress voids any previous success without being a failure.
		{"Opening DMR Network: BM_3102", "BM_3102", TriUnknown, true},
		// Daemon lifecycle messages name no network.
		{"DMRGateway is starting", "", TriUnknown, false},
		{"DMRGateway is stopping", "", TriUnknown, false},
		// Anything unrecognised is no news, not bad news.
		{"", "", TriUnknown, false},
		{"Some future message nobody has written yet", "", TriUnknown, false},
		{"Logged into DMR Network:", "", TriUnknown, false}, // named nothing
		// Network names with spaces survive intact.
		{"Logged into DMR Network: My Local Master", "My Local Master", TriYes, true},
	}
	for _, c := range cases {
		t.Run(c.message, func(t *testing.T) {
			network, login, ok := DMRGatewayStatus(c.message)
			if network != c.network || login != c.login || ok != c.ok {
				t.Errorf("got (%q, %v, %v), want (%q, %v, %v)", network, login, ok, c.network, c.login, c.ok)
			}
		})
	}
}

// The prefixes must not match a message that merely contains them, so a network
// whose name embeds one cannot be mistaken for a different verdict.
func TestDMRGatewayStatusNeedsThePrefix(t *testing.T) {
	if _, _, ok := DMRGatewayStatus("Something happened. Logged into DMR Network: BM"); ok {
		t.Error("matched a prefix in the middle of a message")
	}
}
