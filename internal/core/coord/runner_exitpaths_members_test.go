package coord_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/spool"
	"github.com/ctxloom/ctxloom/internal/testsupport/spooltest"
)

// persistentMembers lists every path under harp's session dir that the
// session-member table classifies as Persist (paths.HarpMembers): what resume,
// distill, the session list and the human read after the run is over.
func persistentMembers(t *testing.T, harp string) []string {
	t.Helper()
	dir, err := paths.HarpDir(harp)
	require.NoError(t, err)
	var out []string
	require.NoError(t, filepath.WalkDir(dir, func(p string, _ fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		if m, ok := paths.ClassifyMember(filepath.ToSlash(rel)); ok && m.Lifetime == paths.Persist {
			out = append(out, p)
		}
		return nil
	}))
	return out
}

// lostPersistent returns each member of before that is no longer on disk.
//
// A live spool message is IN TRANSIT, not at rest: its reader's consume is a
// rename into the direction's consumed/ sibling (spool.Consume), and nothing
// orders the coordinator's out/ sweep against the run going idle — the point
// the member set is taken at. A message found at its consume destination was
// delivered, not lost, so it counts as kept there; one at neither place, or
// dropped to failed/ instead, is lost.
func lostPersistent(before []string) []string {
	mapper := spool.NewHomeMapper()
	var lost []string
	for _, p := range before {
		if _, err := os.Lstat(p); err == nil {
			continue
		}
		if dest, ok := consumedDestination(mapper, p); ok {
			if _, err := os.Lstat(dest); err == nil {
				continue
			}
		}
		lost = append(lost, p)
	}
	return lost
}

// consumedDestination is where spool.Consume puts the message at p, when p is
// a message in a consumable spool direction.
func consumedDestination(mapper spool.HomeMapper, p string) (string, bool) {
	ref, err := mapper.RefOf(p)
	if err != nil {
		return "", false
	}
	if ref.Dir, err = ref.Dir.Consumed(); err != nil {
		return "", false
	}
	dest, err := mapper.Resolve(ref)
	return dest, err == nil
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
	ref, err := w.Write(&spool.Message{Kind: "result", FromHarp: harp, To: "parent", Body: "turn result"})
	require.NoError(t, err)
	outPath, err := mapper.Resolve(ref)
	require.NoError(t, err)
	persist, err := paths.HarpPersistDir(harp)
	require.NoError(t, err)
	transcript := filepath.Join(persist, paths.CanonicalTranscriptFileName)
	require.NoError(t, os.WriteFile(transcript, nil, 0o600))

	before := persistentMembers(t, harp)
	require.Contains(t, before, outPath, "the member set must be taken while the message is still in out/")

	done, err := spool.Consume(mapper, ref)
	require.NoError(t, err)
	require.Empty(t, lostPersistent(before), "a message the coordinator consumed was delivered, not lost")

	donePath, err := mapper.Resolve(done)
	require.NoError(t, err)
	require.NoError(t, os.Remove(donePath))
	require.NoError(t, os.Remove(transcript))
	require.ElementsMatch(t, []string{outPath, transcript}, lostPersistent(before),
		"a message at neither its path nor its consume destination, and a removed member, are lost")
}
