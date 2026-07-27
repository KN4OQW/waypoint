package config

import (
	"encoding/json"
	"testing"
)

// The out-of-the-box policy checks for updates: the check is anonymous, so the
// default is on and an operator who does not want it turns it off (#15).
func TestDefaultUpdateChecksOn(t *testing.T) {
	if !DefaultUpdate().CheckEnabled {
		t.Fatal("DefaultUpdate must enable automatic checks")
	}
}

// Turning checks off changes exactly one field. This is the "disabling checks
// disables nothing else" half of #15 at the persistence layer: a partial save of
// check_enabled must not disturb the channel, the quiet window, or auto-apply.
func TestSetUpdateCheckEnabledPreservesTheRestOfThePolicy(t *testing.T) {
	st := newStore(t)
	seed := UpdatePrefs{Channel: ChannelBeta, CheckEnabled: true, AutoApply: true, QuietWindow: "03:30"}
	if err := st.Set("update", seed, "test"); err != nil {
		t.Fatal(err)
	}
	if err := SetUpdate(st, []byte(`{"check_enabled":false}`), "test"); err != nil {
		t.Fatal(err)
	}
	var got UpdatePrefs
	if _, err := st.GetInto("update", &got); err != nil {
		t.Fatal(err)
	}
	want := seed
	want.CheckEnabled = false
	if got != want {
		t.Fatalf("saving check_enabled=false disturbed the rest of the policy:\n want %+v\n  got %+v", want, got)
	}
}

// An update row written before check_enabled existed must come out of an upgrade
// still checking — a missing key is not an opt-out anyone chose.
func TestBackfillUpdateCheckEnabled(t *testing.T) {
	st := newStore(t)

	// No update row at all: nothing to do (the section backfill seeds it instead).
	if wrote, err := BackfillUpdateCheckEnabled(st); err != nil || wrote {
		t.Fatalf("missing row: wrote=%v err=%v, want false/nil", wrote, err)
	}

	// The pre-#15 shape: no check_enabled key. It is seeded on, and the operator's
	// other settings survive.
	old := json.RawMessage(`{"channel":"beta","auto_apply":true,"quiet_window":"02:15"}`)
	if err := st.Set("update", old, "test"); err != nil {
		t.Fatal(err)
	}
	wrote, err := BackfillUpdateCheckEnabled(st)
	if err != nil || !wrote {
		t.Fatalf("old row: wrote=%v err=%v, want true/nil", wrote, err)
	}
	var got UpdatePrefs
	if _, err := st.GetInto("update", &got); err != nil {
		t.Fatal(err)
	}
	want := UpdatePrefs{Channel: ChannelBeta, CheckEnabled: true, AutoApply: true, QuietWindow: "02:15"}
	if got != want {
		t.Fatalf("backfill did not preserve the policy:\n want %+v\n  got %+v", want, got)
	}

	// An operator who opted out stays opted out: a row that carries the field is
	// never rewritten, and a second backfill is a no-op.
	off := UpdatePrefs{Channel: ChannelStable, CheckEnabled: false, AutoApply: false, QuietWindow: DefaultQuietWindow}
	if err := st.Set("update", off, "test"); err != nil {
		t.Fatal(err)
	}
	if wrote, err := BackfillUpdateCheckEnabled(st); err != nil || wrote {
		t.Fatalf("opted-out row: wrote=%v err=%v, want false/nil", wrote, err)
	}
	if _, err := st.GetInto("update", &got); err != nil {
		t.Fatal(err)
	}
	if got.CheckEnabled {
		t.Fatal("backfill re-enabled checks on a node that opted out")
	}
}
