package state

import (
	"testing"
	"time"

	"github.com/spf13/afero"
)

// These tests hold Store's documented race analysis to the code. Store's
// comment used to say a lost read-modify-write "fails safe — the denial just
// repeats and re-arms". That is true in one direction only, and the tests
// below are the two directions side by side. They are deterministic by
// construction — two Stores opened over one file, saved in a chosen order —
// because a green -race run proves nothing about a race that did not happen to
// interleave (and this one is a lost UPDATE, which the detector cannot see at
// all: the two writers are separate processes in production).
//
// If the persistence layer ever gains a lock or a delta-merged Save,
// TestLostClearResurrectsASpentOverride is the test that must change, which is
// the point of writing it down rather than leaving it in prose.

func armedStore(t *testing.T, fs afero.Fs, path, cmd string, now time.Time) {
	t.Helper()
	s := Open(fs, path)
	s.Arm(cmd, now, 0, time.Minute)
	if err := s.Save(now); err != nil {
		t.Fatalf("Save: %v", err)
	}
}

// The safe direction the comment describes: a lost ARM costs the user nothing
// worse than being denied again.
func TestLostArmFailsSafe(t *testing.T) {
	fs := afero.NewMemMapFs()
	const path = "/p/.ltk/state.json"
	now := time.Unix(1_700_000_000, 0)

	a := Open(fs, path) // both invocations start from an empty store
	b := Open(fs, path)
	a.Arm("git push --force", now, 0, time.Minute)
	if err := a.Save(now); err != nil {
		t.Fatalf("Save: %v", err)
	}
	b.Arm("rm -rf /", now, 0, time.Minute)
	if err := b.Save(now); err != nil {
		t.Fatalf("Save: %v", err)
	}

	reopened := Open(fs, path)
	if reopened.Armed("git push --force", now) {
		t.Skip("this Save no longer clobbers, so there is no lost arm to observe")
	}
	// b's whole-map write dropped a's arm. The user simply gets denied again
	// and a fresh override is armed: nothing was permitted that was not asked
	// for.
	if !reopened.Armed("rm -rf /", now) {
		t.Fatal("b's own arm did not survive its Save")
	}
}

// The UNSAFE direction, which the comment did not describe: the same whole-map
// write applied from a STALE snapshot resurrects an override the user already
// spent. Nothing here is concurrent in the Go sense — it is two hook
// invocations, each of which read the file before the other wrote it, which is
// the ordinary case when an agent issues parallel tool calls.
//
// The consequence is that ltk acts on a consent the user did not give twice: a
// single deliberate repeat was consumed, and an unrelated command's denial
// silently re-arms the first one, so the NEXT repeat of it is allowed with no
// fresh denial and no fresh delay.
func TestLostClearResurrectsASpentOverride(t *testing.T) {
	fs := afero.NewMemMapFs()
	const path = "/p/.ltk/state.json"
	now := time.Unix(1_700_000_000, 0)

	armedStore(t, fs, path, "git push --force", now)

	// The fixture must be hostile from Store's vantage point: the override
	// really is live on disk before anything below runs.
	if !Open(fs, path).Armed("git push --force", now) {
		t.Fatal("fixture is not hostile: nothing was armed to lose")
	}

	// An unrelated denial arrives and reads the store — this is the stale
	// snapshot. In production it is a second hook process, not a goroutine.
	stale := Open(fs, path)

	// Meanwhile the user repeats the command and the override is consumed.
	consumer := Open(fs, path)
	if !consumer.Ready("git push --force", now) {
		t.Fatal("fixture is not hostile: the override was not consumable")
	}
	consumer.Clear("git push --force")
	if err := consumer.Save(now); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if Open(fs, path).Armed("git push --force", now) {
		t.Fatal("fixture is not hostile: the clear did not reach disk")
	}

	// The unrelated denial now arms its own key and writes ITS whole map.
	stale.Arm("go test ./...", now, 0, time.Minute)
	if err := stale.Save(now); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if !Open(fs, path).Armed("git push --force", now) {
		t.Fatal("the spent override was NOT resurrected — Store's persistence " +
			"no longer clobbers from a stale snapshot, so its doc comment " +
			"should stop describing this hazard")
	}
}
