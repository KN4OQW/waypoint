package status

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// Property 7 (no log scraping — structurally): the status fold reads no files. It
// derives everything from the in-memory event stream, so there is no log-file
// reader here to disable — the #5 "verified by disabling all log file reads"
// acceptance holds by construction, and this guard keeps it from regressing.
func TestNoLogScraping(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	// File-reading / process / log-tailing packages have no business in the fold.
	forbidden := map[string]bool{
		"os": true, "os/exec": true, "bufio": true, "io/ioutil": true,
		"github.com/fsnotify/fsnotify": true,
	}
	for _, pkg := range pkgs {
		for name, file := range pkg.Files {
			if strings.HasSuffix(name, "_test.go") {
				continue // tests may open temp files; the guard is about the package itself
			}
			for _, imp := range file.Imports {
				path := strings.Trim(imp.Path.Value, `"`)
				if forbidden[path] {
					t.Errorf("%s imports %q — the status fold must read no files (RFC-0008 no-log-scraping)", name, path)
				}
			}
		}
	}
}
