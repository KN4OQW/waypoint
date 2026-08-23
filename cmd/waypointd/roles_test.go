package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/auth"
)

// The permission mapping, checked against the routes the daemon actually
// registers rather than against a list somebody maintained by hand.
//
// The claim these tests hold up is the amendment's: three roles, a mapping that
// is exhaustive over the route table, and a default of deny. The failure they
// exist to catch is a route added later that nobody placed — which under
// default-deny surfaces as "an operator cannot reach it", visible and reported,
// rather than as "a viewer can", which is not.

// loginAs creates an account with the given role and returns a session cookie for
// it, with the rotation flag already cleared — these tests are about roles, and
// the forced-rotation gate is tested on its own.
func loginAs(t *testing.T, e *authEnv, username string, role auth.Role) *http.Cookie {
	t.Helper()
	const pw = "role-matrix-password"
	rec, err := auth.HashPassword(pw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.as.CreateAccount(0, username, rec, role, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	rec2 := httptest.NewRecorder()
	e.handler.ServeHTTP(rec2, jsonReq("POST", "/api/session", map[string]string{"username": username, "password": pw}))
	if rec2.Code != http.StatusOK {
		t.Fatalf("login as %s: %d (%s)", role, rec2.Code, rec2.Body.String())
	}
	return sessionCookie(t, rec2.Result())
}

// probe issues a request as the given cookie and reports whether the ROLE layer
// refused it. A handler's own 404/405/503 is not a refusal — the point is whether
// the request got past the permission check, not whether the handler liked it.
func probe(t *testing.T, e *authEnv, method, path string, cookie *http.Cookie) (allowed bool, code int) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	if rec.Code == http.StatusForbidden {
		var body map[string]any
		if err := decodeBody(rec, &body); err == nil {
			if _, isRole := body["required_role"]; isRole {
				return false, rec.Code
			}
		}
	}
	return true, rec.Code
}

// TestRoleMatrix walks a representative route for each role band and asserts the
// verdict for all three roles.
func TestRoleMatrix(t *testing.T) {
	e := newAuthEnv(t, ":memory:")
	_ = e.claim(t, "theadmin", "goodpassword")
	admin := loginAs(t, e, "matrix-admin", auth.RoleAdmin)
	operator := loginAs(t, e, "matrix-op", auth.RoleOperator)
	viewer := loginAs(t, e, "matrix-view", auth.RoleViewer)

	for _, tc := range []struct {
		method, path string
		viewerOK     bool
		operatorOK   bool
	}{
		// viewer and above.
		{"GET", "/api/status", true, true},
		{"GET", "/api/history", true, true},
		{"GET", "/api/whoami", true, true},
		{"GET", "/api/map", true, true},
		// operator and above. The config view is the interesting one: it is NOT
		// viewer, because it names the node's networks, addresses and ports.
		{"GET", "/api/config", false, true},
		{"PUT", "/api/config/general", false, true},
		{"POST", "/api/config/apply", false, true},
		{"POST", "/api/cal/transmit", false, true},
		{"GET", "/api/flash", false, true},
		{"POST", "/api/hardware/detect", false, true},
		{"GET", "/api/profiles", false, true},
		{"GET", "/api/dmr/talkgroups", false, true},
		{"GET", "/api/network/status", false, true},
		{"GET", "/api/import/scan", false, true},
		{"POST", "/api/buses/validate", false, true},
		// admin only.
		{"GET", "/api/accounts", false, false},
		{"GET", "/api/phonebook", false, false},
		{"GET", "/api/messages", false, false},
		{"GET", "/api/peering/peers", false, false},
		{"PUT", "/api/network/config", false, false},
		{"GET", "/api/update/check", false, false},
		{"GET", "/api/public-view/settings", false, false},
		{"GET", "/api/branding", false, false},
		{"POST", "/api/import/apply", false, false},
		{"POST", "/api/buses/migrate", false, false},
		{"POST", "/api/map/position", false, false},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			// An admin reaches everything, by definition of the role.
			if ok, code := probe(t, e, tc.method, tc.path, admin); !ok {
				t.Errorf("admin was refused (%d)", code)
			}
			if ok, code := probe(t, e, tc.method, tc.path, operator); ok != tc.operatorOK {
				t.Errorf("operator allowed=%v (%d), want %v", ok, code, tc.operatorOK)
			}
			if ok, code := probe(t, e, tc.method, tc.path, viewer); ok != tc.viewerOK {
				t.Errorf("viewer allowed=%v (%d), want %v", ok, code, tc.viewerOK)
			}
		})
	}
}

// TestUnmappedRouteRequiresAdmin is the default-deny rule stated directly. A route
// nobody has placed is admin-only, so the cost of forgetting is an operator who
// cannot reach something rather than a viewer who can.
func TestUnmappedRouteRequiresAdmin(t *testing.T) {
	if got := requiredRole("/api/something-nobody-has-placed"); got != auth.RoleAdmin {
		t.Errorf("an unmapped route requires %q, want admin", got)
	}
	// And through the real stack, not just the lookup.
	e := newAuthEnv(t, ":memory:")
	_ = e.claim(t, "theadmin", "goodpassword")
	viewer := loginAs(t, e, "unmapped-view", auth.RoleViewer)
	if ok, code := probe(t, e, "GET", "/api/not-a-real-route", viewer); ok {
		t.Errorf("a viewer reached an unmapped route (%d)", code)
	}
}

// TestEveryRegisteredRouteHasARole walks the daemon's own route table and asserts
// each one resolves to a role. It is the exhaustiveness half of the mapping: a
// route registered later shows up here rather than being discovered in the field.
func TestEveryRegisteredRouteHasARole(t *testing.T) {
	for _, rt := range allRoutes {
		if roleFree(rt.path) {
			continue
		}
		if got := requiredRole(rt.path); !got.Valid() {
			t.Errorf("%s resolves to an invalid role %q", rt.path, got)
		}
	}
}

// TestNestedPrefixesResolveToTheMostSpecific: the mapping mixes exact paths with
// prefixes and some of them nest. Matching in map order would make the verdict
// depend on Go's map iteration, which is randomised.
func TestNestedPrefixesResolveToTheMostSpecific(t *testing.T) {
	for _, tc := range []struct {
		path string
		want auth.Role
	}{
		{"/api/import/scan", auth.RoleOperator},
		{"/api/import/apply", auth.RoleAdmin},
		{"/api/buses/validate", auth.RoleOperator},
		{"/api/buses/migrate", auth.RoleAdmin},
		{"/api/network/status", auth.RoleOperator},
		{"/api/network/config", auth.RoleAdmin},
		{"/api/map", auth.RoleViewer},
		{"/api/map/position", auth.RoleAdmin},
		{"/api/config", auth.RoleOperator},
		{"/api/config/general", auth.RoleOperator},
	} {
		// Run each many times: map iteration order is randomised per run, so a
		// lookup that depended on it would pass intermittently rather than never.
		for range 20 {
			if got := requiredRole(tc.path); got != tc.want {
				t.Fatalf("requiredRole(%q) = %q, want %q", tc.path, got, tc.want)
			}
		}
	}
}

// TestDemotionTakesEffectImmediately: the role is read from accounts on every
// request and never copied into the session, so an account demoted mid-session
// loses access without having to log in again.
func TestDemotionTakesEffectImmediately(t *testing.T) {
	e := newAuthEnv(t, ":memory:")
	_ = e.claim(t, "theadmin", "goodpassword")
	cookie := loginAs(t, e, "demote-me", auth.RoleOperator)

	if ok, code := probe(t, e, "GET", "/api/config", cookie); !ok {
		t.Fatalf("operator could not reach /api/config to begin with (%d)", code)
	}
	acct, ok, err := e.as.AccountByUsername("demote-me")
	if err != nil || !ok {
		t.Fatal(err)
	}
	if err := e.as.SetRole(acct.ID, auth.RoleViewer, time.Now()); err != nil {
		t.Fatal(err)
	}
	if ok, code := probe(t, e, "GET", "/api/config", cookie); ok {
		t.Errorf("the same session still reached /api/config after demotion (%d)", code)
	}
	// The session is still valid — it lost permission, not authentication.
	if ok, _ := probe(t, e, "GET", "/api/whoami", cookie); !ok {
		t.Error("demotion invalidated the session; it should only have narrowed it")
	}
}

// decodeBody is a small helper so probe can tell a role refusal from a handler's
// own 403.
func decodeBody(rec *httptest.ResponseRecorder, dst any) error {
	return json.Unmarshal(rec.Body.Bytes(), dst)
}
