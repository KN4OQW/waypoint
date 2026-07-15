package netconfig

import "time"

// Clock abstracts time for the confirm-or-revert Guard so its state machine can be
// driven by a fake clock in tests (the rollback deadline is the whole point of the
// guard, so it must be testable without real sleeps). The production clock is
// realClock; tests inject a fake that fires timers on command.
type Clock interface {
	Now() time.Time
	// AfterFunc schedules f to run once after d and returns a handle that can
	// cancel it (returning false if it had already fired). Mirrors time.AfterFunc.
	AfterFunc(d time.Duration, f func()) Timer
}

// Timer is a cancelable scheduled callback (the subset of *time.Timer the Guard uses).
type Timer interface {
	// Stop cancels the timer, returning false if it had already fired.
	Stop() bool
}

// realClock is the production Clock, backed by the time package.
type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) AfterFunc(d time.Duration, f func()) Timer { return time.AfterFunc(d, f) }
