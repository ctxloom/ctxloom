package coord

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/spool"
)

// NOTE on assertion style: Coordinator.Close runs in t.Cleanup and a
// require.* FailNow inside a coord test that holds a coordinator deadlocks it
// (mailbox_takefail_test.go). assert + return wherever a coordinator is held.

// overLong is a body past MaxInlineBodyBytes whose lines make the clean-cut
// boundary observable: every line ends in '\n', so a correct cut ends in one.
func overLong() string {
	var b strings.Builder
	for i := 0; b.Len() <= 3*MaxInlineBodyBytes; i++ {
		b.WriteString(strings.Repeat("evidence ", 7))
		b.WriteString("line\n")
	}
	return b.String()
}

// assertOverflowed checks one DELIVERED message against the full message
// that was sent: it is bounded, its body is a clean prefix plus the marker, the
// structured companion is not delivered inline, and the marker's artifact —
// fetched as the recipient — recovers the WHOLE message, structured included.
func assertOverflowed(t *testing.T, c *Coordinator, recipient Identity, holder, full string, sent json.RawMessage, delivered string, deliveredStructured json.RawMessage) bool {
	t.Helper()
	if !assertOverflowHead(t, full, delivered, deliveredStructured) {
		return false
	}
	agent, id := markerAddress(t, delivered)
	if !assert.Equal(t, holder, agent, "the marker names the holder") {
		return false
	}
	art, err := c.FetchArtifact(context.Background(), recipient, FetchRequest{Harp: agent, ArtifactID: id})
	if !assert.NoError(t, err, "the recipient must be entitled to the artifact the marker names") {
		return false
	}
	return assertOverflowArtifact(t, full, sent, id, art.Bytes)
}

// assertOverflowHead checks the delivered side: the marker is present, no
// structured companion travels inline, and the head before the marker line is
// a bounded, rune-clean prefix of the original.
func assertOverflowHead(t *testing.T, full, delivered string, deliveredStructured json.RawMessage) bool {
	t.Helper()
	if !assert.Contains(t, delivered, OverflowMarkerPhrase, "the delivered body must tell the reader to keep reading") ||
		!assert.Empty(t, deliveredStructured, "an overflowed message's structured companion travels in the artifact, not inline") {
		return false
	}
	i := strings.LastIndex(delivered, "\n[... "+OverflowMarkerPhrase)
	if !assert.GreaterOrEqual(t, i, 0, "the marker line follows the head") {
		return false
	}
	head := delivered[:i]
	return assert.LessOrEqual(t, len(head), MaxInlineBodyBytes, "the inline head is bounded") &&
		assert.True(t, strings.HasPrefix(full, head), "the head is a prefix of the original") &&
		assert.True(t, utf8.ValidString(head), "the cut never splits a rune")
}

// assertOverflowArtifact checks the artifact the marker names: its id carries
// the whole digest, and it recovers the WHOLE message — the body alone, or a
// JSON envelope with the structured companion.
func assertOverflowArtifact(t *testing.T, full string, sent json.RawMessage, id string, artBytes []byte) bool {
	t.Helper()
	sum := sha256.Sum256(artBytes)
	if !assert.True(t, strings.HasSuffix(id, hex.EncodeToString(sum[:])), "the id carries the artifact's WHOLE digest, not a prefix") {
		return false
	}
	if len(sent) == 0 {
		return assert.Equal(t, full, string(artBytes), "the artifact is the FULL body")
	}
	var env struct {
		Body       string          `json:"body"`
		Structured json.RawMessage `json:"structured"`
	}
	if !assert.NoError(t, json.Unmarshal(artBytes, &env), "a message with a structured companion overflows as a JSON envelope") {
		return false
	}
	return assert.Equal(t, full, env.Body) && assert.JSONEq(t, string(sent), string(env.Structured), "the structured companion is recoverable")
}

// markerAddress reads the (agent_id, artifact_id) a marker names — the reader's
// own view of it, so the test follows the marker rather than recomputing it.
func markerAddress(t *testing.T, delivered string) (agent, id string) {
	t.Helper()
	m := regexp.MustCompile(`agent_id "([^"]+)", artifact_id "([^"]+)"`).FindStringSubmatch(delivered)
	require.Len(t, m, 3, "the marker names agent_id and artifact_id")
	return m[1], m[2]
}

// bigStructured is a structured companion that alone exceeds the inline cap:
// the bypass the cap must not leave open.
func bigStructured() json.RawMessage {
	raw, _ := json.Marshal(map[string]string{"evidence": strings.Repeat("y", 2*MaxInlineBodyBytes)})
	return raw
}

func TestInlineHead_ExactlyAtCapIsUnchanged(t *testing.T) {
	body := strings.Repeat("x", MaxInlineBodyBytes)
	head, cut := inlineHead(body, MaxInlineBodyBytes)
	assert.False(t, cut)
	assert.Equal(t, body, head)
}

func TestInlineHead_CutsAtALineBoundary(t *testing.T) {
	head, cut := inlineHead(overLong(), MaxInlineBodyBytes)
	require.True(t, cut)
	assert.LessOrEqual(t, len(head), MaxInlineBodyBytes)
	assert.True(t, strings.HasSuffix(head, "\n"), "a body with lines is cut after a whole line")
}

func TestInlineHead_NeverSplitsARune(t *testing.T) {
	// Three-byte runes, no newline to cut at, and a cap not divisible by
	// three: a byte cut lands mid-rune.
	body := strings.Repeat("日", MaxInlineBodyBytes)
	head, cut := inlineHead(body, MaxInlineBodyBytes)
	require.True(t, cut)
	assert.True(t, utf8.ValidString(head))
	assert.LessOrEqual(t, len(head), MaxInlineBodyBytes)
	assert.Greater(t, len(head), MaxInlineBodyBytes-utf8.UTFMax, "backing off to a rune boundary costs at most one rune")
}

func TestBoundBody_AtCapStoresNothing(t *testing.T) {
	c := newTestCoordinator(t, newFakeSpawner(nil, nil), nil)
	body := strings.Repeat("x", MaxInlineBodyBytes)
	got, gotStructured, err := c.boundBody("holder", body, nil)
	assert.NoError(t, err)
	assert.Equal(t, body, got)
	assert.Empty(t, gotStructured)
	assert.Empty(t, c.Artifacts("holder"), "a body within the cap publishes no artifact")
}

// The structured companion counts toward the cap: body + structured exactly
// at it is delivered whole, one byte past it overflows.
func TestBoundBody_StructuredCountsTowardTheCap(t *testing.T) {
	c := newTestCoordinator(t, newFakeSpawner(nil, nil), nil)
	structured := json.RawMessage(`{"decision":"accept"}`)
	body := strings.Repeat("x", MaxInlineBodyBytes-len(structured))
	got, gotStructured, err := c.boundBody("holder", body, structured)
	if !assert.NoError(t, err) {
		return
	}
	assert.Equal(t, body, got, "exactly at the cap is unchanged")
	assert.JSONEq(t, string(structured), string(gotStructured))
	assert.Empty(t, c.Artifacts("holder"))

	got, gotStructured, err = c.boundBody("holder", body+"x", structured)
	if !assert.NoError(t, err) {
		return
	}
	assertOverflowed(t, c, Identity{Harp: "holder"}, "holder", body+"x", structured, got, gotStructured)
}

// A small body cannot smuggle a large structured companion past the cap.
func TestBoundBody_LargeStructuredOverflows(t *testing.T) {
	c := newTestCoordinator(t, newFakeSpawner(nil, nil), nil)
	got, gotStructured, err := c.boundBody("holder", "short", bigStructured())
	if !assert.NoError(t, err) {
		return
	}
	assertOverflowed(t, c, Identity{Harp: "holder"}, "holder", "short", bigStructured(), got, gotStructured)
}

func TestBoundBody_RefusesPastTheArtifactCeiling(t *testing.T) {
	c := newTestCoordinator(t, newFakeSpawner(nil, nil), nil)
	_, _, err := c.boundBody("holder", strings.Repeat("x", ArtifactUploadSizeCap+1), nil)
	assert.ErrorIs(t, err, ErrBodyTooLarge)
}

// ---- the artifact is create-once ----------------------------------------

// A manifest already filed under an overflow id is NEVER replaced by different
// content: the write fails loudly and the earlier manifest stands.
func TestBoundBody_NeverOverwritesAnExistingArtifact(t *testing.T) {
	c := newTestCoordinator(t, newFakeSpawner(nil, nil), nil)
	full := overLong()
	sum := sha256.Sum256([]byte(full))
	id := overflowArtifactID(hex.EncodeToString(sum[:]))
	other := sha256.Sum256([]byte("an earlier, different message"))
	if !assert.NoError(t, c.recordArtifact("holder", ArtifactProduced{ArtifactID: id, SHA256: other[:], UploadID: hex.EncodeToString(other[:])})) {
		return
	}
	before, _ := c.artifactRecord("holder", id)

	_, _, err := c.boundBody("holder", full, nil)
	assert.ErrorIs(t, err, errOverflowIDTaken, "a different body under a taken id is refused, not a new revision")
	after, _ := c.artifactRecord("holder", id)
	assert.Equal(t, before, after, "the earlier manifest is untouched")
}

// The same content twice is ONE artifact: the second send reuses the stored
// blob (never re-published over it) and the journaled manifest, unchanged.
func TestBoundBody_SameContentReusesTheArtifact(t *testing.T) {
	c := newTestCoordinator(t, newFakeSpawner(nil, nil), nil)
	full := overLong()
	first, _, err := c.boundBody("holder", full, nil)
	if !assert.NoError(t, err) {
		return
	}
	_, id := markerAddress(t, first)
	rec, ok := c.artifactRecord("holder", id)
	if !assert.True(t, ok) {
		return
	}
	blobBefore, err := os.Stat(c.artifacts.path(rec.SHA256))
	if !assert.NoError(t, err) {
		return
	}

	second, _, err := c.boundBody("holder", full, nil)
	if !assert.NoError(t, err) {
		return
	}
	assert.Equal(t, first, second)
	again, _ := c.artifactRecord("holder", id)
	assert.Equal(t, rec, again, "the manifest is not re-revised")
	assert.EqualValues(t, 1, again.Revision)
	assert.Len(t, c.Artifacts("holder"), 1)
	blobAfter, err := os.Stat(c.artifacts.path(rec.SHA256))
	if assert.NoError(t, err) {
		assert.True(t, os.SameFile(blobBefore, blobAfter), "the stored blob is not rewritten")
	}
	assertOverflowed(t, c, Identity{Harp: "holder"}, "holder", full, nil, second, nil)
}

// The same body to two recipients is filed under EACH recipient: neither
// overwrites the other, each can fetch its own, and neither can fetch the
// other's.
func TestSendOverflow_SameBodyToTwoChildren(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass"}}, nil)
	c := newTestCoordinator(t, sp, nil)
	var kids []Identity
	for range 2 {
		out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "work", "", "")
		if !assert.NoError(t, err) {
			return
		}
		kids = append(kids, Identity{Harp: out.Harp, RunID: out.RunID, Depth: 1})
	}
	full := overLong()
	var ids []string
	for _, k := range kids {
		if _, err := c.AgentSend(ownerIdentity(), k.Harp, KindMessage, full, nil, ""); !assert.NoError(t, err) {
			return
		}
		delivered := awaitChildOverflow(t, k.Harp)
		if delivered == "" {
			return
		}
		assertOverflowed(t, c, k, k.Harp, full, nil, delivered, nil)
		_, id := markerAddress(t, delivered)
		ids = append(ids, id)
		assert.Len(t, c.Artifacts(k.Harp), 1)
	}
	for i, k := range kids {
		other := kids[1-i]
		_, err := c.FetchArtifact(context.Background(), k, FetchRequest{Harp: other.Harp, ArtifactID: ids[1-i]})
		assert.ErrorIs(t, err, ErrForbidden, "a child may not read its sibling's overflow")
		art, err := c.FetchArtifact(context.Background(), ownerIdentity(), FetchRequest{Harp: k.Harp, ArtifactID: ids[i]})
		if assert.NoError(t, err, "the parent may read its child's") {
			assert.Equal(t, full, string(art.Bytes))
		}
	}
}

// ---- every route -----------------------------------------------------------

// companions is each route's structured variant: none, and one large enough
// on its own to overflow.
var companions = map[string]json.RawMessage{"body only": nil, "with structured": bigStructured()}

func awaitChildOverflow(t *testing.T, harp string) string {
	t.Helper()
	var delivered string
	assert.Eventually(t, func() bool {
		for _, dir := range []spool.Dir{spool.DirIn, spool.ClaimedDirName} {
			for _, e := range spoolEntries(t, harp, dir) {
				if strings.Contains(e.Message.Body, OverflowMarkerPhrase) {
					delivered = e.Message.Body
					assert.Empty(t, e.Message.Structured, "structured travels in the artifact")
					return true
				}
			}
		}
		return false
	}, conformanceWait, 10*time.Millisecond, "the overflowed message never reached %s's spool", harp)
	return delivered
}

func TestSendOverflow_OwnerToChild(t *testing.T) {
	for name, structured := range companions {
		t.Run(name, func(t *testing.T) {
			resetStrictness(t)
			sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass"}}, nil)
			c := newTestCoordinator(t, sp, nil)
			out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "work", "", "")
			if !assert.NoError(t, err) {
				return
			}
			child := Identity{Harp: out.Harp, RunID: out.RunID, Depth: 1}
			full := overLong()
			if _, err := c.AgentSend(ownerIdentity(), out.Harp, KindMessage, full, structured, ""); !assert.NoError(t, err) {
				return
			}
			if delivered := awaitChildOverflow(t, out.Harp); delivered != "" {
				assertOverflowed(t, c, child, out.Harp, full, structured, delivered, nil)
			}
		})
	}
}

func childAndOwnerInbox(t *testing.T) (*Coordinator, Identity) {
	t.Helper()
	resetStrictness(t)
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass"}}, nil)
	c := newTestCoordinator(t, sp, nil)
	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "work", "", "")
	require.NoError(t, err)
	return c, Identity{Harp: out.Harp, RunID: out.RunID, Depth: 1}
}

func ownerReceivesOverflow(t *testing.T, c *Coordinator, full string, structured json.RawMessage) {
	t.Helper()
	msgs, err := spoolMail(t, c, ownerIdentity().Harp, conformanceWait)
	if !assert.NoError(t, err) {
		return
	}
	for _, m := range msgs {
		if strings.Contains(m.Body, OverflowMarkerPhrase) {
			assertOverflowed(t, c, ownerIdentity(), ownerIdentity().Harp, full, structured, m.Body, m.Structured)
			return
		}
	}
	t.Errorf("no overflowed message reached the owner (got %d messages)", len(msgs))
}

func TestSendOverflow_ChildToParentThroughTheSendVerb(t *testing.T) {
	for name, structured := range companions {
		t.Run(name, func(t *testing.T) {
			c, child := childAndOwnerInbox(t)
			full := overLong()
			_, err := c.Send(context.Background(), child, SendRequest{To: ParentAddress, Kind: KindResult, Body: full, Structured: structured})
			if !assert.NoError(t, err, "an over-cap message is overflowed, not refused") {
				return
			}
			ownerReceivesOverflow(t, c, full, structured)
		})
	}
}

func TestSendOverflow_ChildToParentThroughTheSpool(t *testing.T) {
	for name, structured := range companions {
		t.Run(name, func(t *testing.T) {
			c, child := childAndOwnerInbox(t)
			full := overLong()
			var fields map[string]any
			if len(structured) > 0 {
				require.NoError(t, json.Unmarshal(structured, &fields))
			}
			c.routeSpoolOut(child.Harp, spool.Entry{Message: &spool.Message{V: 1, Kind: KindResult, To: ParentAddress, Body: full, Structured: fields}})
			ownerReceivesOverflow(t, c, full, structured)
		})
	}
}

// A FINAL report is queued to the parent as mail (notifyParentOfFinalReport),
// so the fold the agent_report description promises holds there too.
func TestSendOverflow_FinalReportNotice(t *testing.T) {
	c, child := childAndOwnerInbox(t)
	full := overLong()
	if err := c.Report(context.Background(), child, ReportRequest{Scope: "final", Body: full}); !assert.NoError(t, err) {
		return
	}
	ownerReceivesOverflow(t, c, full, nil)
}
