package wxzones

import (
	"os"
	"testing"
)

// readSource returns the package's implementation file. Split out so the import
// of os stays away from the test that asserts what the package imports.
func readSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("wxzones.go")
	if err != nil {
		t.Fatalf("reading wxzones.go: %v", err)
	}
	return string(b)
}
