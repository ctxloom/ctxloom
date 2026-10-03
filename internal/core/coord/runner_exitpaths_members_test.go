package coord_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/spool"
	"github.com/ctxloom/ctxloom/internal/testsupport/spooltest"
)

// sessionMembers is a snapshot of a session's Persist members: their paths,
// and — for each inbox message among them — its spool identity, read while
// the file still exists, because a delivered inbox message is deleted and
// only its identity survives it (spool.Deliver).
type sessionMembers struct {
	Paths    []string
	harp     string
	identity map[string]string // spool message path -> spool identity
	outbound map[string]bool   // spool message paths that are out/ messages
}

// persistentMembers lists every path under harp's session dir that the
// session-member table classifies as Persist (paths.HarpMembers): what resume,
// distill, the session list and the human read after the run is over.
func persistentMembers(t *testing.T, harp string) sessionMembers {
	t.Helper()
	dir, err := paths.HarpDir(harp)
	require.NoError(t, err)
	out := sessionMembers{harp: harp, identity: map[string]string{}, outbound: map[string]bool{}}
	require.NoError(t, filepath.WalkDir(dir, func(p string, _ fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		if m, ok := paths.ClassifyMember(filepath.ToSlash(rel)); ok && m.Lifetime == paths.Persist {
			out.Paths = append(out.Paths, p)
		}
		return nil
	}))
	for _, d := range []spool.Dir{spool.DirIn, spool.ClaimedDirName, spool.DirOut} {
		for _, e := range spooltest.Entries(t, harp, d) {
			path, err := spool.DirPath(spool.NewHomeMapper(), harp, d)
			require.NoError(t, err)
			out.identity[filepath.Join(path, e.Ref.Name)] = e.Identity()
			if d == spool.DirOut {
				out.outbound[filepath.Join(path, e.Ref.Name)] = true
			}
		}
	}
	return out
}

// lostPersistent returns each member of before that is no longer on disk.
//
// A live spool message is IN TRANSIT, not at rest. Both directions finish a
// message by deleting it and recording its identity — an out/ message in the
// routed record (spool.Consume), an inbox message in the delivered record
// (spool.Deliver) — and nothing orders either against the run going idle, the
// point the member set is taken at. A message whose identity is in its
// direction's record was handled, not lost; one in neither, or dropped to
// failed/ instead, is lost.
func lostPersistent(before sessionMembers) []string {
	mapper := spool.NewHomeMapper()
	delivered, err := spool.DeliveredIdentities(mapper, before.harp)
	if err != nil {
		delivered = nil
	}
	var lost []string
	for _, p := range before.Paths {
		if _, err := os.Lstat(p); err == nil {
			continue
		}
		if id, ok := before.identity[p]; ok {
			if before.outbound[p] {
				if routed, err := spool.Routed(mapper, before.harp, id); err == nil && routed {
					continue
				}
			} else if _, done := delivered[id]; done {
				continue
			}
		}
		lost = append(lost, p)
	}
	return lost
}

// The runtime-stop exit path's CI failure, forced: the member set is taken
// while the child's reply still sits in out/, and the coordinator's sweep
// consumes it before the check. The delivered message must count as kept;
// a message or plain member actually removed must still count as lost.
func TestRunnerExitPaths_ConsumedSpoolMessageIsKept(t *testing.T) {
	spooltest.TeeHome(t)
	const harp = "third-happy-daily"
	mapper := spool.NewHomeMapper()
	w, err := spool.NewWriter(mapper, harp, spool.DirOut, harp)
	require.NoError(t, err)
	kept, err := w.Write(&spool.Message{Kind: "result", FromHarp: harp, To: "parent", OriginID: "m-routed", Body: "turn result"})
	require.NoError(t, err)
	gone, err := w.Write(&spool.Message{Kind: "result", FromHarp: harp, To: "parent", OriginID: "m-dropped", Body: "vanished"})
	require.NoError(t, err)
	keptP, err := mapper.Resolve(kept)
	require.NoError(t, err)
	goneP, err := mapper.Resolve(gone)
	require.NoError(t, err)
	persist, err := paths.HarpPersistDir(harp)
	require.NoError(t, err)
	transcript := filepath.Join(persist, paths.CanonicalTranscriptFileName)
	require.NoError(t, os.WriteFile(transcript, nil, 0o600))

	before := persistentMembers(t, harp)
	require.Contains(t, before.Paths, keptP, "the member set must be taken while the message is still in out/")

	require.NoError(t, spool.Consume(mapper, kept, "m-routed", time.Now()))
	require.Empty(t, lostPersistent(before), "a message the coordinator routed was delivered, not lost")

	require.NoError(t, os.Remove(goneP))
	require.NoError(t, os.Remove(transcript))
	require.ElementsMatch(t, []string{goneP, transcript}, lostPersistent(before),
		"a message removed without its routed record, and a removed member, are lost")
}

// The inbox half: the member set is taken while mail still sits in the
// child's in/, and the runner delivers it before the check. Delivery deletes
// the file and records its identity, so it must count as kept; a file removed
// without that record must still count as lost.
func TestRunnerExitPaths_DeliveredInboxMessageIsKept(t *testing.T) {
	spooltest.TeeHome(t)
	const harp = "fourth-quiet-inbox"
	mapper := spool.NewHomeMapper()
	w, err := spool.NewWriter(mapper, harp, spool.DirIn, "coord")
	require.NoError(t, err)
	kept, err := w.Write(&spool.Message{Kind: "message", FromHarp: "parent", To: harp, OriginID: "m-kept", Body: "delivered"})
	require.NoError(t, err)
	gone, err := w.Write(&spool.Message{Kind: "message", FromHarp: "parent", To: harp, OriginID: "m-gone", Body: "vanished"})
	require.NoError(t, err)
	goneP, err := mapper.Resolve(gone)
	require.NoError(t, err)

	before := persistentMembers(t, harp)
	require.NoError(t, spool.Deliver(mapper, kept, "m-kept", time.Now()))
	require.NoError(t, os.Remove(goneP))
	assert.Equal(t, []string{goneP}, lostPersistent(before),
		"a delivered inbox message is kept by its record; one removed without it is lost")
}
