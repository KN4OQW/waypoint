package main

import (
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"

	"github.com/KN4OQW/waypoint/internal/publicview"
)

// The embeddable widget (D5).
//
// This exists because the thing it replaces is worse. Clubs currently put their
// node's activity on their own website by scraping Pi-Star's last_heard.php —
// fetching an HTML page meant for a browser, parsing it with a regex, and
// re-rendering it. That breaks on every upstream cosmetic change, and it means the
// club's site is hitting a page far heavier than the data it wants. A small
// purpose-built widget and a documented JSON API make the scrape unnecessary.
//
// It is a separate document from the public page rather than a mode of it, and the
// separation is load-bearing: this one is designed to be framed by strangers, so
// it gets frame-ancestors *, while the public page keeps frame-ancestors 'self'.
// Those two policies must never be served by the same handler, because the way
// that mistake presents is a public page silently becoming embeddable anywhere.

// embedMaxLimit bounds the row count a caller can ask for. A widget on a club
// homepage wants five to twenty rows; the ceiling is what stops an embed from
// becoming a way to pull the whole retention window on every page view.
const (
	embedDefaultLimit = 10
	embedMaxLimit     = 50
)

// registerEmbedRoutes mounts the widget inside the public-view gate.
func (s *server) registerEmbedRoutes(mux *http.ServeMux, limiter *publicview.RateLimiter) {
	mux.Handle("/embed/lastheard", s.publicGate(publicCORS(limiter.Middleware(
		embedCSP(http.HandlerFunc(s.embedLastHeard))))))
}

// embedCSP is the widget's policy, and it differs from the public page's in
// exactly one respect that matters.
//
// frame-ancestors * is the point of the endpoint: a club embeds this in a site on
// another origin, and any narrower value would block the only use it has. That is
// safe here for reasons that do not generalise — the document is anonymous,
// read-only, carries no session, has no forms, and runs no script at all. There is
// nothing for a hostile embedder to steal by framing it and nothing for a
// clickjacker to trick a visitor into clicking.
//
// script-src 'none' is what buys that. The widget is server-rendered HTML with no
// JavaScript whatsoever: a page that cannot execute cannot be turned against the
// person who framed it, whatever else is true about the frame it is in.
func embedCSP(next http.Handler) http.Handler {
	const policy = "default-src 'none'; " +
		"style-src 'unsafe-inline'; " +
		"img-src 'self'; " +
		"script-src 'none'; " +
		"object-src 'none'; " +
		"base-uri 'none'; " +
		"form-action 'none'; " +
		"frame-ancestors *"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", policy)
		w.Header().Set("Referrer-Policy", "no-referrer")
		// X-Frame-Options has no "any origin" value and would be read as DENY or
		// SAMEORIGIN by anything that honours it, which is the opposite of what
		// this endpoint is for. frame-ancestors is the modern control and every
		// browser that matters prefers it; setting the legacy header here would
		// only break the feature in the clients that still read it.
		w.Header().Del("X-Frame-Options")
		next.ServeHTTP(w, r)
	})
}

// embedLastHeard serves the widget: a small, self-contained, script-free document.
//
// Server-rendered rather than a JS fetch, deliberately. A widget that fetched its
// own data would need script, which would need a CSP that permits script, in a
// document that runs inside somebody else's page. Rendering on the node means the
// embedded document is inert HTML — and it means the widget works in a feed
// reader, an email client, or anything else that will not run JavaScript.
func (s *server) embedLastHeard(w http.ResponseWriter, r *http.Request) {
	if !publicGET(w, r) {
		return
	}
	set, err := s.publicStore.Settings()
	if err != nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	// The widget shows the last-heard list, so it follows that module's toggle.
	// An operator who turned the list off has not agreed to publish it through a
	// different door.
	if !set.ShowLastHeard {
		http.NotFound(w, r)
		return
	}

	limit := embedDefaultLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			http.Error(w, "invalid limit", http.StatusBadRequest)
			return
		}
		limit = min(n, embedMaxLimit)
	}
	// ?status=1 adds the one-line status. Off by default: an embedder asked for a
	// last-heard widget, and a module they did not ask for should not appear in
	// their layout.
	wantStatus := r.URL.Query().Get("status") == "1"

	res, err := s.publicSvc.LastHeard(limit)
	if err != nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	var st publicview.Status
	if wantStatus && set.ShowStatus {
		if got, err := s.publicSvc.Status(); err == nil {
			st = got
		}
	}

	var b strings.Builder
	b.WriteString(embedHead)
	if wantStatus && set.ShowStatus && st.State != "" {
		b.WriteString(embedStatusLine(st))
	}
	switch {
	case !res.Available:
		// The station database is missing or corrupt, so the node withheld the
		// list. The widget says so rather than rendering an empty table that a
		// club's visitors would read as "nobody has used this repeater".
		fmt.Fprintf(&b, `<p class="n">%s</p>`, html.EscapeString(res.Notice))
	case len(res.Entries) == 0:
		// "on RF" is not padding. The status line counts network traffic as
		// activity — the machine is busy playing a talkgroup out — while this list
		// is only stations this node heard over the air. Without the qualifier the
		// widget can read "last activity just now" directly above "nothing heard",
		// which looks like a contradiction and is actually two true statements
		// about different things.
		b.WriteString(`<p class="n">No stations heard on RF in the retention window.</p>`)
	default:
		b.WriteString(`<table><tbody>`)
		for _, h := range res.Entries {
			fmt.Fprintf(&b, `<tr><td class="c">%s</td><td class="m">%s</td><td class="w">%s</td></tr>`,
				html.EscapeString(h.Callsign), html.EscapeString(h.Mode),
				html.EscapeString(publicview.RelativeTime(h.At, s.publicSvc.Now())))
		}
		b.WriteString(`</tbody></table>`)
	}
	b.WriteString(embedFoot)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// A short cache is right for an embed: a club homepage may be hit far harder
	// than the node itself, and thirty seconds of staleness in a last-heard list
	// is invisible while the traffic reduction is not.
	w.Header().Set("Cache-Control", "public, max-age=30")
	_, _ = w.Write([]byte(b.String()))
}

// embedStatusLine renders the optional status row.
func embedStatusLine(st publicview.Status) string {
	if st.State == publicview.StateTransmitting {
		return `<p class="s"><span class="d tx"></span>Transmitting</p>`
	}
	if st.LastActivityMinutes != nil {
		return `<p class="s"><span class="d"></span>Idle · last activity ` +
			html.EscapeString(publicview.RelativeMinutes(*st.LastActivityMinutes)) + `</p>`
	}
	return `<p class="s"><span class="d"></span>Idle</p>`
}

// The widget's chrome. Styles are inline in a <style> block rather than a linked
// stylesheet so the document is a single request with no dependencies — an embed
// that needed a second fetch would be a second thing to fail inside someone else's
// page, and img-src/style-src are scoped accordingly.
//
// The colours are transparent-background-friendly: the widget inherits nothing
// from its host page (it is a frame, not an include), so it carries a dark card
// that reads on any background rather than assuming one.
const embedHead = `<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Last heard</title>
<style>
:root{--a:#35d07f;--bg:#0f1218;--l:#1c222c;--i:#c7cfdb;--m:#8b95a4;--f:#8a929e;
--mono:"IBM Plex Mono",ui-monospace,Consolas,Menlo,monospace;
--sans:"Space Grotesk",system-ui,-apple-system,"Segoe UI",Arial,sans-serif}
*{box-sizing:border-box}
html,body{margin:0;padding:0}
body{background:var(--bg);color:var(--i);font-family:var(--sans);font-size:13px;
border:1px solid var(--l);border-radius:9px;padding:12px 14px;min-height:100%}
table{width:100%;border-collapse:collapse}
td{padding:6px 4px;border-bottom:1px solid #171b23;vertical-align:baseline}
tr:last-child td{border-bottom:0}
.c{font-family:var(--mono);font-weight:600;color:var(--a);letter-spacing:.5px;white-space:nowrap}
.m{color:var(--i);font-size:12px}
.w{font-family:var(--mono);font-size:11px;color:var(--m);text-align:right;white-space:nowrap}
.n{margin:6px 0;color:var(--f);font-size:12px;font-style:italic}
.s{margin:0 0 10px;font-family:var(--mono);font-size:10.5px;letter-spacing:1px;color:var(--m);
display:flex;align-items:center;gap:7px}
.d{width:7px;height:7px;border-radius:50%;background:var(--a);flex:none}
.d.tx{background:#ff9f45}
.f{margin:10px 0 0;font-family:var(--mono);font-size:9px;letter-spacing:1px;color:var(--f)}
.f a{color:var(--f)}
@media (prefers-color-scheme: light){
body{background:#fff;color:#1c222c;border-color:#dfe3e9}
.m{color:#1c222c}.w,.s{color:#5a6473}.n,.f{color:#6b7482}
}
</style></head><body>
`

const embedFoot = `<p class="f">Last heard · served by Waypoint</p>
</body></html>
`
