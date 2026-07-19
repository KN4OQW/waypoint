package store

import (
	"encoding/json"
	"testing"
)

func TestSetMany(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// A pre-existing key that SetMany must not touch.
	if err := s.Set("untouched", map[string]int{"x": 1}, "seed"); err != nil {
		t.Fatal(err)
	}

	vals := map[string]json.RawMessage{
		"a":  json.RawMessage(`{"n":1}`),
		"b":  json.RawMessage(`"two"`),
		"a2": json.RawMessage(`[1,2,3]`),
	}
	if err := s.SetMany(vals, "test"); err != nil {
		t.Fatal(err)
	}

	for k, want := range vals {
		got, ok, err := s.Get(k)
		if err != nil || !ok {
			t.Fatalf("Get(%q): ok=%v err=%v", k, ok, err)
		}
		if string(got) != string(want) {
			t.Errorf("Get(%q) = %s, want %s", k, got, want)
		}
	}
	// The unrelated key is intact.
	if _, ok, _ := s.Get("untouched"); !ok {
		t.Error("SetMany disturbed an unrelated key")
	}

	// Upsert semantics: SetMany over an existing key replaces its value.
	if err := s.SetMany(map[string]json.RawMessage{"a": json.RawMessage(`{"n":2}`)}, "test"); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.Get("a")
	if string(got) != `{"n":2}` {
		t.Errorf("SetMany did not upsert: got %s", got)
	}
}
