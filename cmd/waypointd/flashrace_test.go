package main

import (
	"encoding/json"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/config"
	"github.com/KN4OQW/waypoint/internal/flash"
)

// raceFlasher is a flasher wired the way the daemon wires one, so start's
// background goroutine has an engine to run against and fails on its own terms
// instead of panicking on a nil one.
func raceFlasher(t *testing.T) *flasher {
	t.Helper()
	return flashTestServer(t, config.HardwareState{}, stubSource{cat: stubCatalog()}, true).flash
}

// The job start returns is written by the flash goroutine and read by the HTTP
// handler that JSON-encodes it, on another goroutine and without the mutex. They
// must not be the same object.
//
// Handing out the live pointer is what made encoding/json race flasher.publish,
// which the detector reported intermittently against the existing flash tests.
func TestFlashStartReturnsACopyNotTheLiveJob(t *testing.T) {
	f := raceFlasher(t)

	job, err := f.start(flash.Request{}, func() {})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if job == f.job {
		t.Fatal("start returned the live job; an encoder reading it races the flash goroutine")
	}
}

// And the copy really is insulated: encoding it while the owner publishes progress
// into the live job is clean under -race.
//
// The job is installed directly rather than through start, because start's
// background goroutine finishes the stubbed flash almost immediately and sets
// Ended — after which publish is a no-op and there is no writer left to race. An
// earlier version of this test drove it through start and was flaky for exactly
// that reason.
func TestFlashJobCopyIsSafeToEncodeWhileTheOwnerWrites(t *testing.T) {
	f := &flasher{subs: map[chan flash.Progress]struct{}{}}
	f.job = &flashJob{ID: "1-race", Stage: flash.StageChoosing, Started: time.Now().UTC()}
	held := *f.job // what start hands a caller

	// wrote closes once the writer has actually published, so the assertion below
	// waits for it instead of assuming 2000 encodes is long enough for the
	// scheduler to have run the goroutine.
	//
	// It is not long enough, and that is not theoretical: this failed on the arm64
	// runner with "the writer was never exercised" after an unrelated change made
	// the package finish three times faster. The encodes are a tight loop with no
	// blocking call in it, so on a busy or single-core runner the main goroutine can
	// hold its P for all 2000 iterations and reach close(stop) before the writer has
	// run once. Nothing in the language promises otherwise — an unsynchronised
	// goroutine is guaranteed to run eventually, not by any particular point.
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wrote := make(chan struct{})
	var once sync.Once
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 1; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			f.publish(flash.Progress{Stage: flash.StageWriting, Done: i, Total: 100, Detail: "writing"})
			once.Do(func() { close(wrote) })
		}
	}()
	for i := 0; i < 2000; i++ {
		if err := json.NewEncoder(io.Discard).Encode(&held); err != nil {
			t.Fatalf("encode: %v", err)
		}
	}
	// The race the test is about has already had its 2000 chances to happen by
	// here; this only makes the "did the writer run at all" check deterministic.
	select {
	case <-wrote:
	case <-time.After(10 * time.Second):
		t.Fatal("the writer goroutine never published; it was starved rather than racing")
	}
	close(stop)
	wg.Wait()

	// The copy is a snapshot of the moment it was taken, which is the contract: a
	// handler reports the job as started, and progress arrives on the event stream
	// rather than by the struct changing under the encoder.
	if held.Done != 0 || held.Stage != flash.StageChoosing {
		t.Errorf("the held copy moved: stage=%v done=%d", held.Stage, held.Done)
	}
	if got := f.snapshot(); got == nil || got.Done == 0 {
		t.Error("the live job did not advance; the writer was never exercised")
	}
}

// snapshot has always copied; this keeps it that way, since it is the other path
// a handler reads a job through.
func TestFlashSnapshotIsACopy(t *testing.T) {
	f := &flasher{subs: map[chan flash.Progress]struct{}{}}
	f.job = &flashJob{ID: "1-snap", Stage: flash.StageChoosing, Started: time.Now().UTC()}

	a := f.snapshot()
	if a == f.job {
		t.Fatal("snapshot returned the live job")
	}
	f.publish(flash.Progress{Stage: flash.StageWriting, Done: 42, Total: 100})
	if a.Done != 0 {
		t.Errorf("an earlier snapshot changed underneath its holder: Done = %d", a.Done)
	}
	if b := f.snapshot(); b.Done != 42 {
		t.Errorf("a later snapshot did not see the update: Done = %d", b.Done)
	}
}

// Asking about a flasher that has never run must stay safe: running() calls
// Running() on a snapshot that is nil until the first flash.
func TestFlashRunningBeforeAnyJob(t *testing.T) {
	f := &flasher{subs: map[chan flash.Progress]struct{}{}}
	if f.running() {
		t.Error("a flasher with no job reported one running")
	}
	if f.snapshot() != nil {
		t.Error("snapshot invented a job")
	}
}
