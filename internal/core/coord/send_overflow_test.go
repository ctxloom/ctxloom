package coord

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// assertOverflowed checks one DELIVERED body against the full body that was
// sent: it is bounded, it is a clean prefix plus the marker, and the marker's
// artifact — fetched as the recipient — is the whole original.
func assertOverflowed(t *testing.T, c *Coordinator, recipient Identity, holder, full, delivered string) bool {
	t.Helper()
	sum := sha256.Sum256([]byte(full))
	id := overflowArtifactID(hex.EncodeToString(sum[:]))
	marker := overflowMarker(holder, id, len(full))
	if !assert.Contains(t, delivered, OverflowMarkerPhrase, "the delivered body must tell the reader to keep reading") {
		return false
	}
	if !assert.True(t, strings.HasSuffix(delivered, marker), "the marker names the holder and the artifact id") {
		return false
	}
	head := strings.TrimSuffix(delivered, marker)
	if !assert.LessOrEqual(t, len(head), MaxInlineBodyBytes, "the inline head is bounded") ||
		!assert.True(t, strings.HasPrefix(full, head), "the head is a prefix of the original") ||
		!assert.True(t, utf8.ValidString(head), "the cut never splits a rune") {
		return false
	}
	art, err := c.FetchArtifact(context.Background(), recipient, FetchRequest{Harp: holder, ArtifactID: id})
	if !assert.NoError(t, err, "the recipient must be entitled to the artifact the marker names") {
		return false
	}
	return assert.Equal(t, full, string(art.Bytes), "the artifact is the FULL body")
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
	got, err := c.boundBody("holder", body)
	assert.NoError(t, err)
	assert.Equal(t, body, got)
	assert.Empty(t, c.Artifacts("holder"), "a body within the cap publishes no artifact")
}

func TestBoundBody_RefusesPastTheArtifactCeiling(t *testing.T) {
	c := newTestCoordinator(t, newFakeSpawner(nil, nil), nil)
	_, err := c.boundBody("holder", strings.Repeat("x", ArtifactUploadSizeCap+1))
	assert.ErrorIs(t, err, ErrBodyTooLarge)
}

// Every route an agent-authored body takes to a recipient is bounded.

func TestSendOverflow_OwnerToChild(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass"}}, nil)
	c := newTestCoordinator(t, sp, nil)
	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "work", "", "")
	if !assert.NoError(t, err) {
		return
	}
	child := Identity{Harp: out.Harp, RunID: out.RunID, Depth: 1}
	full := overLong()
	if _, err := c.AgentSend(ownerIdentity(), out.Harp, KindMessage, full, nil, ""); !assert.NoError(t, err) {
		return
	}
	var delivered string
	ok := assert.Eventually(t, func() bool {
		for _, dir := range []spool.Dir{spool.DirIn, spool.ClaimedDirName, spool.DirInConsumed} {
			for _, e := range spoolEntries(t, out.Harp, dir) {
				if strings.Contains(e.Message.Body, OverflowMarkerPhrase) {
					delivered = e.Message.Body
					return true
				}
			}
		}
		return false
	}, conformanceWait, 10*time.Millisecond, "the overflowed message never reached the child's spool")
	if !ok {
		return
	}
	assertOverflowed(t, c, child, out.Harp, full, delivered)
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

func ownerReceivesOverflow(t *testing.T, c *Coordinator, full string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), conformanceWait)
	defer cancel()
	msgs, err := c.inbox.recv(ctx, ownerIdentity().Harp, conformanceWait)
	if !assert.NoError(t, err) {
		return
	}
	for _, m := range msgs {
		if strings.Contains(m.Body, OverflowMarkerPhrase) {
			assertOverflowed(t, c, ownerIdentity(), ownerIdentity().Harp, full, m.Body)
			return
		}
	}
	t.Errorf("no overflowed message reached the owner (got %d messages)", len(msgs))
}

func TestSendOverflow_ChildToParentThroughTheSendVerb(t *testing.T) {
	c, child := childAndOwnerInbox(t)
	full := overLong()
	_, err := c.Send(context.Background(), child, SendRequest{To: ParentAddress, Kind: KindResult, Body: full})
	if !assert.NoError(t, err, "an over-cap body is overflowed, not refused") {
		return
	}
	ownerReceivesOverflow(t, c, full)
}

func TestSendOverflow_ChildToParentThroughTheSpool(t *testing.T) {
	c, child := childAndOwnerInbox(t)
	full := overLong()
	c.routeSpoolOut(child.Harp, spool.Entry{Message: &spool.Message{V: 1, Kind: KindResult, To: ParentAddress, Body: full}})
	ownerReceivesOverflow(t, c, full)
}

func TestSendOverflow_AskReplyIsBounded(t *testing.T) {
	c, child := childAndOwnerInbox(t)
	ch := make(chan AskAnswer, 1)
	c.mu.Lock()
	if c.asks == nil {
		c.asks = map[string]*pendingAsk{}
	}
	c.asks["ask-1"] = &pendingAsk{targetHarp: child.Harp, kind: "question", ch: ch}
	c.mu.Unlock()
	full := overLong()
	if _, err := c.AgentSend(child, ParentAddress, KindResult, full, nil, "ask-1"); !assert.NoError(t, err) {
		return
	}
	select {
	case a := <-ch:
		assertOverflowed(t, c, ownerIdentity(), child.Harp, full, a.Text)
	default:
		t.Error("the ask was not answered")
	}
}
