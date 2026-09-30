package runner

import (
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/launch/launchtest"
	"github.com/ctxloom/ctxloom/internal/core/spool"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/testsupport/coordharness"
	"github.com/ctxloom/ctxloom/internal/testsupport/scriptedchat"
	"github.com/ctxloom/ctxloom/internal/testsupport/spooltest"
)

// The runner side's suite drives its Home and engine host against a real,
// served coordinator; what it needs of the coordinator is its constructor
// and a spawner that spawns nothing (these tests dial in themselves). The
// helpers keep the spellings the tests were written with.

const conformanceWait = 5 * time.Second

func termSink() report.Sink       { return coordharness.Sink() }
func termRep() report.Reporter    { return report.To(termSink()) }
func teeHome(t *testing.T) string { return spooltest.TeeHome(t) }

func resetStrictness(t *testing.T) {
	t.Helper()
	strictness.Reset()
	t.Cleanup(func() { strictness.Reset() })
}

// ownerIdentity is the coordinating session's identity (depth 0).
func ownerIdentity() coord.Identity { return coord.Identity{Harp: coordharness.OwnerHarp, Depth: 0} }

func ownerLaunch(harp, backend, label, model, workDir, perm string) launch.Launch {
	return launchtest.Structured(harp, backend, label, model, workDir, perm)
}

func spoolEntries(t *testing.T, harp string, dir spool.Dir) []spool.Entry {
	return spooltest.Entries(t, harp, dir)
}

// writeSpoolMail puts one message file straight into harp's in/ spool the way
// the coordinator's courier would.
func writeSpoolMail(t *testing.T, harp, from, kind, body string) {
	t.Helper()
	spoolKind, err := coord.SpoolKindForMail(kind)
	if err != nil {
		t.Fatal(err)
	}
	spooltest.WriteMail(t, harp, from, spoolKind, body, "coord")
}

type scriptedChat = scriptedchat.Chat

// newTestCoordinator serves a coordinator for the runner side to dial.
func newTestCoordinator(t *testing.T, _ coord.Spawner, _ func() time.Time) *coord.Coordinator {
	t.Helper()
	c := coordharness.New(t, t.TempDir())
	if err := coordgrpc.Serve(c); err != nil {
		t.Fatalf("serve coordinator: %v", err)
	}
	return c
}

// newFakeSpawner is the spawner these tests hand newTestCoordinator, which
// ignores it: the runner side spawns nothing.
func newFakeSpawner(map[string]any, func() *scriptedChat) coord.Spawner {
	return coordharness.NopSpawner{}
}
