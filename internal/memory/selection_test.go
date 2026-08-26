package memory

import (
	"strings"
	"testing"

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
