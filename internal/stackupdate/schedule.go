package stackupdate

import "time"

// WithinQuietWindow reports whether now (in its own location) falls in the one-hour
// window starting at the "HH:MM" quiet time. A one-hour window means a poller
// ticking more often than hourly reliably lands at least one tick inside it. An
// unparseable quiet string is never in-window (auto-apply then simply never fires).
func WithinQuietWindow(now time.Time, quiet string) bool {
	t, err := time.Parse("15:04", quiet)
	if err != nil {
		return false
	}
	start := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, now.Location())
	return !now.Before(start) && now.Before(start.Add(time.Hour))
}

// DueForAutoApply is the auto-apply decision: opt-in is on, updates are available,
// now is in the quiet window, and we have not already auto-applied in this window
// today (lastApply is on an earlier local day). Keeping it to once per local day
// stops a repeated in-window poll from re-applying.
func DueForAutoApply(now, lastApply time.Time, quiet string, autoApply, available bool) bool {
	if !autoApply || !available {
		return false
	}
	if !WithinQuietWindow(now, quiet) {
		return false
	}
	return !sameLocalDay(now, lastApply)
}

func sameLocalDay(a, b time.Time) bool {
	if b.IsZero() {
		return false
	}
	ay, am, ad := a.Date()
	by, bm, bd := b.In(a.Location()).Date()
	return ay == by && am == bm && ad == bd
}
