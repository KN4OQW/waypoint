package idresolve_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"strconv"
	"strings"
	"testing"
)

// TestPackageImportsOnlyTheStandardLibrary is the deliverable's no-cycle
// requirement turned into something that fails rather than something the package
// comment asserts.
//
// The chain has to be droppable into internal/bus/frames' Resolver slot, and that
// only stays true while this package pulls nothing behind it. The moment it
// imports internal/phonebook (for an Entry) or internal/dmrids (for a Table), the
// frame library — which is required to do no file or database I/O — acquires a
// transitive path to a SQL driver, and the option of ever having frames import
// this package closes for good.
//
// The legs are therefore declared as interfaces over primitives, and this test is
// what keeps the next person from "simplifying" that by importing the concrete
// types. It parses this package's own sources, so it needs no build tooling and no
// dependency of its own.
//
// The equivalent check on the other side is the compile-time assertion in
// idresolve_test.go (`var _ frames.Resolver = (*idresolve.Chain)(nil)`), which
// proves the interface is actually satisfied. Between them: the chain fits the
// slot, and fitting it costs the frame layer nothing.
func TestPackageImportsOnlyTheStandardLibrary(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		// Production sources only. The test files deliberately import phonebook,
		// dmrids and frames — that is how they prove the couplings — and holding
		// them to this rule would make the rule untestable.
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) == 0 {
		t.Fatal("parsed no packages; this test would pass vacuously")
	}
	var files int
	for _, pkg := range pkgs {
		for name, f := range pkg.Files {
			files++
			for _, imp := range f.Imports {
				path, err := strconv.Unquote(imp.Path.Value)
				if err != nil {
					t.Fatal(err)
				}
				// A standard-library path has no dot in its first segment.
				if first, _, _ := strings.Cut(path, "/"); strings.Contains(first, ".") {
					t.Errorf("%s imports %q.\n"+
						"internal/idresolve must import nothing but the standard library: it is what "+
						"lets a Chain satisfy internal/bus/frames.Resolver without the frame layer "+
						"acquiring a path to file or database I/O. Declare the dependency as an "+
						"interface over primitives instead, the way Directory and Table are.",
						name, path)
				}
			}
		}
	}
	if files == 0 {
		t.Fatal("found no non-test files; this test would pass vacuously")
	}
}
