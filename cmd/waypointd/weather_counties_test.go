package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/KN4OQW/waypoint/internal/wxzones"
)

// countiesReply is the handler's response shape, in one place so a change to it
// breaks the tests rather than being absorbed by them.
type countiesReply struct {
	Counties []wxzones.County `json:"counties"`
	Total    int              `json:"total"`
	Limit    int              `json:"limit"`
	Unknown  []string         `json:"unknown"`
	Error    string           `json:"error"`
}

// getCounties drives the handler on a ZERO-VALUE server on purpose.
//
// That is not a shortcut, it is the assertion: the picker must not need the
// weather service, the store, a feed connection or a network, because the state
// an operator configures counties in is the one where none of that exists yet.
// If the handler ever grows a dependency on s, this stops compiling or panics,
// which is the right way to find out.
func getCounties(t *testing.T, query string) (*http.Response, countiesReply) {
	t.Helper()
	s := &server{}
	rec := httptest.NewRecorder()
	s.wxCountiesHandler(rec, httptest.NewRequest(http.MethodGet, "/api/wx/counties"+query, nil))
	res := rec.Result()
	var reply countiesReply
	if err := json.NewDecoder(res.Body).Decode(&reply); err != nil {
		t.Fatalf("decoding %q: %v", query, err)
	}
	return res, reply
}

// The plain case, and the one that closes the gap this endpoint exists for: an
// operator types a name and gets a county with a code attached, instead of
// typing the code.
func TestCountiesSearchReturnsTheCountyWithItsCode(t *testing.T) {
	res, reply := getCounties(t, "?q=santa+rosa")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d", res.StatusCode)
	}
	if len(reply.Counties) == 0 {
		t.Fatal("no counties returned")
	}
	got := reply.Counties[0]
	if got.SAME != "012113" || got.Name != "Santa Rosa" || got.State != "FL" {
		t.Fatalf("first result %+v, want Santa Rosa FL 012113", got)
	}
}

// Every field the store keeps is carried. config.WXCounty holds UGC, name, state
// and WFO beside the code so a chosen county still reads correctly when the
// table no longer has it; a handler that returned only the code would leave the
// panel with nothing to store and that guarantee would quietly stop holding.
func TestCountiesCarryEveryFieldTheStoreKeeps(t *testing.T) {
	_, reply := getCounties(t, "?q=012113")
	if len(reply.Counties) == 0 {
		t.Fatal("no counties returned")
	}
	c := reply.Counties[0]
	if c.SAME == "" || c.UGC == "" || c.Name == "" || c.State == "" || c.WFO == "" {
		t.Fatalf("a field the store keeps came back empty: %+v", c)
	}
}

// The count behind the page. Without it the panel can only say "here are 25",
// which reads as "there are 25" -- and an operator whose county is number 40
// concludes it is missing and goes back to typing codes.
func TestCountiesReportTheTotalBehindThePage(t *testing.T) {
	_, reply := getCounties(t, "?q=fl&limit=5")
	if len(reply.Counties) != 5 {
		t.Fatalf("returned %d counties, want the requested 5", len(reply.Counties))
	}
	if reply.Total <= 5 {
		t.Fatalf("total is %d; Florida alone has 67 counties, so the page is hiding some and not saying so", reply.Total)
	}
}

// The cap is a ceiling on what a caller can ask for, not just a default.
func TestCountiesLimitIsCapped(t *testing.T) {
	_, reply := getCounties(t, "?limit=100000")
	if len(reply.Counties) > wxCountyLimitMax {
		t.Fatalf("returned %d counties, above the %d cap", len(reply.Counties), wxCountyLimitMax)
	}
	if reply.Limit != wxCountyLimitMax {
		t.Fatalf("limit reported as %d, want the cap %d", reply.Limit, wxCountyLimitMax)
	}
}

func TestCountiesRejectANonsenseLimit(t *testing.T) {
	for _, q := range []string{"?limit=0", "?limit=-4", "?limit=lots"} {
		res, reply := getCounties(t, q)
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", q, res.StatusCode)
		}
		if reply.Error == "" {
			t.Errorf("%s: refused without saying what to do", q)
		}
	}
}

// The opening state of the picker: no query, a screenful of counties.
func TestCountiesWithNoQueryReturnADefaultPage(t *testing.T) {
	_, reply := getCounties(t, "")
	if len(reply.Counties) != wxCountyLimitDefault {
		t.Fatalf("returned %d counties, want the default page of %d", len(reply.Counties), wxCountyLimitDefault)
	}
	if reply.Total != wxzones.Count() {
		t.Fatalf("total is %d, want the whole table (%d)", reply.Total, wxzones.Count())
	}
}

// A query that matches nothing says so by returning nothing, rather than falling
// back to the whole table -- which would look to the operator like a match.
func TestCountiesSearchWithNoMatchesIsEmptyNotEverything(t *testing.T) {
	_, reply := getCounties(t, "?q=qqzzxx")
	if len(reply.Counties) != 0 || reply.Total != 0 {
		t.Fatalf("got %d counties (total %d), want none", len(reply.Counties), reply.Total)
	}
}

// same= is the other direction: codes already in the store, resolved to names.
// This is what a configuration imported from a WPSD card or hand-edited before
// the picker existed looks like on first load.
func TestCountiesResolveStoredCodes(t *testing.T) {
	_, reply := getCounties(t, "?same=012113,024510")
	if len(reply.Counties) != 2 {
		t.Fatalf("resolved %d of 2 codes: %+v", len(reply.Counties), reply.Counties)
	}
	if len(reply.Unknown) != 0 {
		t.Fatalf("reported %v as unknown; both are real codes", reply.Unknown)
	}
}

// A code the shipped table does not know is REPORTED, not dropped. Dropping it
// would leave the panel showing fewer counties than the store holds, which is
// the panel lying about what the node is subscribed to.
func TestCountiesReportCodesTheTableCannotName(t *testing.T) {
	_, reply := getCounties(t, "?same=012113,999999")
	if len(reply.Counties) != 1 {
		t.Fatalf("resolved %d counties, want the one real code", len(reply.Counties))
	}
	if len(reply.Unknown) != 1 || reply.Unknown[0] != "999999" {
		t.Fatalf("unknown = %v, want [999999]", reply.Unknown)
	}
}

// An empty counties list must serialize as [] and not null, because the panel
// iterates it. This is the standard Go JSON foot-gun and it is a blank screen
// rather than an error when it bites.
func TestCountiesSerializeAnEmptyListAsAnArray(t *testing.T) {
	s := &server{}
	rec := httptest.NewRecorder()
	s.wxCountiesHandler(rec, httptest.NewRequest(http.MethodGet, "/api/wx/counties?same=999999", nil))
	if body := rec.Body.String(); !contains(body, `"counties":[]`) {
		t.Fatalf("empty counties did not serialize as an array: %s", body)
	}
}

func TestCountiesRefuseAWrite(t *testing.T) {
	s := &server{}
	rec := httptest.NewRecorder()
	s.wxCountiesHandler(rec, httptest.NewRequest(http.MethodPost, "/api/wx/counties", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST returned %d, want 405", rec.Code)
	}
}
