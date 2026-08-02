package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/KN4OQW/waypoint/internal/publicview"
)

// Branding (D4): the operator's identity block on the public page.
//
// Three blocks, three trust levels, three mechanisms — and the mechanisms are not
// interchangeable:
//
//   - The logo is re-encoded by this node from decoded pixels, so the file served
//     shares no bytes with the file uploaded.
//   - The narrative is Markdown, rendered server-side and sanitised at render.
//   - The custom HTML is served verbatim into a sandboxed iframe on its own
//     endpoint, and never into the parent document.
//
// The last one is the interesting one. It is the only place in Waypoint where
// operator-authored markup executes, and it does so with no access to this origin:
// the sandbox grants scripts but withholds same-origin, so the frame gets a unique
// opaque origin. It cannot read the parent's DOM, cannot read cookies or storage,
// and cannot call the API as the operator. That is what lets the feature exist at
// all — "you own what you paste here" is only an acceptable warning when the blast
// radius is the iframe.

// brandingDir returns the directory the logo is written under: a sibling of the
// configuration store, so branding travels with the node's data rather than with
// the binary.
func (s *server) brandingDir() string {
	if s.storePath == "" || s.storePath == ":memory:" {
		return ""
	}
	return filepath.Dir(s.storePath)
}

// registerBrandingRoutes mounts the authenticated write endpoints. They sit behind
// the session wall like every other admin route — nothing here is anonymous, and
// the gate's default-deny is what makes that true without a per-handler check.
func (s *server) registerBrandingRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/branding", s.brandingView)
	mux.HandleFunc("/api/branding/logo", s.brandingLogo)
	mux.HandleFunc("/api/branding/narrative", s.brandingNarrative)
	mux.HandleFunc("/api/branding/custom-html", s.brandingCustomHTML)
}

// brandingView returns the stored branding for the settings panel.
//
// It also returns the RENDERED narrative, so the panel's preview is the same
// sanitised HTML the public page will show rather than a second rendering path
// that could disagree with it. A preview that renders differently from production
// is worse than no preview: it invites an operator to sign off on output nobody
// will ever serve.
func (s *server) brandingView(w http.ResponseWriter, r *http.Request) {
	if s.publicStore == nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	b, err := s.publicStore.Branding()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rendered, err := publicview.RenderNarrative(b.NarrativeMarkdown)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		LogoPath          string `json:"logo_path"`
		NarrativeMarkdown string `json:"narrative_markdown"`
		NarrativeHTML     string `json:"narrative_html"`
		CustomHTML        string `json:"custom_html"`
	}{b.LogoPath, b.NarrativeMarkdown, rendered, b.CustomHTML})
}

// brandingLogo accepts a PNG or JPEG and stores a re-encoded copy.
//
// The body is the image itself rather than a multipart form: there is exactly one
// field, and multipart would add a parser between an authenticated upload and the
// decoder for no benefit. DELETE clears the logo.
func (s *server) brandingLogo(w http.ResponseWriter, r *http.Request) {
	if s.publicStore == nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodDelete:
		if err := s.publicStore.SetLogoPath(""); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if dir := s.brandingDir(); dir != "" {
			// Best effort: the store is the source of truth for whether a logo
			// exists, so a file left behind is untidy rather than serving.
			_ = os.Remove(filepath.Join(dir, filepath.FromSlash(publicview.LogoRelPath)))
		}
		w.WriteHeader(http.StatusNoContent)
		return
	case http.MethodPost, http.MethodPut:
	default:
		w.Header().Set("Allow", "POST, PUT, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	dir := s.brandingDir()
	if dir == "" {
		http.Error(w, "this node has no writable data directory for branding", http.StatusServiceUnavailable)
		return
	}
	// MaxBytesReader caps what the process will read even if the client lies about
	// Content-Length; StoreLogo caps it again from the reader's side.
	body := http.MaxBytesReader(w, r.Body, publicview.LogoMaxBytes+1)
	rel, err := publicview.StoreLogo(dir, body)
	switch {
	case errors.Is(err, publicview.ErrLogoTooLarge),
		errors.Is(err, publicview.ErrLogoFormat),
		errors.Is(err, publicview.ErrLogoDimension):
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	case err != nil:
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.publicStore.SetLogoPath(rel); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"logo_path": rel})
}

// brandingNarrative stores the operator's Markdown verbatim.
//
// Verbatim is deliberate: sanitising on the way in would corrupt the source the
// operator edits next, and the render path sanitises its own output anyway. What
// is stored is what they typed; what is served is what the sanitiser allows.
func (s *server) brandingNarrative(w http.ResponseWriter, r *http.Request) {
	s.brandingText(w, r, func(b *publicview.Branding, v string) { b.NarrativeMarkdown = v },
		func(b publicview.Branding) string { return b.NarrativeMarkdown })
}

// brandingCustomHTML stores raw HTML verbatim.
//
// Nothing is sanitised, and that is the feature rather than an oversight. Its
// contract is to be served verbatim into a sandboxed iframe that cannot reach this
// origin; sanitising it would make the sandbox pointless and the block useless at
// the same time.
func (s *server) brandingCustomHTML(w http.ResponseWriter, r *http.Request) {
	s.brandingText(w, r, func(b *publicview.Branding, v string) { b.CustomHTML = v },
		func(b publicview.Branding) string { return b.CustomHTML })
}

// brandingTextMax bounds a single text block. Generous for prose, small enough
// that the store stays a configuration database rather than a CMS.
const brandingTextMax = 64 << 10

func (s *server) brandingText(w http.ResponseWriter, r *http.Request,
	set func(*publicview.Branding, string), get func(publicview.Branding) string,
) {
	if s.publicStore == nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	b, err := s.publicStore.Branding()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"value": get(b)})
		return
	case http.MethodPut, http.MethodPost:
	default:
		w.Header().Set("Allow", "GET, PUT")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var body struct {
		Value string `json:"value"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, brandingTextMax))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		http.Error(w, "invalid body: want {\"value\": \"...\"}", http.StatusBadRequest)
		return
	}
	set(&b, body.Value)
	if err := s.publicStore.SaveBranding(b); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Public serving
// ---------------------------------------------------------------------------

// publicLogo serves the re-encoded logo.
//
// The Content-Type is a constant, not sniffed and not derived from the path,
// because StoreLogo guarantees the file is a PNG — it wrote it. Paired with
// nosniff, that means a browser has no path to interpreting this response as
// anything but an image, whatever the bytes turn out to be.
func (s *server) publicLogo(w http.ResponseWriter, r *http.Request) {
	if !publicGET(w, r) {
		return
	}
	b, err := s.publicStore.Branding()
	if err != nil || b.LogoPath == "" {
		http.NotFound(w, r)
		return
	}
	dir := s.brandingDir()
	if dir == "" {
		http.NotFound(w, r)
		return
	}
	// The stored path is written by StoreLogo and is always LogoRelPath; serving
	// the constant rather than the stored string means a store row that somehow
	// held "../../etc/shadow" could not become a file read.
	raw, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(publicview.LogoRelPath)))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write(raw)
}

// publicCustomBlock serves the operator's raw HTML as a standalone document.
//
// This endpoint exists to be framed and for no other purpose. The page embeds it
// as <iframe sandbox="allow-scripts" src="/public/custom-block">, with
// allow-same-origin deliberately withheld, so the document runs in a unique opaque
// origin: no access to the parent DOM, no cookies, no storage, no authenticated
// API calls.
//
// Its own CSP is relaxed rather than strict, which reads backwards until you see
// what it is protecting. The isolation here comes from the sandbox attribute the
// PARENT sets — a policy this document served could not weaken and cannot be
// trusted to enforce. Making the CSP strict would only break the operator's block
// while adding nothing, since the sandbox has already removed everything worth
// reaching. What it does keep is frame-ancestors 'self': this document may be
// framed by the node's own page and by nothing else, so another site cannot embed
// an operator's custom block and pass it off as its own.
func (s *server) publicCustomBlock(w http.ResponseWriter, r *http.Request) {
	if !publicGET(w, r) {
		return
	}
	set, err := s.publicStore.Settings()
	if err != nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	b, err := s.publicStore.Branding()
	if err != nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	// Empty custom HTML means the module does not exist, not that it is blank.
	if strings.TrimSpace(b.CustomHTML) == "" || !set.Enabled {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "frame-ancestors 'self'")
	// Referrer-Policy still applies: the frame may fetch nothing useful, but it
	// should not leak this node's URL to wherever it tries.
	w.Header().Set("Referrer-Policy", "no-referrer")

	// A minimal document wrapper so the operator's fragment is a valid page. Their
	// content is written verbatim — that is the contract — and the isolation is the
	// sandbox, not this wrapper.
	_, _ = w.Write([]byte("<!doctype html>\n<html><head><meta charset=\"utf-8\">" +
		"<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">" +
		"<style>html,body{margin:0;padding:0;background:transparent;color:#c7cfdb;" +
		"font-family:system-ui,-apple-system,\"Segoe UI\",Arial,sans-serif}</style>" +
		"</head><body>\n"))
	_, _ = w.Write([]byte(b.CustomHTML))
	_, _ = w.Write([]byte("\n</body></html>\n"))
}
