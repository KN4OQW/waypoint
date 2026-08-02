package publicview

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // registers the JPEG decoder; D4 permits PNG and JPEG uploads
	"image/png"
	"io"
	"os"
	"path/filepath"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

// Branding: the three operator-authored blocks on the public page, and the three
// different amounts of trust they get.
//
// The logo is a raster the node re-encodes itself. The narrative is Markdown
// rendered and then sanitised. The custom HTML is served verbatim, but only into
// a sandboxed iframe that cannot reach this origin. Each is handled by exactly one
// of those mechanisms and never by a mixture, because the failure mode of "mostly
// sanitised" is indistinguishable from "sanitised" right up until it isn't.

// LogoMaxBytes is the upload ceiling (D4). A logo is a small raster displayed at
// 38 CSS pixels; a megabyte is already generous, and the limit exists to bound
// what an authenticated-but-careless operator can make the node decode.
const LogoMaxBytes = 1 << 20

// LogoRelPath is where the re-encoded logo lives under the store's data
// directory, and what branding.logo_path holds.
const LogoRelPath = "branding/logo.png"

// logoMaxDimension bounds the decoded image before re-encoding.
//
// This is the decompression-bomb guard, and it is a separate concern from
// LogoMaxBytes: PNG is deflate-compressed, so a few hundred kilobytes on the wire
// can be tens of thousands of pixels square once decoded — hundreds of megabytes
// of memory on a device with a gigabyte of it. The byte ceiling does not bound
// that; this does.
const logoMaxDimension = 2048

var (
	ErrLogoTooLarge  = errors.New("publicview: logo exceeds the size limit")
	ErrLogoFormat    = errors.New("publicview: logo must be a PNG or JPEG image")
	ErrLogoDimension = errors.New("publicview: logo dimensions are too large")
)

// StoreLogo validates an uploaded logo and writes a re-encoded copy under dir.
//
// The re-encode is the security control, not a convenience. Decoding to pixels and
// writing a fresh PNG from those pixels means the file the node serves shares no
// bytes with the file it was given, which:
//
//   - strips every metadata block, including the EXIF GPS tags a phone photo
//     carries. An operator uploading a picture of their tower should not thereby
//     publish the coordinates it was taken at, and they will not think to check.
//   - defeats polyglots. A file crafted to be a valid GIF and a valid JavaScript
//     program at once does not survive being decoded to a pixel grid and written
//     back out; what lands on disk is a PNG and nothing else.
//   - normalises the format, so the served Content-Type is a fact about the file
//     rather than a claim about it.
//
// SVG is rejected rather than sanitised (D4). An SVG is a document that can carry
// script, styles and external references; making one safe means solving the same
// problem the custom-HTML block solves with an iframe, for a logo. Two raster
// formats cover what anyone actually uploads.
func StoreLogo(dir string, r io.Reader) (relPath string, err error) {
	// One byte over the limit is enough to know: read the ceiling plus one and
	// check what came back, rather than trusting a Content-Length the client set.
	raw, err := io.ReadAll(io.LimitReader(r, LogoMaxBytes+1))
	if err != nil {
		return "", err
	}
	if len(raw) > LogoMaxBytes {
		return "", fmt.Errorf("%w: %d bytes, limit %d", ErrLogoTooLarge, len(raw), LogoMaxBytes)
	}
	if len(raw) == 0 {
		return "", fmt.Errorf("%w: empty upload", ErrLogoFormat)
	}

	// Decode with the two decoders that are registered, so anything else — SVG,
	// GIF, WebP, a polyglot, a rename — fails here rather than later.
	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrLogoFormat, err)
	}
	if format != "png" && format != "jpeg" {
		return "", fmt.Errorf("%w: got %s", ErrLogoFormat, format)
	}
	if cfg.Width > logoMaxDimension || cfg.Height > logoMaxDimension {
		return "", fmt.Errorf("%w: %dx%d, limit %d on a side", ErrLogoDimension, cfg.Width, cfg.Height, logoMaxDimension)
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrLogoFormat, err)
	}

	var buf bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.DefaultCompression}).Encode(&buf, img); err != nil {
		return "", err
	}

	full := filepath.Join(dir, filepath.FromSlash(LogoRelPath))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return "", err
	}
	// Write-then-rename, so a reader never sees a half-written logo and a failed
	// write leaves the previous one in place.
	tmp := full + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, full); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return LogoRelPath, nil
}

// narrativePolicy is the sanitiser applied to rendered narrative HTML.
//
// UGCPolicy is bluemonday's user-generated-content profile: it permits the
// formatting Markdown produces and strips script, event handlers, styles, iframes,
// objects and anything else that executes. It is applied to the OUTPUT of the
// Markdown renderer rather than to the input, which matters — Markdown that looks
// inert can render to HTML that is not, and sanitising the source would also
// corrupt the text an operator edits next.
var narrativePolicy = func() *bluemonday.Policy {
	p := bluemonday.UGCPolicy()
	// Links leave this node, so they get the treatment any outbound link gets.
	p.RequireNoFollowOnLinks(true)
	p.RequireNoReferrerOnLinks(true)
	p.AddTargetBlankToFullyQualifiedLinks(true)
	return p
}()

// markdown is the renderer. GFM for tables and autolinks, which is what operators
// write. Raw HTML in the source is NOT enabled: goldmark leaves it out by default,
// so an operator who pastes a <script> into the narrative gets it escaped by the
// renderer and then removed by the sanitiser — two independent mechanisms, either
// of which alone would be sufficient.
var markdown = goldmark.New(goldmark.WithExtensions(extension.GFM))

// RenderNarrative turns stored Markdown into sanitised HTML fit for the public
// page. An empty source renders to empty output, which the page reads as "no
// narrative" and omits the block entirely.
func RenderNarrative(src string) (string, error) {
	if src == "" {
		return "", nil
	}
	var buf bytes.Buffer
	if err := markdown.Convert([]byte(src), &buf); err != nil {
		return "", err
	}
	return narrativePolicy.Sanitize(buf.String()), nil
}
