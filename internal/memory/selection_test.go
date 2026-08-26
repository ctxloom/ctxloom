package memory

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"testing"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// TestRenderToolArgs_ElidesCodeKeepsIdentity pins both directions in one
// render: the generated-code payload must be gone and the arguments that
// identify the call must survive. Asserting only the absence would be
// satisfied by a renderer that emitted nothing at all.
func TestRenderToolArgs_ElidesCodeKeepsIdentity(t *testing.T) {
	in := []byte(`{"file_path":"/x/y.go","old_string":"OLD_CODE_BODY","new_string":"NEW_CODE_BODY"}`)

	got := renderToolArgs(in)

	if strings.Contains(got, "NEW_CODE_BODY") || strings.Contains(got, "OLD_CODE_BODY") {
		t.Fatalf("generated-code payload survived rendering: %s", got)
	}
	if !strings.Contains(got, "/x/y.go") {
		t.Fatalf("identifying argument was lost: %s", got)
	}
	if !strings.Contains(got, "elided") {
		t.Fatalf("elision left no marker, so a reader cannot tell content was removed: %s", got)
	}
}

// TestRenderToolArgs_KeepsUnrecoverableArgs pins the other half of the policy:
// a shell command and a subagent brief cannot be looked up anywhere, so they
// must pass through whole.
func TestRenderToolArgs_KeepsUnrecoverableArgs(t *testing.T) {
	in := []byte(`{"command":"just test-acceptance","prompt":"SUBAGENT_BRIEF_TEXT"}`)

	got := renderToolArgs(in)

	if !strings.Contains(got, "just test-acceptance") {
		t.Fatalf("issued command was dropped: %s", got)
	}
	if !strings.Contains(got, "SUBAGENT_BRIEF_TEXT") {
		t.Fatalf("subagent brief was dropped and is recoverable from nowhere: %s", got)
	}
}

// TestRenderToolArgs_UnparseableInputSurvives pins that undecodable arguments
// are passed through rather than silently rendering as empty -- the project's
// characteristic failure is a step that "succeeds" having written nothing.
func TestRenderToolArgs_UnparseableInputSurvives(t *testing.T) {
	got := renderToolArgs([]byte(`not json at all`))
	if got != `not json at all` {
		t.Fatalf("unparseable arguments were not passed through: %q", got)
	}
}

// TestSessionToText_ElidesEditPayload proves the elision is reached through
// the real render path, not just callable in isolation.
func TestSessionToText_ElidesEditPayload(t *testing.T) {
	c := &Compactor{config: CompactionConfig{}}
	text, _ := c.sessionToText(&agent.Session{
		Entries: []agent.SessionEntry{
			{Type: agent.EntryTypeToolUse, ToolName: "Edit",
				ToolInput: []byte(`{"file_path":"/a/b.go","new_string":"GENERATED_CODE_MARKER"}`)},
		},
	})
	if strings.Contains(text, "GENERATED_CODE_MARKER") {
		t.Fatalf("generated code reached the distillation text: %s", text)
	}
	if !strings.Contains(text, "/a/b.go") {
		t.Fatalf("edited path lost from distillation text: %s", text)
	}
}

// TestRenderToolArgs_QuestionsKeepTextDropOptions pins that a question put to
// the user survives while the answer menu does not. The chosen answer is not
// in the arguments at all -- it comes back in the tool result -- so keeping
// the options here preserves only roads not taken.
func TestRenderToolArgs_QuestionsKeepTextDropOptions(t *testing.T) {
	in := []byte(`{"questions":[{"question":"THE_QUESTION_ASKED","header":"h",` +
		`"options":[{"label":"ROAD_NOT_TAKEN","description":"UNCHOSEN_DETAIL"}]}]}`)

	got := renderToolArgs(in)

	if !strings.Contains(got, "THE_QUESTION_ASKED") {
		t.Fatalf("the question asked was lost: %s", got)
	}
	if strings.Contains(got, "ROAD_NOT_TAKEN") || strings.Contains(got, "UNCHOSEN_DETAIL") {
		t.Fatalf("unchosen answer options survived: %s", got)
	}
}

// bigBody is comfortably over agent.DefaultToolReflectBytes so a test states
// its intent ("this is a large result") rather than a magic length.
func bigBody() string { return strings.Repeat("z", agent.DefaultToolReflectBytes*2) }

// TestSelectForDistill_RepairsOnlyUnreflectedLargeResults pins the trigger from
// all three sides: a large result the agent commented on needs no repair, an
// identical one it ignored does, and a SMALL ignored one does not. Asserting
// only the positive case would be satisfied by queueing every result for an
// LLM call, which is the expensive failure.
func TestSelectForDistill_RepairsOnlyUnreflectedLargeResults(t *testing.T) {
	call := agent.SessionEntry{Type: agent.EntryTypeToolUse, ToolName: "Bash",
		ToolInput: []byte(`{"command":"go build ./..."}`)}

	reflected := selectForDistill([]agent.SessionEntry{
		call,
		{Type: agent.EntryTypeToolResult, ToolName: "Bash", ToolOutput: bigBody()},
		{Type: agent.EntryTypeAssistant, Content: "The build failed on a link error."},
	})
	if len(reflected.Repairs) != 0 {
		t.Fatalf("queued a repair for a result the agent already commented on: %+v", reflected.Repairs)
	}

	ignored := selectForDistill([]agent.SessionEntry{
		call,
		{Type: agent.EntryTypeToolResult, ToolName: "Bash", ToolOutput: bigBody()},
		call,
	})
	if len(ignored.Repairs) != 1 {
		t.Fatalf("want 1 repair for an uncommented large result, got %d", len(ignored.Repairs))
	}
	if ignored.Repairs[0].Body != bigBody() {
		t.Fatal("repair carries a truncated body; recovery needs the whole result")
	}
	if !strings.Contains(ignored.Repairs[0].Intent, "go build") {
		t.Fatalf("repair lost the intent it must reason from: %q", ignored.Repairs[0].Intent)
	}
	if got := ignored.Entries[ignored.Repairs[0].Index]; got.Type != agent.EntryTypeToolResult {
		t.Fatalf("repair Index does not address its own entry: %+v", got)
	}

	small := selectForDistill([]agent.SessionEntry{
		{Type: agent.EntryTypeToolUse, ToolName: "Write", ToolInput: []byte(`{"file_path":"/a"}`)},
		{Type: agent.EntryTypeToolResult, ToolName: "Write", ToolOutput: "ok"},
		{Type: agent.EntryTypeToolUse, ToolName: "Write", ToolInput: []byte(`{"file_path":"/b"}`)},
	})
	if len(small.Repairs) != 0 {
		t.Fatalf("queued a repair for a small result: %+v", small.Repairs)
	}
}

// TestReflectedAfter_ParallelResultsShareOneReflection pins that a run of
// results from parallel tool calls is satisfied by the single message that
// follows them. Treating each as unreflected would queue an LLM call per
// result for a batch the agent did comment on.
func TestReflectedAfter_ParallelResultsShareOneReflection(t *testing.T) {
	entries := []agent.SessionEntry{
		{Type: agent.EntryTypeToolResult, ToolName: "Bash", ToolOutput: bigBody()},
		{Type: agent.EntryTypeToolResult, ToolName: "Bash", ToolOutput: bigBody()},
		{Type: agent.EntryTypeAssistant, Content: "Both probes agree."},
	}
	for i := range []int{0, 1} {
		if !reflectedAfter(entries, i) {
			t.Fatalf("result %d in a parallel run read as unreflected", i)
		}
	}
}

// TestRenderResultBody_ExcerptOnlyWhenUnreflected pins the deterministic
// fallback: a commented-on result keeps only its shape, an ignored large one
// keeps a bounded excerpt because nothing else recorded what it said.
func TestRenderResultBody_ExcerptOnlyWhenUnreflected(t *testing.T) {
	body := "FINDABLE_MARKER" + strings.Repeat("q", agent.DefaultToolReflectBytes)

	withFinding := renderResultBody(body, true)
	if strings.Contains(withFinding, "FINDABLE_MARKER") {
		t.Fatalf("kept body despite a stated finding: %q", withFinding)
	}

	without := renderResultBody(body, false)
	if !strings.Contains(without, "FINDABLE_MARKER") {
		t.Fatalf("dropped the only record of an uncommented result: %q", without)
	}
	if len(without) > resultExcerptBytes*2 {
		t.Fatalf("excerpt is unbounded at %d bytes", len(without))
	}
}

// unreflectedSelection builds a Selection holding exactly one large tool result
// the agent never commented on -- the only shape that reaches repairResults.
func unreflectedSelection(t *testing.T) Selection {
	t.Helper()
	sel := selectForDistill([]agent.SessionEntry{
		{Type: agent.EntryTypeToolUse, ToolName: "Bash", ToolInput: []byte(`{"command":"go vet ./..."}`)},
		{Type: agent.EntryTypeToolResult, ToolName: "Bash", ToolOutput: bigBody()},
		{Type: agent.EntryTypeToolUse, ToolName: "Bash", ToolInput: []byte(`{"command":"ls"}`)},
	})
	if len(sel.Repairs) != 1 {
		t.Fatalf("fixture did not produce exactly one repair: %d", len(sel.Repairs))
	}
	return sel
}

// TestRepairResults_WritesRecoveredFindingIntoTheEntry pins the whole point of
// the repair pass: the recovered sentence must reach the entry that gets
// rendered. Asserting only the returned count would pass with a pass that
// called the model and threw the answer away -- this project's characteristic
// silent no-op.
func TestRepairResults_WritesRecoveredFindingIntoTheEntry(t *testing.T) {
	const finding = "go vet reported no findings across the module."
	mock := &pb.MockClient{
		RunFunc: func(ctx context.Context, req *pb.RunStart, stdout, stderr io.Writer) (int32, error) {
			_, _ = stdout.Write([]byte(finding))
			return 0, nil
		},
	}
	c := &Compactor{config: CompactionConfig{LLM: "test-plugin"}, clientFactory: pb.MockClientFactory(mock)}
	sel := unreflectedSelection(t)

	got := c.repairResults(context.Background(), sel)

	if got != 1 {
		t.Fatalf("repairResults reported %d recovered, want 1", got)
	}
	body := sel.Entries[sel.Repairs[0].Index].ToolOutput
	if !strings.Contains(body, finding) {
		t.Fatalf("recovered finding never reached the entry: %q", body)
	}
	if strings.Contains(body, bigBody()) {
		t.Fatal("entry still carries the raw body; the finding was meant to replace the excerpt")
	}
}

// TestRepairResults_FailedRecoveryLeavesTheExcerpt pins the degrade path. A
// failed LLM call must leave the deterministic rendering standing, never an
// empty result -- the excerpt is the whole reason tier 3 exists.
func TestRepairResults_FailedRecoveryLeavesTheExcerpt(t *testing.T) {
	mock := &pb.MockClient{
		RunFunc: func(ctx context.Context, req *pb.RunStart, stdout, stderr io.Writer) (int32, error) {
			return 0, errors.New("plugin unreachable")
		},
	}
	c := &Compactor{config: CompactionConfig{LLM: "test-plugin"}, clientFactory: pb.MockClientFactory(mock)}
	sel := unreflectedSelection(t)
	before := sel.Entries[sel.Repairs[0].Index].ToolOutput

	if got := c.repairResults(context.Background(), sel); got != 0 {
		t.Fatalf("repairResults counted %d recovered despite a failing client", got)
	}
	after := sel.Entries[sel.Repairs[0].Index].ToolOutput
	if after != before {
		t.Fatalf("failed recovery mutated the entry:\n before %q\n after  %q", before, after)
	}
	if strings.TrimSpace(after) == "" {
		t.Fatal("failed recovery left an empty body")
	}
}

// TestRepairResults_NoConclusionLeavesTheExcerpt pins the prompt's own escape
// hatch. A model with nothing to say must not overwrite the excerpt with a
// confident nothing.
func TestRepairResults_NoConclusionLeavesTheExcerpt(t *testing.T) {
	mock := &pb.MockClient{
		RunFunc: func(ctx context.Context, req *pb.RunStart, stdout, stderr io.Writer) (int32, error) {
			_, _ = stdout.Write([]byte("  " + noConclusionAvailable + "\n"))
			return 0, nil
		},
	}
	c := &Compactor{config: CompactionConfig{LLM: "test-plugin"}, clientFactory: pb.MockClientFactory(mock)}
	sel := unreflectedSelection(t)
	before := sel.Entries[sel.Repairs[0].Index].ToolOutput

	if got := c.repairResults(context.Background(), sel); got != 0 {
		t.Fatalf("counted a recovery for %q", noConclusionAvailable)
	}
	if after := sel.Entries[sel.Repairs[0].Index].ToolOutput; after != before {
		t.Fatalf("escape-hatch answer overwrote the excerpt: %q", after)
	}
}

// TestRepairResults_NoCandidatesMakesNoCall pins that a session where every
// result was commented on costs nothing. Spawning a plugin subprocess per
// result would make reflection more expensive than not reflecting.
func TestRepairResults_NoCandidatesMakesNoCall(t *testing.T) {
	mock := &pb.MockClient{
		RunFunc: func(ctx context.Context, req *pb.RunStart, stdout, stderr io.Writer) (int32, error) {
			t.Fatal("repairResults called the model with no repair candidates")
			return 0, nil
		},
	}
	c := &Compactor{config: CompactionConfig{LLM: "test-plugin"}, clientFactory: pb.MockClientFactory(mock)}

	sel := selectForDistill([]agent.SessionEntry{
		{Type: agent.EntryTypeToolUse, ToolName: "Bash", ToolInput: []byte(`{"command":"go vet ./..."}`)},
		{Type: agent.EntryTypeToolResult, ToolName: "Bash", ToolOutput: bigBody()},
		{Type: agent.EntryTypeAssistant, Content: "Vet was clean."},
	})

	if got := c.repairResults(context.Background(), sel); got != 0 {
		t.Fatalf("recovered %d findings when none were missing", got)
	}
	if mock.RunCalls != 0 {
		t.Fatalf("made %d model calls with no candidates", mock.RunCalls)
	}
}

// TestRepairResults_ConcurrentRecoveriesEachLandInTheirOwnEntry drives ENOUGH
// candidates to exceed distillConcurrency, so the pass actually runs in
// parallel. The single-candidate tests above cannot exercise that: they spawn
// one goroutine, so a race detector run over them proves nothing.
//
// Each recovery is keyed to its own entry, which is the property concurrency
// can break -- a shared index, or a captured loop variable, would land every
// finding on one entry and leave the rest holding excerpts.
func TestRepairResults_ConcurrentRecoveriesEachLandInTheirOwnEntry(t *testing.T) {
	const candidates = distillConcurrency * 3

	var entries []agent.SessionEntry
	for i := 0; i < candidates; i++ {
		cmd := fmt.Sprintf(`{"command":"go test ./pkg%d/..."}`, i)
		entries = append(entries,
			agent.SessionEntry{Type: agent.EntryTypeToolUse, ToolName: "Bash", ToolInput: []byte(cmd)},
			agent.SessionEntry{Type: agent.EntryTypeToolResult, ToolName: "Bash",
				ToolOutput: fmt.Sprintf("PKG%d_MARKER%s", i, bigBody())},
			agent.SessionEntry{Type: agent.EntryTypeToolUse, ToolName: "Bash", ToolInput: []byte(`{"command":"true"}`)},
		)
	}
	sel := selectForDistill(entries)
	if len(sel.Repairs) != candidates {
		t.Fatalf("want %d repair candidates, got %d", candidates, len(sel.Repairs))
	}

	// Echo the pkg number back out of the prompt so each answer is distinct and
	// traceable to the call that produced it.
	pkgRE := regexp.MustCompile(`go test \./pkg(\d+)/`)
	mock := &pb.MockClient{
		RunFunc: func(ctx context.Context, req *pb.RunStart, stdout, stderr io.Writer) (int32, error) {
			m := pkgRE.FindStringSubmatch(req.GetPrompt().GetContent())
			if m == nil {
				return 0, errors.New("prompt did not carry the tool call")
			}
			_, _ = fmt.Fprintf(stdout, "FINDING_FOR_PKG%s", m[1])
			return 0, nil
		},
	}
	c := &Compactor{config: CompactionConfig{LLM: "test-plugin"}, clientFactory: pb.MockClientFactory(mock)}

	if got := c.repairResults(context.Background(), sel); got != candidates {
		t.Fatalf("recovered %d of %d", got, candidates)
	}
	for i, r := range sel.Repairs {
		want := fmt.Sprintf("FINDING_FOR_PKG%d", i)
		if body := sel.Entries[r.Index].ToolOutput; !strings.Contains(body, want) {
			t.Fatalf("repair %d holds the wrong finding (%q missing): %q", i, want, body)
		}
	}
}
