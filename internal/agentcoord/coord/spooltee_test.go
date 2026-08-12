package coord

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/agentcoord/spool"
	"github.com/ctxloom/ctxloom/internal/paths"
)

// Tests for the mailbox's SHADOW TEE (spooltee.go): mail additionally written
// as spool files, reads untouched.
//
// Every test here redirects HOME first. The spool resolves through
// paths.HarpPersistDir, which resolves against $HOME, so a test that forgot
// would write its fixtures into the developer's real session store and pass —
// the residue only surfacing later as a spool full of "child-harp-1".

// teeHome redirects HOME to a fresh temp dir and returns it. Every spool path
// in these tests hangs off it.
func teeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

// newTeeCoordinator is newTestCoordinator with the shadow tee ON. It is a
// separate constructor rather than a parameter on the shared one because the
// tee's defining property is that it is OFF everywhere else: every other test
// in this package is, by construction, also a test that the disabled tee
// changes nothing.
func newTeeCoordinator(t *testing.T, sp Spawner) *Coordinator {
	t.Helper()
	c, err := New(Options{
		ProjectDir: t.TempDir(),
		StateDir:   t.TempDir(),
		Spawner:    sp,
		SpoolTee:   true,
	})
	require.NoError(t, err, "new tee coordinator")
	require.NoError(t, c.Serve(), "serve tee coordinator")
	t.Cleanup(c.Close)
	return c
}

// assertSpoolParity asserts that dir holds EXACTLY one spool file per mailbox
// message in want, matched by the mailbox id, with every mapped field equal.
//
// The empty-source guard is the point of the helper. Sweeping a directory that
// was never written and comparing it to an expectation nobody populated is two
// empty sets agreeing, which is the shape this project's characteristic bug
// (exit 0, success message, zero bytes) wears when a test is written to pass.
func assertSpoolParity(t *testing.T, harp string, dir spool.Dir, want []Message) {
	t.Helper()
	require.NotEmpty(t, want, "parity over an EMPTY expected set proves nothing: two empty sets always match")

	res, err := spool.Sweep(spool.NewHomeMapper(), harp, dir)
	require.NoError(t, err, "sweeping %s's %s", harp, dir)
	require.NoError(t, res.ProblemErr(), "the tee must never write a file the sweep cannot read")
	require.Len(t, res.Entries, len(want), "one spool file per mailbox delivery, no more and no fewer")

	byOrigin := make(map[string]*spool.Message, len(res.Entries))
	for _, e := range res.Entries {
		require.NotEmpty(t, e.Message.OriginID,
			"a teed file with no origin_id cannot be matched to its mailbox twin (%s)", e.Ref)
		byOrigin[e.Message.OriginID] = e.Message
	}
	require.Len(t, byOrigin, len(res.Entries), "two files claiming one mailbox id means the tee duplicated a delivery")

	for _, w := range want {
		require.NotEmpty(t, w.ID, "the expectation itself must carry the mailbox id it is matched by")
		got, ok := byOrigin[w.ID]
		require.True(t, ok, "mailbox message %s (kind %q) has no spool twin", w.ID, w.Kind)

		wantKind, err := SpoolKindForMail(w.Kind)
		require.NoError(t, err)
		assert.Equal(t, wantKind, got.Kind, "kind of %s", w.ID)
		assert.Equal(t, w.Body, got.Body, "body of %s", w.ID)
		assert.Equal(t, w.InReplyTo, got.InReplyTo, "in_reply_to of %s", w.ID)
		assert.Equal(t, w.From, got.FromHarp, "from_harp of %s", w.ID)
		assert.Equal(t, w.To, got.To, "to of %s", w.ID)
		assertStructuredParity(t, w.ID, w.Structured, got.Structured)
	}
}

// assertStructuredParity compares a mailbox message's raw-JSON companion with
// the frontmatter mapping it became.
//
// Both sides are normalised through JSON before comparing because they arrive
// through different decoders: JSON gives every number as float64, YAML gives
// whole numbers as int, and a raw reflect.DeepEqual would call an identical
// payload unequal. The guard against comparing two empty payloads is here for
// the same reason it is in the caller.
func assertStructuredParity(t *testing.T, msgID string, want json.RawMessage, got map[string]any) {
	t.Helper()
	if len(want) == 0 {
		assert.Empty(t, got, "message %s carried no structured payload, so its twin must carry none", msgID)
		return
	}
	var wantObj map[string]any
	require.NoError(t, json.Unmarshal(want, &wantObj), "the expectation's own structured payload must be a JSON object")
	require.NotEmpty(t, wantObj, "an EMPTY structured expectation proves nothing about the mapping")
	require.NotEmpty(t, got, "message %s carried a structured payload its twin lost entirely", msgID)
	assert.Equal(t, wantObj, jsonNormalise(t, got), "structured payload of %s", msgID)
}

func jsonNormalise(t *testing.T, in map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(in)
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(raw, &out))
	return out
}

// spoolEntries sweeps a spool directory, treating "the directory does not
// exist" as "no messages" rather than as an error.
//
// The distinction matters for the failure tests: a tee that could not build
// its writer never created the directory, and the assertion those tests make
// is about the ABSENCE of files, which a sweep error would mask as a different
// failure entirely.
func spoolEntries(t *testing.T, harp string, dir spool.Dir) []spool.Entry {
	t.Helper()
	path, err := spool.DirPath(spool.NewHomeMapper(), harp, dir)
	require.NoError(t, err)
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		return nil
	}
	res, err := spool.Sweep(spool.NewHomeMapper(), harp, dir)
	require.NoError(t, err)
	require.NoError(t, res.ProblemErr())
	return res.Entries
}

// awaitEngineText waits for the i-th scripted child engine to have been driven
// with a turn containing want.
//
// Delivery is asserted AT THE ENGINE rather than by draining the mailbox
// because for a coordinator-driven child the coordinator's own driver takes
// the mail (takeNextMail) and turns it into a turn: a test that drained the
// mailbox instead would be racing its own subject and would report "no
// message" for a message that was delivered perfectly.
func awaitEngineText(t *testing.T, sp *fakeSpawner, i int, want string) {
	t.Helper()
	require.NotEmpty(t, want, "waiting for an EMPTY text would be satisfied by any turn at all")
	require.Eventually(t, func() bool {
		e := sp.engine(i)
		if e == nil {
			return false
		}
		for _, got := range e.recordedTexts() {
			if strings.Contains(got, want) {
				return true
			}
		}
		return false
	}, conformanceWait, 10*time.Millisecond, "the child engine was never driven with %q", want)
}

// awaitRunnerHome waits for the migrated path's runner half to exist. AgentRun
// returns once the run is ENQUEUED; the runner is spawned and dials home
// afterwards, so reaching for it immediately finds nothing.
func awaitRunnerHome(t *testing.T, c *Coordinator, sp *fakeSpawner, harp string) *Home {
	t.Helper()
	require.Eventually(t, func() bool {
		return sp.engineHome(0) != nil && rosterState(c, harp) == StateIdle
	}, conformanceWait, 10*time.Millisecond, "the migrated child's runner never came up")
	home := sp.engineHome(0)
	require.NotNil(t, home)
	return home
}

// spoolDirsUnder lists every directory named "spool" anywhere below root — the
// evidence for "the disabled tee touched nothing".
func spoolDirsUnder(t *testing.T, root string) []string {
	t.Helper()
	var found []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// A vanished temp entry is not evidence either way; a real
			// failure is, so it is returned rather than swallowed.
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() && d.Name() == spool.SpoolDirName {
			found = append(found, path)
		}
		return nil
	})
	require.NoError(t, err, "walking %s", root)
	return found
}

// TestSpoolTee_DisabledWritesNothingAndRingsNothing pins INERTNESS, which is
// the only property that makes shipping this to production defensible: with
// the flag off a full two-way exchange must leave the filesystem exactly as it
// found it and ring no doorbell at all.
//
// It asserts the ABSENCE of the spool directory, not merely an empty one. A
// tee that created its directories eagerly and wrote nothing would pass a
// "no files" check while already having changed what an operator sees on disk
// — and a lazily-created directory is precisely what a later refactor would
// reintroduce without noticing.
func TestSpoolTee_DisabledWritesNothingAndRingsNothing(t *testing.T) {
	resetStrictness(t)
	home := teeHome(t)
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass"}}, nil)
	c := newTestCoordinator(t, sp, nil)
	require.False(t, c.SpoolTeeEnabled(), "the tee must be off unless a project asks for it")

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "go", "", "")
	require.NoError(t, err)
	child := Identity{Harp: out.Harp, RunID: out.RunID, Depth: 1}

	// Both directions, with correlation and a structured payload — everything
	// the enabled tee mirrors.
	ownerMsgID, _, _, err := c.peerSend(ownerIdentity(), out.Harp, KindMessage, "an instruction", nil, "")
	require.NoError(t, err)
	require.NotEmpty(t, ownerMsgID)
	_, _, _, err = c.peerSend(child, ParentAddress, KindResult, "a finding", nil, ownerMsgID)
	require.NoError(t, err)

	// The mail itself is unaffected: it still reaches the child's engine as a
	// turn, and the parent still receives the child's reply.
	awaitEngineText(t, sp, 0, "an instruction")
	require.NotEmpty(t, recvBody(t, c, "a finding", conformanceWait),
		"the disabled tee must not disturb ordinary delivery")

	assert.Empty(t, spoolDirsUnder(t, home),
		"the disabled tee must create no spool directory anywhere under HOME")
	assert.Equal(t, SpoolTeeStats{}, c.SpoolTeeStats(), "nothing written, nothing failed")
	assert.Equal(t, SpoolDoorbellStats{}, c.SpoolDoorbellStats(),
		"a doorbell rung with no channel would COUNT as dropped; the disabled tee must not ring at all")
}

// TestSpoolTee_CoordinatorMailHasOneSpoolTwinEach is the coordinator-side
// parity assertion: every mail the coordinator commits to a child appears
// exactly once in that child's in/, with kind, correlation and payload intact.
func TestSpoolTee_CoordinatorMailHasOneSpoolTwinEach(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass"}}, nil)
	c := newTeeCoordinator(t, sp)
	require.True(t, c.SpoolTeeEnabled())

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "go", "", "")
	require.NoError(t, err)
	owner := ownerIdentity()

	structured := json.RawMessage(`{"decision":"allow","tool":"Bash"}`)
	var want []Message

	// An UNKINDED message: the most common kind on the wire (Inject queues
	// one on every human injection) and the one with no frontmatter spelling
	// of its own, so it is first rather than an afterthought.
	id, _, _, err := c.peerSend(owner, out.Harp, KindUnset, "no kind at all", nil, "")
	require.NoError(t, err)
	want = append(want, Message{ID: id, From: owner.Harp, To: out.Harp, Kind: KindUnset, Body: "no kind at all"})

	id, _, _, err = c.peerSend(owner, out.Harp, KindMessage, "an instruction", nil, "")
	require.NoError(t, err)
	want = append(want, Message{ID: id, From: owner.Harp, To: out.Harp, Kind: KindMessage, Body: "an instruction"})

	// Correlated AND carrying a structured companion — the escalation
	// ladder's shape, which is where a dropped in_reply_to or a stripped
	// payload would actually cost something.
	replyTo := want[1].ID
	id, _, _, err = c.peerSend(owner, out.Harp, KindQuestion, "and this?", structured, replyTo)
	require.NoError(t, err)
	want = append(want, Message{
		ID: id, From: owner.Harp, To: out.Harp, Kind: KindQuestion,
		Body: "and this?", Structured: structured, InReplyTo: replyTo,
	})

	assertSpoolParity(t, out.Harp, spool.DirIn, want)
	assert.Equal(t, uint64(len(want)), c.SpoolTeeStats().Written)
	assert.Zero(t, c.SpoolTeeStats().Failed)
}

// TestSpoolTee_MailToTheOwnerIsNotTeed pins the tee's SCOPE. The session
// owner's mailbox is drained in this process by AgentRecv; there is no runner
// and no spool on the other end of it, so a file written for it would sit in a
// directory nothing ever sweeps — an accumulating, invisible leak that every
// "did the tee write something" check would report as success.
func TestSpoolTee_MailToTheOwnerIsNotTeed(t *testing.T) {
	resetStrictness(t)
	home := teeHome(t)
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass"}}, nil)
	c := newTeeCoordinator(t, sp)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "go", "", "")
	require.NoError(t, err)
	child := Identity{Harp: out.Harp, RunID: out.RunID, Depth: 1}

	_, _, _, err = c.peerSend(child, ParentAddress, KindResult, "a finding", nil, "")
	require.NoError(t, err)
	require.NotEmpty(t, recvBody(t, c, "a finding", conformanceWait), "the parent still receives it")

	ownerSpool, err := paths.HarpPersistDir(ownerIdentity().Harp)
	require.NoError(t, err)
	assert.NotContains(t, spoolDirsUnder(t, home), filepath.Join(ownerSpool, spool.SpoolDirName),
		"the coordinator's own in-process mailbox has no spool and must not be given one")
}

// TestSpoolTee_RunnerSendHasOneSpoolTwinEach is the runner-side parity
// assertion, and it rides the REAL runner half: a live Home over the
// coordinator's own listeners, whose tee posture it read out of the per-spawn
// env stamp rather than being handed by this test. It therefore also proves
// the flag reaches the second of its two sites.
func TestSpoolTee_RunnerSendHasOneSpoolTwinEach(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := newFakeSpawner(map[string]fakeAgent{
		"worker": {perm: "bypass", viaStartRun: true},
	}, nil)
	sp.engineCaps = RunnerCapabilities(true)
	c := newTeeCoordinator(t, sp)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "go", "", "")
	require.NoError(t, err)

	home := awaitRunnerHome(t, c, sp, out.Harp)
	require.True(t, home.SpoolTeeEnabled(),
		"the runner must have learned the tee posture from the coordinator's per-spawn env stamp")

	// The coordinator's doorbell handler: the runner's ring must arrive here,
	// validated, with the ref of the file it just wrote.
	rings := make(chan spool.Ref, 8)
	c.SetSpoolDoorbellHandler(func(_ string, ref spool.Ref) { rings <- ref })

	structured, err := structpb.NewStruct(map[string]any{"kind": KindResult, "confidence": "high"})
	require.NoError(t, err)
	resp, err := home.Request(context.Background(), &agentcoordpb.AgentRequest{
		Kind: &agentcoordpb.AgentRequest_PeerSend{PeerSend: &agentcoordpb.PeerSendRequest{
			ToRole:     ParentAddress,
			Text:       "a finding",
			Structured: structured,
		}},
	})
	require.NoError(t, err)
	require.EqualValues(t, 0, resp.GetStatus().GetCode(), "the send must be accepted: %s", resp.GetStatus().GetMessage())
	msgID := resp.GetPeerSend().GetMessageId()
	require.NotEmpty(t, msgID)

	structuredJSON, err := structured.MarshalJSON()
	require.NoError(t, err)
	assertSpoolParity(t, out.Harp, spool.DirOut, []Message{{
		ID: msgID, From: out.Harp, To: ParentAddress, Kind: KindResult,
		Body: "a finding", Structured: structuredJSON,
	}})
	assert.Equal(t, uint64(1), home.SpoolTeeStats().Written)
	assert.Zero(t, home.SpoolTeeStats().Failed)

	select {
	case ref := <-rings:
		assert.Equal(t, out.Harp, ref.Harp)
		assert.Equal(t, spool.DirOut, ref.Dir)
		res, err := spool.Sweep(spool.NewHomeMapper(), out.Harp, spool.DirOut)
		require.NoError(t, err)
		require.Len(t, res.Entries, 1)
		assert.Equal(t, res.Entries[0].Ref.Name, ref.Name,
			"the doorbell must name the file the tee actually wrote")
	case <-time.After(3 * time.Second):
		t.Fatal("the runner's tee wrote a file but never rang the coordinator")
	}
}

// TestSpoolTee_RefusedSendLeavesNoFile pins that out/ records what the agent
// actually SENT. A file for a message the coordinator refused would have no
// mailbox twin at all — a permanent, unexplainable divergence in exactly the
// data the soak exists to compare.
func TestSpoolTee_RefusedSendLeavesNoFile(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := newFakeSpawner(map[string]fakeAgent{
		"worker": {perm: "bypass", viaStartRun: true},
	}, nil)
	sp.engineCaps = RunnerCapabilities(true)
	c := newTeeCoordinator(t, sp)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "go", "", "")
	require.NoError(t, err)
	home := awaitRunnerHome(t, c, sp, out.Harp)
	require.True(t, home.SpoolTeeEnabled())

	// A child addressing a sibling is hub-and-spoke's refusal (ErrPeerRouting).
	resp, err := home.Request(context.Background(), &agentcoordpb.AgentRequest{
		Kind: &agentcoordpb.AgentRequest_PeerSend{PeerSend: &agentcoordpb.PeerSendRequest{
			ToAgentId: "some-sibling-harp",
			Text:      "psst",
		}},
	})
	require.NoError(t, err)
	require.NotEqualValues(t, 0, resp.GetStatus().GetCode(), "the send must have been refused")

	assert.Empty(t, spoolEntries(t, out.Harp, spool.DirOut),
		"a refused send is not a sent message and must leave no file")
	assert.Zero(t, home.SpoolTeeStats().Written)
}

// TestSpoolTee_WriteFailureStillDelivers is the load-bearing safety property:
// the mailbox is the system of record, so a tee that cannot write must warn,
// count, and get out of the way. If this ever inverts, enabling the flag turns
// a full disk into lost mail.
func TestSpoolTee_WriteFailureStillDelivers(t *testing.T) {
	resetStrictness(t)
	home := teeHome(t)
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass"}}, nil)
	c := newTeeCoordinator(t, sp)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "go", "", "")
	require.NoError(t, err)

	// Block the spool by making the session's persist directory a FILE. The
	// writer's MkdirAll then fails with ENOTDIR — deterministically, and for
	// root as well, which a chmod-based block would not.
	sessionDir := filepath.Join(home, ".ctxloom", "sessions", out.Harp)
	require.NoError(t, os.MkdirAll(sessionDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, paths.PersistDirName), []byte("not a directory"), 0o600))

	msgID, _, _, err := c.peerSend(ownerIdentity(), out.Harp, KindMessage, "still has to arrive", nil, "")
	require.NoError(t, err, "a tee failure must NEVER fail the mailbox delivery it shadows")
	require.NotEmpty(t, msgID)
	awaitEngineText(t, sp, 0, "still has to arrive")

	stats := c.SpoolTeeStats()
	assert.Equal(t, uint64(1), stats.Failed, "the divergence must be counted, not just logged")
	assert.Zero(t, stats.Written)
}

// TestSpoolTee_UnmappableKindFailsLoudlyAndDelivers pins the other tee-failure
// class: a kind the mapping table does not know is refused at the projection
// rather than written verbatim as a file no reader routes.
func TestSpoolTee_UnmappableKindFailsLoudlyAndDelivers(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass"}}, nil)
	c := newTeeCoordinator(t, sp)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "go", "", "")
	require.NoError(t, err)

	// queueMailPayloadID is below the sender-vocabulary guard, which is where
	// a kind the coordinator itself minted would arrive from.
	msgID, _, err := c.queueMail(ownerIdentity().Harp, out.Harp, "invented_kind", "still has to arrive")
	require.NoError(t, err, "an unmappable kind must not fail the delivery")
	require.NotEmpty(t, msgID)
	awaitEngineText(t, sp, 0, "still has to arrive")

	stats := c.SpoolTeeStats()
	assert.Equal(t, uint64(1), stats.Failed)
	assert.Zero(t, stats.Written)

	assert.Empty(t, spoolEntries(t, out.Harp, spool.DirIn),
		"an unmappable message must leave NO file rather than a mis-kinded one")
}

// TestSpoolTee_NonObjectStructuredRoundTripsUnderTheWrapper pins the payload
// half of the same rule, in the shape the CUTOVER forced.
//
// While the spool was a shadow, a payload that is not a JSON object could be
// refused: the file was dropped and counted and the mailbox still carried the
// message. Once the file IS the message that refusal becomes the message being
// lost, so a non-object payload now travels as its own JSON TEXT under a
// marker key. What this pins is that the wrap is BYTE-FAITHFUL — the exact
// bytes back out, not a YAML re-rendering of them, which is where a big
// integer or a numeric-looking string would quietly change.
func TestSpoolTee_NonObjectStructuredRoundTripsUnderTheWrapper(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass"}}, nil)
	c := newTeeCoordinator(t, sp)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "go", "", "")
	require.NoError(t, err)

	// A bare array, a value whose YAML round trip would lose precision, and a
	// string that YAML would happily hand back as a number.
	payloads := []string{
		`["not","an","object"]`,
		`12345678901234567890123`,
		`"0640"`,
	}
	for _, raw := range payloads {
		_, _, err = c.queueMailPayload(ownerIdentity().Harp, out.Harp, KindMessage, "body "+raw,
			json.RawMessage(raw), "")
		require.NoError(t, err, "queueing a message whose payload is %s", raw)
	}

	assert.Zero(t, c.SpoolTeeStats().Failed, "a non-object payload is carried, not refused")
	entries := spoolEntries(t, out.Harp, spool.DirIn)
	require.Len(t, entries, len(payloads), "one file per message, payload and all")

	got := make([]string, 0, len(entries))
	for _, e := range entries {
		back, err := mailStructured(e.Message.Structured)
		require.NoError(t, err, "reading %s back", e.Ref)
		got = append(got, string(back))
	}
	assert.Equal(t, payloads, got, "every non-object payload must come back byte for byte")
}

// TestSpoolTee_KindMappingIsExhaustive is the completeness gate: every kind
// that can ride the mailbox has a frontmatter spelling, the mapping round
// trips, and a kind nobody mapped is an ERROR rather than a silent
// pass-through.
func TestSpoolTee_KindMappingIsExhaustive(t *testing.T) {
	kinds := MailKinds()
	require.NotEmpty(t, kinds, "the exhaustiveness authority itself must not be empty")
	require.Contains(t, kinds, KindUnset, "the unkinded message is the most common one on the wire")

	seen := make(map[string]string, len(kinds))
	for _, k := range kinds {
		spoolKind, err := SpoolKindForMail(k)
		require.NoError(t, err, "mail kind %q has no frontmatter spelling", k)
		require.NotEmpty(t, spoolKind, "a frontmatter kind may never be empty: spool.Writer refuses one")

		back, err := MailKindForSpool(spoolKind)
		require.NoError(t, err, "frontmatter kind %q does not map back", spoolKind)
		assert.Equal(t, k, back, "the mapping must round trip for %q", k)

		if prev, dup := seen[spoolKind]; dup {
			t.Fatalf("mail kinds %q and %q share the frontmatter spelling %q", prev, k, spoolKind)
		}
		seen[spoolKind] = k
	}

	assert.NotEqual(t, KindMessage, seen[SpoolKindUnkinded],
		"the unkinded message must keep a spelling of its own: it renders no provenance header, a %q does", KindMessage)

	// A synthetic new kind must FAIL, not travel unmapped.
	_, err := SpoolKindForMail("a_kind_nobody_mapped")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no frontmatter representation")
	_, err = MailKindForSpool("a_frontmatter_kind_nobody_mapped")
	require.Error(t, err)
}

// TestSpoolTee_EveryProtoMessageKindIsAccountedFor guards the OTHER authority.
// The mailbox vocabulary is strings, but the wire's MessageKind enum is where a
// new kind is most likely to be added first, and a kind added there and never
// spelled in mailkind.go would ride the mailbox unmapped without failing the
// test above. Every enum value must therefore either map or be named here as
// one that does not ride the mailbox at all.
func TestSpoolTee_EveryProtoMessageKindIsAccountedFor(t *testing.T) {
	// Kinds that travel as control PeerMessages (enginehost_control.go), never
	// through the mailbox queue the tee shadows.
	notMailborne := map[string]bool{
		"steer":        true,
		"user_control": true,
	}
	checked := 0
	for value, name := range agentcoordpb.MessageKind_name {
		if agentcoordpb.MessageKind(value) == agentcoordpb.MessageKind_MESSAGE_KIND_UNSPECIFIED {
			continue
		}
		// The enum's names are the mailbox's strings, upper-cased and
		// prefixed — MESSAGE_KIND_APPROVAL_REQUEST is "approval_request".
		spelling := strings.ToLower(strings.TrimPrefix(name, "MESSAGE_KIND_"))
		checked++
		if notMailborne[spelling] {
			_, err := SpoolKindForMail(spelling)
			require.Error(t, err, "%s is listed as never riding the mailbox, so it must NOT be in the mapping table", name)
			continue
		}
		_, err := SpoolKindForMail(spelling)
		require.NoError(t, err,
			"wire kind %s (mailbox spelling %q) has no frontmatter mapping; add it to mailKindToSpool, or to notMailborne if it never rides the mailbox",
			name, spelling)
	}
	require.NotZero(t, checked, "the enum authority itself must not be empty")
}
