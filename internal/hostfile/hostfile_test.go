package hostfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAssemble_NoHooks_ByteIdentical(t *testing.T) {
	base := []byte("MASTER-1 1.2.3.4 pass 62031\n")
	if got := Assemble(nil, base, nil); string(got) != string(base) {
		t.Errorf("no hooks should return base unchanged: got %q", got)
	}
	// A base without a trailing newline is likewise untouched when no hooks exist.
	raw := []byte("no-newline")
	if got := Assemble(nil, raw, nil); string(got) != "no-newline" {
		t.Errorf("no-hook base must not be mutated: got %q", got)
	}
}

func TestAssemble_Order(t *testing.T) {
	got := Assemble(
		[][]byte{[]byte("LOCAL-A a 1\n"), []byte("LOCAL-B b 2")}, // second lacks trailing NL
		[]byte("BASE base 3\n"),
		[][]byte{[]byte("TAIL-A t 4\n")},
	)
	want := "LOCAL-A a 1\nLOCAL-B b 2\nBASE base 3\nTAIL-A t 4\n"
	if string(got) != want {
		t.Errorf("assemble order/newlines wrong:\n got %q\nwant %q", got, want)
	}
}

func TestWriteWithHooks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "DMR_Hosts.txt")
	base := []byte("BASE base 3\n")

	// No hook dirs: file is exactly the base.
	if err := WriteWithHooks(path, base, "dmrhosts"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != string(base) {
		t.Fatalf("no hooks: file = %q, want %q", got, base)
	}
	if HasHooks(path) {
		t.Errorf("HasHooks should be false with no hook dirs")
	}

	// Add prepend + append hooks, out of lexical order on disk.
	mkdir(t, PrependDir(path))
	mkdir(t, AppendDir(path))
	mustWrite(t, filepath.Join(PrependDir(path), "20-b"), "LOCAL-B b 2\n")
	mustWrite(t, filepath.Join(PrependDir(path), "10-a"), "LOCAL-A a 1\n")
	mustWrite(t, filepath.Join(AppendDir(path), "10-tail"), "TAIL t 4\n")
	mustWrite(t, filepath.Join(PrependDir(path), ".swp"), "IGNORED\n") // dot-file skipped

	if !HasHooks(path) {
		t.Errorf("HasHooks should be true once hook files exist")
	}
	if err := WriteWithHooks(path, base, "dmrhosts"); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(path)
	want := "LOCAL-A a 1\nLOCAL-B b 2\nBASE base 3\nTAIL t 4\n"
	if string(got) != want {
		t.Errorf("assembled file wrong:\n got %q\nwant %q", got, want)
	}

	// Update survival: re-fetch the same base with the same hooks ⇒ byte-identical.
	if err := WriteWithHooks(path, base, "dmrhosts"); err != nil {
		t.Fatal(err)
	}
	got2, _ := os.ReadFile(path)
	if string(got) != string(got2) {
		t.Errorf("re-fetch not byte-identical (update survival):\n%q\n%q", got, got2)
	}
}

func mkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
