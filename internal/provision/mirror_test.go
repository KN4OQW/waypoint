package provision

import (
	"testing"

	"github.com/KN4OQW/waypoint/internal/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// Property 1: a store that has never been provisioned reads false — the same
// needs-setup default the absent marker gives.
func TestMirrorDefaultsToFalse(t *testing.T) {
	m, err := NewMirror(openStore(t))
	if err != nil {
		t.Fatal(err)
	}
	got, err := m.Get()
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Error("Get = true on a fresh store")
	}
}

// Property 2: Set round-trips, both ways. The false direction matters — a factory
// reset has to be able to put the node back into needs-setup.
func TestMirrorSetGet(t *testing.T) {
	m, err := NewMirror(openStore(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []bool{true, false, true} {
		if err := m.Set(want); err != nil {
			t.Fatal(err)
		}
		got, err := m.Get()
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("Get = %v after Set(%v)", got, want)
		}
	}
}

// Property 3: NewMirror is idempotent. It runs on every daemon start, and the
// second run must be a no-op rather than a duplicate-column error.
func TestMirrorMigrateIsIdempotent(t *testing.T) {
	s := openStore(t)
	m, err := NewMirror(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Set(true); err != nil {
		t.Fatal(err)
	}

	again, err := NewMirror(s)
	if err != nil {
		t.Fatalf("second NewMirror: %v", err)
	}
	got, err := again.Get()
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Error("re-attaching the mirror lost the flag")
	}
}

// Property 4: the mirror never touches the config surface. It lives in meta, not
// in the settings key tree, so it is not compiled into any INI, not returned by
// /api/config, and not carried in an exported profile.
func TestMirrorStaysOffTheConfigSurface(t *testing.T) {
	s := openStore(t)
	m, err := NewMirror(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Set(true); err != nil {
		t.Fatal(err)
	}

	empty, err := s.IsEmpty()
	if err != nil {
		t.Fatal(err)
	}
	if !empty {
		all, _ := s.All()
		t.Errorf("the mirror wrote into the settings tree: %v", all)
	}
}
