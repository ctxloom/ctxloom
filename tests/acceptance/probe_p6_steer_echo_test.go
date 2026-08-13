// Untagged, like the kit it checks: `just test` runs every case below with no
// built binary, no engine and no paid turn.
//
// THESE ARE THE TESTS OF A TEST, and the bar is the one the design sets for
// every probe's verdict function: it is the suite's trust anchor, so it gets
// adversarial cases rather than a happy path. Specifically, each check below
// exists to make one FALSE GREEN impossible:
//
//   - an unminted (empty) harp, which strings.Contains matches inside every
//     string ever sent;
//   - a foreign channel: the child echoing the marker it read from its own
//     COMPOSED CONTEXT rather than the steer it was handed, which is the exact
//     way this probe could look green while proving j002300's already-proven
//     direction a second time;
//   - a mailbox that returned nothing at all, which must be named a silent
//     no-op and never blurred into an ordinary mismatch;
//   - a spool directory that exists and is empty, which is the whole reason the
//     soak assertion is made on payload bytes instead of on a flag.
//
// Mutation-visible on purpose: flip any strings.Contains below to an equality,
// delete a shape branch, or relax the empty-value guards, and one of these
// fails.
package acceptance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func p6TestVerdict() probeVerdict {
	return p6Verdict(p6Cell("claude-code", "host", "none"))
}

func TestP6Verdict_CarriesTheRegistrysOwnChannel(t *testing.T) {
	v := p6TestVerdict()
	require.Equal(t, channelBusMessage, v.Channel,
		"P6's verdict must carry the SAME channel the registry declares for it; a probe whose failure message names a different channel than its registry row is attributing its reds to the wrong subsystem")
	require.Equal(t, probeP6, v.Cell.Probe)
	require.Contains(t, v.Cell.String(), "engine=claude-code")
}

func TestP6SteerBody_StatesTheHarpAndRefusesAnEmptyOne(t *testing.T) {
	body, err := p6SteerBody("swift-amber-falcon")
	require.NoError(t, err)
	require.Contains(t, body, "swift-amber-falcon",
		"the steer must actually carry the harp — it is the only place the child can learn it")
	require.Contains(t, body, "agent_send",
		"the steer must name the tool the child is being asked to call, or a red measures the wording rather than the channel")

	for _, empty := range []string{"", "   ", "\t\n"} {
		_, err := p6SteerBody(empty)
		require.Error(t, err,
			"composing a steer around an empty harp must fail loudly: the echo assertion is strings.Contains(body, harp), and every body contains the empty string")
	}
}

func TestP6AssertEcho_GreenOnlyWhenAHarpArrives(t *testing.T) {
	v := p6TestVerdict()
	const harp = "swift-amber-falcon"

	t.Run("the echo arrives verbatim", func(t *testing.T) {
		require.NoError(t, p6AssertEcho(v, harp, []string{"swift-amber-falcon"}))
	})

	t.Run("the echo arrives among other traffic", func(t *testing.T) {
		// Two messages reach a coordinator from one child harp on a live run —
		// the child's own agent_send and bridgeTurnResult's copy of its turn —
		// and which lands in which agent_recv batch is a race. A verdict that
		// only looked at the first would make a green engine flake red.
		require.NoError(t, p6AssertEcho(v, harp, []string{
			"I have received your instruction and will comply.",
			"swift-amber-falcon",
		}))
	})

	t.Run("the harp embedded in a sentence still counts", func(t *testing.T) {
		require.NoError(t, p6AssertEcho(v, harp, []string{"the phrase is swift-amber-falcon"}),
			"P6 asserts DELIVERY of a mid-session message, not prose obedience; the strict output contract is P0's job")
	})
}

func TestP6AssertEcho_EmptyHarpIsRefusedRatherThanVacuouslyGreen(t *testing.T) {
	v := p6TestVerdict()
	err := p6AssertEcho(v, "", []string{"anything at all"})
	require.Error(t, err, "an unminted harp must be refused: every body contains the empty string, so this would be the one check that proves the channel passing without looking at it")
	shape, ok := probeShapeOf(err)
	require.True(t, ok)
	require.Equal(t, shapeDelivery, shape)
}

func TestP6AssertEcho_ForeignChannelCannotFalseGreenIt(t *testing.T) {
	// THE FALSE GREEN THIS PROBE IS MOST EXPOSED TO. The child also holds a
	// wake-up marker in its own composed context (that is how the cell knows it
	// launched). If the verdict looked for anything BUT the steer harp, a child
	// that simply repeated its context marker — proving only the context
	// delivery j002300 already proves — would satisfy P6 and the steer would go
	// unexercised.
	v := p6TestVerdict()
	const steer = "swift-amber-falcon"
	const contextMarker = "P6-WAKE-MARKER-CLAUDE-CODE-9f2c"

	err := p6AssertEcho(v, steer, []string{contextMarker, "I can see " + contextMarker + " in my context."})
	require.Error(t, err, "a body carrying only the CONTEXT marker must not satisfy the BUS channel")
	shape, ok := probeShapeOf(err)
	require.True(t, ok)
	require.Equal(t, channelBusMessage.Shape, shape,
		"a child that spoke without the steer is a BUS-DELIVERY failure — the channel's own shape, never a generic mismatch")
	require.Contains(t, err.Error(), contextMarker,
		"the evidence must quote what the engine said instead, or a live red is unreadable")
}

func TestP6AssertEcho_SilenceIsNamedASilentNoOp(t *testing.T) {
	v := p6TestVerdict()
	err := p6AssertEcho(v, "swift-amber-falcon", nil)
	require.Error(t, err)
	shape, ok := probeShapeOf(err)
	require.True(t, ok)
	require.Equal(t, shapeSilentNoOp, shape,
		"nothing coming back at all is a different finding from the wrong thing coming back, and this project's characteristic bug is the first one")
}

func TestP6AssertEcho_CredentialFailureIsARunFailureNotADeliveryOne(t *testing.T) {
	// j002300 paid for this twice: a runner-exit report carrying codex's 401 is
	// a credential to renew, not a delegation regression. Misclassifying it as a
	// BUS-DELIVERY failure sends the next reader hunting a product bug that is
	// not there.
	v := p6TestVerdict()
	err := p6AssertEcho(v, "swift-amber-falcon", []string{
		"runner exited: Your access token could not be refreshed because your refresh token was already used (401 refresh_token_reused)",
	})
	require.Error(t, err)
	shape, ok := probeShapeOf(err)
	require.True(t, ok)
	require.Equal(t, shapeRunFailed, shape)
	require.Contains(t, err.Error(), "re-authenticate")
}

func TestP6AssertEcho_ACredentialWordInAGREENBodyDoesNotDowngradeIt(t *testing.T) {
	// The classifier must never be able to turn a pass into a fail: the harp
	// check runs first and wins.
	v := p6TestVerdict()
	require.NoError(t, p6AssertEcho(v, "swift-amber-falcon",
		[]string{"swift-amber-falcon (I got a 401 earlier but retried)"}))
}

// --- the spool evidence ------------------------------------------------------

// p6WriteSpoolFile is the test's own miniature of what coord's spool writer
// does: one file, in one plane, with the given bytes.
func p6WriteSpoolFile(t *testing.T, root, dir, name, body string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(dir))
	require.NoError(t, os.MkdirAll(full, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(full, name), []byte(body), 0o600))
}

func TestP6ReadSpoolCensus_MissingRootIsAnErrorNotAnEmptyCensus(t *testing.T) {
	_, err := p6ReadSpoolCensus(filepath.Join(t.TempDir(), "never-created"), "swift-amber-falcon")
	require.Error(t, err,
		"a census over a directory that was never created must fail loudly; reporting an empty census would report 'no evidence' for the most literal reason possible and read as a result")
}

func TestP6ReadSpoolCensus_CountsPlanesAndLocatesTheHarp(t *testing.T) {
	root := t.TempDir()
	const harp = "swift-amber-falcon"
	p6WriteSpoolFile(t, root, "in/consumed", "0001.msg.md", "kind: message\n---\n"+harp)
	p6WriteSpoolFile(t, root, "out", "0002.msg.md", "kind: message\n---\n"+harp)
	p6WriteSpoolFile(t, root, "out", "0003.msg.md", "kind: message\n---\nsomething else")

	c, err := p6ReadSpoolCensus(root, harp)
	require.NoError(t, err)
	require.Equal(t, 3, c.Total)
	require.Len(t, c.HarpIn, 1, "the consumed rename is the child runner's ACK — in/consumed is still the in plane, and asserting on in/ alone would red a cell for the child being prompt")
	require.Equal(t, "in/consumed/0001.msg.md", c.HarpIn[0])
	require.Len(t, c.HarpOut, 1)
	require.Contains(t, c.String(), "0003.msg.md", "the census is the evidence line; a renderer that drops files makes the spool look emptier than it was")
}

func TestP6ReadSpoolCensus_SubdirectoriesAreStructureNotMessages(t *testing.T) {
	root := t.TempDir()
	p6WriteSpoolFile(t, root, "in/consumed", "0001.msg.md", "body")
	c, err := p6ReadSpoolCensus(root, "")
	require.NoError(t, err)
	require.Equal(t, 1, c.Total, "in/ contains the consumed/ directory itself; counting it as a message would inflate every census by one and hide an empty in plane")
	require.Empty(t, c.Files["in"])
}

func TestP6AssertSpoolEvidence_EmptySpoolIsTheSilentNoOp(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "in"), 0o700))
	c, err := p6ReadSpoolCensus(root, "swift-amber-falcon")
	require.NoError(t, err)

	err = p6AssertSpoolEvidence(p6TestVerdict(), c, "swift-amber-falcon")
	require.Error(t, err, "an existing-but-empty spool is exactly the exit-0-with-zero-bytes failure this assertion exists to catch")
	shape, ok := probeShapeOf(err)
	require.True(t, ok)
	require.Equal(t, shapeSilentNoOp, shape)
}

func TestP6AssertSpoolEvidence_FilesWithoutTheHarpAreADeliveryFailure(t *testing.T) {
	root := t.TempDir()
	p6WriteSpoolFile(t, root, "in", "0001.msg.md", "kind: message\n---\nsome other traffic entirely")
	c, err := p6ReadSpoolCensus(root, "swift-amber-falcon")
	require.NoError(t, err)

	err = p6AssertSpoolEvidence(p6TestVerdict(), c, "swift-amber-falcon")
	require.Error(t, err)
	shape, ok := probeShapeOf(err)
	require.True(t, ok)
	require.Equal(t, channelBusMessage.Shape, shape)
	require.Contains(t, err.Error(), "0001.msg.md", "the census must ride the failure: which plane held which file IS the diagnostic")
}

func TestP6AssertSpoolEvidence_OutPlaneAloneIsNotTheClaim(t *testing.T) {
	// P6's spool claim is about the coordinator's STEER, which is an IN-plane
	// file. A harp appearing only on the out plane would mean the child sent
	// something carrying it while the steer itself never landed as a file — the
	// substrate did not do the thing this cell is measuring.
	root := t.TempDir()
	p6WriteSpoolFile(t, root, "out", "0009.msg.md", "kind: message\n---\nswift-amber-falcon")
	c, err := p6ReadSpoolCensus(root, "swift-amber-falcon")
	require.NoError(t, err)
	require.Empty(t, c.HarpIn)
	require.Len(t, c.HarpOut, 1)

	require.Error(t, p6AssertSpoolEvidence(p6TestVerdict(), c, "swift-amber-falcon"))
}

func TestP6AssertSpoolEvidence_GreenOnAnInPlaneFileCarryingTheHarp(t *testing.T) {
	root := t.TempDir()
	p6WriteSpoolFile(t, root, "in", "0001.msg.md", "kind: message\nto: swift-amber-falcon\n---\nswift-amber-falcon")
	c, err := p6ReadSpoolCensus(root, "swift-amber-falcon")
	require.NoError(t, err)
	require.NoError(t, p6AssertSpoolEvidence(p6TestVerdict(), c, "swift-amber-falcon"))
}

func TestP6AssertSpoolEvidence_EmptyHarpIsRefused(t *testing.T) {
	root := t.TempDir()
	p6WriteSpoolFile(t, root, "in", "0001.msg.md", "anything")
	c, err := p6ReadSpoolCensus(root, "")
	require.NoError(t, err)
	require.Error(t, p6AssertSpoolEvidence(p6TestVerdict(), c, ""),
		"scanning a spool for the empty string matches every file that exists — the assertion would pass on unrelated traffic")
}

func TestP6RefuseEmptyMarker(t *testing.T) {
	require.NoError(t, p6RefuseEmptyMarker("claude-code", "P6-WAKE-MARKER-CLAUDE-CODE-9f2c"))
	for _, empty := range []string{"", "  "} {
		require.Error(t, p6RefuseEmptyMarker("claude-code", empty))
	}
}

func TestP6Mint_IsLedgeredAndUniquePerCell(t *testing.T) {
	// The mint is what makes the steer fixture-private; the ledger is what makes
	// PX able to tell a leak from a collision. Both properties are asserted here
	// against the SAME ledger the cells use, because a probe minting outside it
	// would be invisible to the leak scanner.
	a, err := probeHarps.Mint(p6Cell("claude-code", "host", "none"))
	require.NoError(t, err)
	b, err := probeHarps.Mint(p6Cell("codex", "host", "none"))
	require.NoError(t, err)
	require.NotEqual(t, a, b, "two cells sharing a harp would make the leak scanner report a collision as contamination")

	again, err := probeHarps.Mint(p6Cell("claude-code", "host", "none"))
	require.NoError(t, err)
	require.Equal(t, a, again, "the fixture step and the assertion step are separate steps and must agree on the value")

	require.NotEmpty(t, strings.TrimSpace(a))
	snap := probeHarps.Snapshot()
	require.Equal(t, a, snap[p6Cell("claude-code", "host", "none")], "the cell must be findable in the ledger PX scans, or a scan there would be measuring the wrong thing")
}
