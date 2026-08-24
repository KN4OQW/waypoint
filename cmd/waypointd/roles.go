package main

import (
	"net/http"
	"sort"
	"strings"

	"github.com/KN4OQW/waypoint/internal/auth"
)

// Role enforcement (RFC-0002 Amendment 1).
//
// The gate in internal/auth answers "is there a valid session". This answers "may
// THIS account reach THIS route", and it is a separate question with a separate
// table because the two fail differently: the gate's answer is 401 and sends you
// to the login screen, and this one's is 403 and sending you to a login screen
// would be a lie — you are logged in, and this is not yours.
//
// # Default deny
//
// routePolicy is exhaustive over the route table as it stands, and anything not
// in it requires admin. That is the amendment's rule and it is the same posture
// RFC-0002's route matrix already takes: a newly registered route is denied until
// somebody deliberately places it. The failure mode of getting this wrong is
// therefore a route an operator cannot reach, which is visible and reported,
// rather than one a viewer can, which is not.
//
// # Longest prefix, not first match
//
// The table mixes exact paths and prefixes, and some prefixes nest —
// /api/config is operator, /api/import/scan is operator while /api/import/apply
// is admin. Matching in map order would make the outcome depend on iteration
// order, so lookup takes the LONGEST matching key. The test walks every
// registered route and asserts the verdict, which is what stops a nested pair
// from silently resolving the wrong way.

// routePolicy maps a route to the minimum role that may reach it.
//
// Keys ending in "/" are prefixes; everything else is an exact path. Transcribed
// from the amendment's permission mapping — if the two ever disagree, the
// amendment is right and this is a bug.
var routePolicy = map[string]auth.Role{
	// --- viewer and above: read-only live activity -------------------------
	"/api/events":  auth.RoleViewer,
	"/api/history": auth.RoleViewer,
	"/api/status":  auth.RoleViewer,
	"/api/ws":      auth.RoleViewer,
	"/api/whoami":  auth.RoleViewer,
	"/api/map":     auth.RoleViewer,
	// Changing your OWN password, which every account must be able to do whatever
	// its role. The amendment's mapping table does not list it, but its prose
	// fixes it: forced rotation refuses "every route except the password-change
	// route" for an account of ANY role, so leaving this to the default-deny would
	// make a viewer or operator created with must_rotate=1 unable to rotate and
	// therefore unable to use the node at all. Placed here deliberately, which is
	// what the default-deny rule asks for.
	"/api/password": auth.RoleViewer,

	// --- operator and above: what the radio does ---------------------------
	// The redacted config view is NOT viewer: it names the node's networks,
	// addresses and ports, which a read-only account has no need for. The callsign
	// chip that used to justify viewer access is served by /api/whoami.
	"/api/config":    auth.RoleOperator,
	"/api/config/":   auth.RoleOperator,
	"/api/overrides": auth.RoleOperator,
	"/api/profiles":  auth.RoleOperator,
	"/api/profiles/": auth.RoleOperator,
	// Calibration transmits; flashing rewrites firmware; hardware detect takes the
	// modem off the air. All three change what goes over the air.
	"/api/cal":       auth.RoleOperator,
	"/api/cal/":      auth.RoleOperator,
	"/api/flash":     auth.RoleOperator,
	"/api/flash/":    auth.RoleOperator,
	"/api/hardware":  auth.RoleOperator,
	"/api/hardware/": auth.RoleOperator,
	"/api/lcd/":      auth.RoleOperator,
	// Dry runs and previews only; the committing halves are admin below.
	"/api/buses/validate": auth.RoleOperator,
	"/api/import/scan":    auth.RoleOperator,
	// Reference data an operator needs to fill the config in.
	"/api/hostlists":         auth.RoleOperator,
	"/api/hostlists/refresh": auth.RoleOperator,
	"/api/dmr/talkgroups":    auth.RoleOperator,
	"/api/dmr/ids":           auth.RoleOperator,
	"/api/dmr/masters":       auth.RoleOperator,
	"/api/ysf/reflectors":    auth.RoleOperator,
	"/api/p25/reflectors":    auth.RoleOperator,
	"/api/nxdn/reflectors":   auth.RoleOperator,
	"/api/dstar/reflectors":  auth.RoleOperator,
	"/api/m17/reflectors":    auth.RoleOperator,
	// Reading and scanning the network, not changing it.
	"/api/network/status":    auth.RoleOperator,
	"/api/network/wifi/scan": auth.RoleOperator,
	"/api/network/timezones": auth.RoleOperator,
	// The county picker behind the Weather panel. Operator for the same reason
	// the reflector and talkgroup pickers are: it reads a list that ships in the
	// binary so a config panel an operator may edit can be filled in. It changes
	// nothing and reaches no network. /api/wx/status and /api/wx/test are NOT
	// here -- one reports on a running feed and the other keys a transmitter, so
	// they take the default-deny to admin.
	"/api/wx/counties": auth.RoleOperator,

	// --- admin only --------------------------------------------------------
	// Everything below changes who may reach the node, or what the node is to the
	// outside world. Listed for the reader; the default would catch them anyway.
	"/api/accounts":           auth.RoleAdmin,
	"/api/accounts/":          auth.RoleAdmin,
	"/api/phonebook":          auth.RoleAdmin,
	"/api/phonebook/":         auth.RoleAdmin,
	"/api/messages":           auth.RoleAdmin,
	"/api/messages/":          auth.RoleAdmin,
	"/api/peering/":           auth.RoleAdmin,
	"/api/network/config":     auth.RoleAdmin,
	"/api/network/apply":      auth.RoleAdmin,
	"/api/network/confirm":    auth.RoleAdmin,
	"/api/network/host/apply": auth.RoleAdmin,
	"/api/update/":            auth.RoleAdmin,
	"/api/public-view/":       auth.RoleAdmin,
	"/api/branding":           auth.RoleAdmin,
	"/api/branding/":          auth.RoleAdmin,
	"/api/map/position":       auth.RoleAdmin,
	"/api/import/apply":       auth.RoleAdmin,
	"/api/buses/migrate":      auth.RoleAdmin,
	// Notifications landed after the amendment was written, so its mapping table
	// does not name them. Placed rather than left to the default: the panel holds
	// an SMTP account's credentials and a test send puts the node's identity in
	// somebody's inbox, which is "what the node is to the outside world" — the
	// line the amendment draws for admin.
	"/api/notify/": auth.RoleAdmin,
}

// rank orders the roles so "at least this role" is a comparison. It is not a
// permission lattice — three values in a line is all the amendment allows.
func rank(r auth.Role) int {
	switch r {
	case auth.RoleViewer:
		return 1
	case auth.RoleOperator:
		return 2
	case auth.RoleAdmin:
		return 3
	default:
		return 0 // unknown role: below everything, so it satisfies nothing
	}
}

// policyKeys is routePolicy's keys, longest first, so lookup can take the most
// specific match without sorting on every request.
var policyKeys = func() []string {
	keys := make([]string, 0, len(routePolicy))
	for k := range routePolicy {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	return keys
}()

// requiredRole returns the minimum role for a path. Anything unmapped is admin.
func requiredRole(path string) auth.Role {
	for _, k := range policyKeys {
		if strings.HasSuffix(k, "/") {
			if strings.HasPrefix(path, k) {
				return routePolicy[k]
			}
			continue
		}
		if path == k {
			return routePolicy[k]
		}
	}
	return auth.RoleAdmin
}

// roleFreePaths are served without a role check because the session gate has
// already decided them, or because they are anonymous by design.
//
// They are listed rather than inferred: an account-bearing request reaching one of
// these still gets whatever the handler does, and the point of naming them is that
// adding a route here is a visible act.
func roleFree(path string) bool {
	switch path {
	case "/api/health", "/api/claim", "/api/session":
		return true
	}
	// The opt-in public surface is anonymous by design and gated on the operator's
	// toggle rather than on a role (D2). It is reached by callers with no account
	// at all, so a role check would be meaningless.
	return IsPublicRoute(path)
}

// enforceRoles wraps the mux with the permission mapping.
//
// It runs INSIDE the session gate — the gate has already established that there
// is a valid session by the time anything here is asked — so an absent account
// here means the session's owner was deleted between the two, which is a 401
// rather than a 403.
func (s *server) enforceRoles(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") || roleFree(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		acct, ok := s.auth.Caller(r)
		if !ok {
			// No resolvable account behind a request the gate let through: the
			// account was deleted while the session was in flight. Revoking a login
			// takes effect immediately, and this is what that looks like from the
			// far end.
			writeJSONStatus(w, http.StatusUnauthorized,
				map[string]any{"error": "authentication required", "mode": "login"})
			return
		}
		// A password an admin chose is a password two people know. Until it is
		// rotated the account reaches the rotation route and nothing else — checked
		// before the role, because it applies to every role equally.
		if acct.MustRotate && r.URL.Path != "/api/password" {
			writeJSONStatus(w, http.StatusForbidden, map[string]any{
				"error": "this account must set a new password before it can be used",
				"mode":  "rotate",
			})
			return
		}
		if want := requiredRole(r.URL.Path); rank(acct.Role) < rank(want) {
			// 403, not 401: the caller is authenticated and sending them to a login
			// screen would be a lie. The role they need is named so the UI can say
			// "ask an admin" rather than "something went wrong".
			writeJSONStatus(w, http.StatusForbidden, map[string]any{
				"error":         "your account does not have permission for this",
				"required_role": string(want),
				"your_role":     string(acct.Role),
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}
