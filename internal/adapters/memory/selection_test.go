package memory

import (
	"github.com/spf13/afero"
	"strconv"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/testsupport"
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
	c := &Compactor{fs: afero.NewOsFs(), config: CompactionConfig{}}
	text, _ := c.sessionToText(&agent.Session{
		Entries: []agent.SessionEntry{
			{Type: agent.EntryTypeToolUse, ToolName: "Edit",
				ToolInput: []byte(`{"file_path":"/a/b.go","new_string":"GENERATED_CODE_MARKER"}`)},
		},
	})
	if strings.Contains(text, "GENERATED_CODE_MARKER") {
		t.Fatalf("generated code reached the compaction text: %s", text)
	}
	if !strings.Contains(text, "/a/b.go") {
		t.Fatalf("edited path lost from compaction text: %s", text)
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

// TestSelectForCompact_EveryResultReducesToItsShape pins that a successful
// tool result keeps only its shape whatever its size and whether or not the
// agent commented on it: no excerpt of the body reaches the distiller.
func TestSelectForCompact_EveryResultReducesToItsShape(t *testing.T) {
	body := "BODY_MARKER" + strings.Repeat("z", 64<<10)
	sel := selectForCompact([]agent.SessionEntry{
		{Type: agent.EntryTypeToolUse, ToolName: "Bash", ToolInput: []byte(`{"command":"go build ./..."}`)},
		{Type: agent.EntryTypeToolResult, ToolName: "Bash", ToolOutput: body},
		{Type: agent.EntryTypeToolUse, ToolName: "Bash", ToolInput: []byte(`{"command":"true"}`)},
	})
	if got := sel.Entries[1].ToolOutput; got != resultShape(body) {
		t.Fatalf("a large uncommented result kept more than its shape: %q", truncateForSummary(got))
	}
}

// TestSessionToText_ErrorBodyIsNotTruncatedAtTheDisplayCap pins the one result
// kind kept for its CONTENT. Errors were being cut at the ordinary 500-byte
// display cap, which removes exactly the stack trace a fix has to answer to --
// 9 of 26 real error results were affected, for about 750 bytes per session.
func TestSessionToText_ErrorBodyIsNotTruncatedAtTheDisplayCap(t *testing.T) {
	// Longer than the display cap, shorter than the error bound.
	tail := "TRAILING_ROOT_CAUSE"
	body := strings.Repeat("e", 900) + tail

	c := &Compactor{fs: afero.NewOsFs(), config: CompactionConfig{}}
	text, _ := c.sessionToText(&agent.Session{Entries: []agent.SessionEntry{
		{Type: agent.EntryTypeToolResult, ToolName: "Bash", ToolOutput: body, IsError: true},
	}})

	if !strings.Contains(text, tail) {
		t.Fatalf("error body was truncated before its root cause: %q", text)
	}
	if !strings.Contains(text, "[ERROR]") {
		t.Fatalf("error marker lost: %q", text)
	}
}

// TestRenderErrorBody_BoundsAPathologicalError pins the other side: an error
// far beyond the bound is still cut, so one runaway result cannot dominate.
func TestRenderErrorBody_BoundsAPathologicalError(t *testing.T) {
	got := renderErrorBody(strings.Repeat("x", maxErrorBytes*3))
	if len(got) > maxErrorBytes {
		t.Fatalf("unbounded error body: %d bytes", len(got))
	}
}

// TestResultShape_DistinguishesEmptyFromDiscarded pins that a result which
// produced NOTHING and one whose body was reduced are different strings.
// Rendering both as an absence makes "the command returned no matches" and
// "the output was dropped" indistinguishable in the essence -- and the first
// is a finding.
func TestResultShape_DistinguishesEmptyFromDiscarded(t *testing.T) {
	empty := resultShape("")
	if !strings.Contains(empty, "no output") {
		t.Fatalf("an empty result does not say so: %q", empty)
	}

	body := resultShape("line one\nline two\nline three")
	if strings.Contains(body, "no output") {
		t.Fatalf("a non-empty result reported as empty: %q", body)
	}
	if !strings.Contains(body, "3 lines") {
		t.Fatalf("line count wrong or missing: %q", body)
	}
	if !strings.Contains(body, strconv.Itoa(len("line one\nline two\nline three"))) {
		t.Fatalf("byte count missing: %q", body)
	}
}

// TestCompactPrompt_CarriesTheConfiguredBudget pins that the budget reaches the
// MODEL. It is injected at runtime rather than written into the prompt file,
// so a config value that never made it into the prompt would leave compaction
// with no size instruction at all -- and nothing else would notice.
func TestCompactPrompt_CarriesTheConfiguredBudget(t *testing.T) {
	c := &Compactor{fs: afero.NewOsFs(), config: CompactionConfig{EssenceMaxChars: 7331}}

	got, err := c.compactPrompt()
	if err != nil {
		t.Fatalf("compactPrompt: %v", err)
	}

	if !strings.Contains(got, "7331") {
		t.Fatalf("configured budget absent from the prompt: %q", truncateForSummary(got))
	}
	if !strings.Contains(got, "Open Items") {
		t.Fatal("budget injection dropped the compaction instruction itself")
	}
}

// TestNewCompactor_ClampsBudgetToTheHardCeiling pins that a target above
// MaxEssenceChars is refused rather than honoured. Handing the model a target
// larger than the ceiling Compact enforces recreates exactly the contradiction
// the absolute budget was introduced to remove.
func TestNewCompactor_ClampsBudgetToTheHardCeiling(t *testing.T) {
	testsupport.Isolate(t)
	recordOutputDir(t, "clamp-under-test")
	c, err := NewCompactor(afero.NewOsFs(), CompactionConfig{
		Source:          &mockSource{},
		EssenceMaxChars: MaxEssenceChars * 4,
		OutputDir:       t.TempDir(),
		HarpName:        "clamp-under-test",
	})
	if err != nil {
		t.Fatalf("NewCompactor: %v", err)
	}
	if c.config.EssenceMaxChars != MaxEssenceChars {
		t.Fatalf("budget %d was not clamped to the %d ceiling",
			c.config.EssenceMaxChars, MaxEssenceChars)
	}
}

// TestNewCompactor_DefaultsTheBudget pins that an unset budget takes the shared
// default rather than zero -- a zero would render "under 0 characters".
func TestNewCompactor_DefaultsTheBudget(t *testing.T) {
	testsupport.Isolate(t)
	recordOutputDir(t, "default-under-test")
	c, err := NewCompactor(afero.NewOsFs(), CompactionConfig{
		Source:    &mockSource{},
		OutputDir: t.TempDir(),
		HarpName:  "default-under-test",
	})
	if err != nil {
		t.Fatalf("NewCompactor: %v", err)
	}
	if c.config.EssenceMaxChars != agent.DefaultEssenceChars {
		t.Fatalf("unset budget = %d, want %d", c.config.EssenceMaxChars, agent.DefaultEssenceChars)
	}
}
