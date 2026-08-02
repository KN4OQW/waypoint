package main

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/KN4OQW/waypoint/internal/publicview"
)

func smallPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 32, 32))
	for y := range 32 {
		for x := range 32 {
			img.Set(x, y, color.RGBA{uint8(x * 8), uint8(y * 8), 0x40, 0xff})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// admin drives an authenticated write endpoint directly. These routes sit behind
// the session wall in the real mux; the gate is tested in internal/auth, and this
// exercises the handlers themselves.
func admin(t *testing.T, s *server, method, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	s.registerBrandingRoutes(mux)
	var r *http.Request
	if body == nil {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, bytes.NewReader(body))
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

// TestBrandingWriteEndpointsAreBehindTheWall is the containment check. These
// endpoints take arbitrary HTML from their caller; if any of them were anonymous,
// the sandboxed custom block would become a defacement channel for strangers.
func TestBrandingWriteEndpointsAreBehindTheWall(t *testing.T) {
	for _, p := range []string{
		"/api/branding", "/api/branding/logo",
		"/api/branding/narrative", "/api/branding/custom-html",
	} {
		if IsPublicRoute(p) {
			t.Errorf("%s is on the anonymous route list — branding writes must require a session", p)
		}
	}
}

func TestLogoUploadRoundTrip(t *testing.T) {
	s, ps := newPublicServer(t, true)

	w := admin(t, s, http.MethodPost, "/api/branding/logo", smallPNG(t))
	if w.Code != http.StatusOK {
		t.Fatalf("logo upload = %d: %s", w.Code, w.Body.String())
	}
	b, err := ps.Branding()
	if err != nil {
		t.Fatal(err)
	}
	if b.LogoPath != publicview.LogoRelPath {
		t.Errorf("stored logo path = %q, want %q", b.LogoPath, publicview.LogoRelPath)
	}

	// And it is served publicly, with a locked content type.
	got := do(t, s, http.MethodGet, "/public/assets/logo", "203.0.113.9:1234")
	if got.Code != http.StatusOK {
		t.Fatalf("/public/assets/logo = %d", got.Code)
	}
	if ct := got.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("logo Content-Type = %q, want a locked image/png", ct)
	}
	if got.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("the logo is served without nosniff, so a browser may sniff it as something else")
	}
	if _, err := png.Decode(bytes.NewReader(got.Body.Bytes())); err != nil {
		t.Errorf("the served logo is not a PNG: %v", err)
	}

	// Deleting clears it and the public route goes back to 404.
	if w := admin(t, s, http.MethodDelete, "/api/branding/logo", nil); w.Code != http.StatusNoContent {
		t.Fatalf("logo delete = %d", w.Code)
	}
	if got := do(t, s, http.MethodGet, "/public/assets/logo", "203.0.113.9:1234"); got.Code != http.StatusNotFound {
		t.Errorf("after deletion, the logo route = %d, want 404", got.Code)
	}
}

func TestLogoUploadRejectsBadInput(t *testing.T) {
	s, _ := newPublicServer(t, true)
	for _, tc := range []struct {
		name string
		body []byte
	}{
		{"svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)},
		{"text", []byte("not an image")},
		{"empty", []byte{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if w := admin(t, s, http.MethodPost, "/api/branding/logo", tc.body); w.Code != http.StatusBadRequest {
				t.Errorf("uploading %s = %d, want 400", tc.name, w.Code)
			}
		})
	}
	// Oversize is refused rather than accepted-and-truncated.
	big := make([]byte, publicview.LogoMaxBytes+1024)
	copy(big, smallPNG(t))
	if w := admin(t, s, http.MethodPost, "/api/branding/logo", big); w.Code != http.StatusBadRequest {
		t.Errorf("oversized upload = %d, want 400", w.Code)
	}
}

func TestNarrativeRoundTripAndRender(t *testing.T) {
	s, _ := newPublicServer(t, true)
	src := "# K4SRC\n\nOpen to all.\n\n<script>alert(1)</script>\n"
	body, _ := json.Marshal(map[string]string{"value": src})

	if w := admin(t, s, http.MethodPut, "/api/branding/narrative", body); w.Code != http.StatusNoContent {
		t.Fatalf("narrative PUT = %d: %s", w.Code, w.Body.String())
	}

	// The panel reads back the source AND the rendered preview, so the preview is
	// the same sanitised HTML production serves rather than a second render path.
	w := admin(t, s, http.MethodGet, "/api/branding", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("/api/branding = %d", w.Code)
	}
	var view struct {
		NarrativeMarkdown string `json:"narrative_markdown"`
		NarrativeHTML     string `json:"narrative_html"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.NarrativeMarkdown != src {
		t.Error("the stored Markdown was rewritten; the operator must get back what they typed")
	}
	if strings.Contains(view.NarrativeHTML, "<script") {
		t.Errorf("the preview HTML carries a script tag: %s", view.NarrativeHTML)
	}
	if !strings.Contains(view.NarrativeHTML, "<h1") {
		t.Errorf("the preview lost ordinary formatting: %s", view.NarrativeHTML)
	}

	// And the same sanitised HTML reaches the public node card.
	pub := do(t, s, http.MethodGet, "/api/public/node", "203.0.113.9:1234")
	if strings.Contains(pub.Body.String(), "<script") {
		t.Errorf("the public node card carries a script tag: %s", pub.Body.String())
	}
	if !strings.Contains(pub.Body.String(), "narrative_html") {
		t.Error("the public node card has no narrative")
	}
}

// ---------------------------------------------------------------------------
// The sandboxed custom block
// ---------------------------------------------------------------------------

func setCustomHTML(t *testing.T, s *server, html string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"value": html})
	if w := admin(t, s, http.MethodPut, "/api/branding/custom-html", body); w.Code != http.StatusNoContent {
		t.Fatalf("custom-html PUT = %d: %s", w.Code, w.Body.String())
	}
}

// TestCustomBlockIsServedVerbatim. Sanitising it would make the sandbox pointless
// and the feature useless at the same time; the isolation is the iframe, not a
// filter.
func TestCustomBlockIsServedVerbatim(t *testing.T) {
	s, _ := newPublicServer(t, true)
	html := `<div id="x"><script>document.body.style.background="#123"</script><p>hello</p></div>`
	setCustomHTML(t, s, html)

	w := do(t, s, http.MethodGet, "/public/custom-block", "203.0.113.9:1234")
	if w.Code != http.StatusOK {
		t.Fatalf("/public/custom-block = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), html) {
		t.Errorf("the custom block was altered on the way out:\n%s", w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("custom block Content-Type = %q", ct)
	}
}

// TestCustomBlockCannotBeFramedElsewhere. The document is deliberately permissive
// inside, so the one thing its own headers must still do is stop another site
// embedding an operator's block and passing it off as theirs.
func TestCustomBlockFrameAncestors(t *testing.T) {
	s, _ := newPublicServer(t, true)
	setCustomHTML(t, s, "<p>hello</p>")
	w := do(t, s, http.MethodGet, "/public/custom-block", "203.0.113.9:1234")
	csp := w.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "frame-ancestors 'self'") {
		t.Errorf("custom block CSP = %q, want frame-ancestors 'self'", csp)
	}
}

// TestPublicPageCanFrameTheCustomBlock: the parent page's CSP has to permit the
// iframe, or the module silently renders blank.
func TestPublicPageAllowsSelfFrames(t *testing.T) {
	s, _ := newPublicServer(t, true)
	csp := do(t, s, http.MethodGet, "/public/", "203.0.113.9:1234").Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "frame-src 'self'") {
		t.Errorf("the page CSP lacks frame-src 'self', so the sandboxed block cannot load: %s", csp)
	}
	// And it stays strict in every other respect.
	if strings.Contains(csp, "frame-src *") || strings.Contains(csp, "frame-src https:") {
		t.Errorf("frame-src is wider than 'self': %s", csp)
	}
}

// TestPageEmbedsCustomBlockSandboxed is the assertion that the iframe is created
// with the right attributes. allow-scripts WITHOUT allow-same-origin is the whole
// isolation: together they would give the frame this origin back, and the sandbox
// would be decoration.
func TestPageEmbedsCustomBlockSandboxed(t *testing.T) {
	s, _ := newPublicServer(t, true)
	js := do(t, s, http.MethodGet, "/public/public.js", "203.0.113.9:1234").Body.String()

	// Match the attribute VALUE rather than grepping the whole file: the source
	// discusses allow-same-origin in a comment explaining why it is withheld, and a
	// naive substring search finds the prose instead of the code.
	m := regexp.MustCompile(`setAttribute\(\s*"sandbox"\s*,\s*"([^"]*)"\s*\)`).FindStringSubmatch(js)
	if m == nil {
		t.Fatal("the page does not set a sandbox attribute on the custom-block iframe")
	}
	sandbox := m[1]
	if !strings.Contains(sandbox, "allow-scripts") {
		t.Errorf("sandbox = %q, want allow-scripts so the operator's block actually runs", sandbox)
	}
	if strings.Contains(sandbox, "allow-same-origin") {
		t.Errorf("sandbox = %q — allow-scripts together with allow-same-origin hands the "+
			"frame this origin back and makes the sandbox decoration", sandbox)
	}
	if !strings.Contains(js, "/public/custom-block") {
		t.Error("the page does not load the custom block from its own endpoint")
	}
}

func TestCustomBlockEmptyIsHidden(t *testing.T) {
	s, _ := newPublicServer(t, true)
	// Never set: the module does not exist rather than being blank.
	if w := do(t, s, http.MethodGet, "/public/custom-block", "203.0.113.9:1234"); w.Code != http.StatusNotFound {
		t.Errorf("an unset custom block = %d, want 404", w.Code)
	}
	setCustomHTML(t, s, "   \n\t ")
	if w := do(t, s, http.MethodGet, "/public/custom-block", "203.0.113.9:1234"); w.Code != http.StatusNotFound {
		t.Errorf("a whitespace-only custom block = %d, want 404", w.Code)
	}
	// The node card agrees, so the page never creates an iframe for nothing.
	var node map[string]any
	pub := do(t, s, http.MethodGet, "/api/public/node", "203.0.113.9:1234")
	if err := json.Unmarshal(pub.Body.Bytes(), &node); err != nil {
		t.Fatal(err)
	}
	if _, present := node["has_custom_block"]; present {
		t.Errorf("the node card advertises a custom block that is empty: %v", node["has_custom_block"])
	}
}

// TestBrandingIsGatedWithTheRest: branding is part of the public surface and
// disappears with it.
func TestBrandingPublicRoutesAreGated(t *testing.T) {
	s, _ := newPublicServer(t, false) // public view disabled
	setCustomHTML(t, s, "<p>hello</p>")
	if w := admin(t, s, http.MethodPost, "/api/branding/logo", smallPNG(t)); w.Code != http.StatusOK {
		t.Fatalf("logo upload with the public view off = %d (the admin side stays usable)", w.Code)
	}
	for _, p := range []string{"/public/assets/logo", "/public/custom-block"} {
		if w := do(t, s, http.MethodGet, p, "203.0.113.9:1234"); w.Code != http.StatusNotFound {
			t.Errorf("%s with the public view disabled = %d, want 404", p, w.Code)
		}
	}
}

// TestLogoPathIsNotAttackerControlled. The stored path is written by StoreLogo and
// is always the same constant; serving that constant rather than the stored string
// means a store row that somehow held a traversal could not become a file read.
func TestLogoPathIsNotAttackerControlled(t *testing.T) {
	s, ps := newPublicServer(t, true)
	if w := admin(t, s, http.MethodPost, "/api/branding/logo", smallPNG(t)); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	// Simulate a store row that has been tampered with.
	if err := ps.SetLogoPath("../../../../etc/passwd"); err != nil {
		t.Fatal(err)
	}
	w := do(t, s, http.MethodGet, "/public/assets/logo", "203.0.113.9:1234")
	if w.Code != http.StatusOK {
		t.Fatalf("logo = %d", w.Code)
	}
	if bytes.Contains(w.Body.Bytes(), []byte("root:")) {
		t.Fatal("the logo route read a file named by the stored path")
	}
	if _, err := png.Decode(bytes.NewReader(w.Body.Bytes())); err != nil {
		t.Errorf("the logo route served something that is not the re-encoded PNG: %v", err)
	}
}
