package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The gate is default-deny, and AllowAnonymous is the one seam that lets a route
// group past it. These tests exist because that seam is exactly the kind of thing
// that is safe when written and a hole two refactors later.

func gateFor(t *testing.T, claimed bool) (*Auth, http.Handler) {
	t.Helper()
	clock := time.Unix(1_700_000_000, 0)
	a, st := newTestAuth(t, func() time.Time { return clock })
	if claimed {
		claimIt(t, st, clock)
	}
	served := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("reached the mux"))
	})
	return a, a.Gate(served)
}

func get(h http.Handler, path string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// TestGateStaysDenyByDefault: without AllowAnonymous the behavior is unchanged,
// so the seam costs nothing to a build that does not use it.
func TestGateStaysDenyByDefault(t *testing.T) {
	for _, claimed := range []bool{false, true} {
		_, h := gateFor(t, claimed)
		w := get(h, "/api/public/node")
		if w.Code == http.StatusOK {
			t.Errorf("claimed=%v: an unregistered route reached the mux without a session", claimed)
		}
	}
}

// TestAnonymousRoutesPassTheWall in both claim states. Both halves matter: a
// public page that appeared only after the owner claimed the box would be a
// surprise on first boot, and one that vanished on claim would be a worse one.
func TestAnonymousRoutesPassTheWall(t *testing.T) {
	for _, claimed := range []bool{false, true} {
		a, h := gateFor(t, claimed)
		a.AllowAnonymous(func(r *http.Request) bool {
			return strings.HasPrefix(r.URL.Path, "/api/public/")
		})
		w := get(h, "/api/public/node")
		if w.Code != http.StatusOK {
			t.Errorf("claimed=%v: a public route was blocked by the gate (%d)", claimed, w.Code)
		}
	}
}

// TestAnonymousExemptionIsNarrow is the important one. Registering the exemption
// must not open anything the predicate does not name — the config surface and the
// event stream stay behind the wall in both claim states.
func TestAnonymousExemptionIsNarrow(t *testing.T) {
	for _, claimed := range []bool{false, true} {
		a, h := gateFor(t, claimed)
		a.AllowAnonymous(func(r *http.Request) bool {
			return strings.HasPrefix(r.URL.Path, "/api/public/")
		})
		for _, p := range []string{
			"/api/config", "/api/events", "/api/status", "/api/history",
			"/api/hardware", "/api/peering/peers", "/api/update/check",
			"/api/publicity", // a longer name sharing the prefix's first characters
		} {
			w := get(h, p)
			if w.Code == http.StatusOK {
				t.Errorf("claimed=%v: %s reached the mux without a session", claimed, p)
			}
		}
	}
}

// TestAnonymousNilIsIgnored: passing nil must not panic and must not open
// anything.
func TestAnonymousNilIsIgnored(t *testing.T) {
	a, h := gateFor(t, true)
	a.AllowAnonymous(nil)
	if w := get(h, "/api/public/node"); w.Code == http.StatusOK {
		t.Error("a nil predicate opened a route")
	}
}
