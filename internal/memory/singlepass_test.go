package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/shared/tokens"
)

// entryBlock renders one "## " block the way appendEntryText does, so these
// fixtures split the same way a real rendered transcript does.
func entryBlock(header, body string) string {
	return "## " + header + "\n" + body + "\n\n"
}

// TestFitToBudget_UnderBudgetIsUntouched: the common case (measured at ~82% of
// real sessions) must hand the model the transcript verbatim. A reducer that
// "helpfully" rewrote short transcripts would lose detail for no reason.
func TestFitToBudget_UnderBudgetIsUntouched(t *testing.T) {
	text := entryBlock("User", "hello") + entryBlock("Assistant", "world")

	got, reduced := fitToBudget(text, SinglePassInputTokens)

	assert.False(t, reduced, "a small transcript must not be reported as reduced")
	assert.Equal(t, text, got, "a transcript under budget must be handed over byte-for-byte")
}

// TestFitToBudget_KeepsTheTailAndCompressesTheHead is the core claim: the
// session's RECENT content is its active working memory and must survive, while
// the oldest content is what gets compressed. A uniform truncation, or one that
// cut from the end, would pass a naive size check and destroy exactly the part
// a resume needs.
func TestFitToBudget_KeepsTheTailAndCompressesTheHead(t *testing.T) {
	// Distinct, findable markers so the assertion is about WHICH content
	// survived, not merely how many bytes did.
	var b strings.Builder
	for i := range 40 {
		b.WriteString(entryBlock(
			fmt.Sprintf("Assistant MARKER%02d", i),
			strings.Repeat("padding text ", 400),
		))
	}
	newest := "NEWEST-CONTENT-VERBATIM"
	b.WriteString(entryBlock("User", newest))
	text := b.String()

	budgetTokens := 2000
	got, reduced := fitToBudget(text, budgetTokens)

	require.True(t, reduced, "fixture must exceed the budget or this test proves nothing")
	require.Less(t, len(got), len(text), "reduction must actually shrink the text")
	assert.LessOrEqual(t, len(got), tokens.Budget(budgetTokens),
		"the whole point of the budget is that the result fits inside it")

	assert.Contains(t, got, newest,
		"the most recent entry is active working memory and must survive intact")
	assert.NotContains(t, got, "MARKER00",
		"the oldest entry must be compressed away first, not kept at the tail's expense")

	// The head is dropped, not silently vanished: a reader must be able to see
	// that earlier content existed.
	assert.Contains(t, got, "earlier entries omitted",
		"dropped content must be reported, never silently disappear")
}

// TestFitToBudget_NeverExceedsTheBudget stresses the half-of-remaining rule
// across a range of sizes. The bound is the property the whole design rests on:
// if it can be exceeded, the single-pass call can overflow the model window and
// the reduction bought nothing.
func TestFitToBudget_NeverExceedsTheBudget(t *testing.T) {
	var b strings.Builder
	for i := range 200 {
		b.WriteString(entryBlock(fmt.Sprintf("Tool Result: T%03d", i), strings.Repeat("x", 900)))
	}
	text := b.String()

	for _, budget := range []int{50, 200, 1000, 5000} {
		got, reduced := fitToBudget(text, budget)
		require.True(t, reduced, "budget %d: fixture must exceed it", budget)
		assert.LessOrEqual(t, len(got), tokens.Budget(budget),
			"budget %d: result must fit the byte budget", budget)
	}
}

// TestFitToBudget_CutsOnRuneBoundaries: a mid-rune split makes the text invalid
// UTF-8, which fails proto3 string marshaling and turns the whole distillation
// into a failure. The reducer must never produce one.
func TestFitToBudget_CutsOnRuneBoundaries(t *testing.T) {
	var b strings.Builder
	for i := range 30 {
		b.WriteString(entryBlock(fmt.Sprintf("Assistant %02d", i), strings.Repeat("日本語テキスト", 200)))
	}

	got, reduced := fitToBudget(b.String(), 500)

	require.True(t, reduced)
	assert.True(t, utf8ValidString(got), "reduction must never split a rune")
}

func utf8ValidString(s string) bool {
	for _, r := range s {
		if r == '�' {
			return false
		}
	}
	return true
}

// TestCollectArtifacts_PinsPathsWithTheToolsThatTouchedThem. Paths are pinned
// deterministically precisely so the model never has to reproduce one; this
// pins that they are read out of the tool-call JSON, deduplicated, and carry
// every distinct tool that touched them.
func TestCollectArtifacts_PinsPathsWithTheToolsThatTouchedThem(t *testing.T) {
	toolUse := func(name, path string) agent.SessionEntry {
		raw, err := json.Marshal(map[string]string{"file_path": path, "old_string": "ignored"})
		require.NoError(t, err)
		return agent.SessionEntry{Type: agent.EntryTypeToolUse, ToolName: name, ToolInput: raw}
	}

	entries := []agent.SessionEntry{
		toolUse("Read", "/repo/a.go"),
		{Type: agent.EntryTypeAssistant, Content: "thinking about it"},
		toolUse("Edit", "/repo/a.go"),
		toolUse("Read", "/repo/a.go"),
		toolUse("Write", "/repo/b.go"),
	}

	got, omitted := collectArtifacts(entries)

	assert.Zero(t, omitted)
	require.Len(t, got, 2, "distinct paths only — a re-read is not a second file")
	assert.Equal(t, "/repo/a.go", got[0].Path, "first-seen order records the work, not a sort")
	assert.Equal(t, []string{"Read", "Edit"}, got[0].Tools,
		"a read and an edit of one path are different facts and both must survive")
	assert.Equal(t, "/repo/b.go", got[1].Path)
}

// TestCollectArtifacts_CapsTheListAndSaysSo. The section lands OUTSIDE the
// MaxEssenceChars check, so an unbounded appendix would reintroduce exactly the
// unbounded-payload failure that check exists to prevent.
func TestCollectArtifacts_CapsTheListAndSaysSo(t *testing.T) {
	var entries []agent.SessionEntry
	for i := range maxArtifacts + 25 {
		raw, err := json.Marshal(map[string]string{"file_path": fmt.Sprintf("/repo/f%04d.go", i)})
		require.NoError(t, err)
		entries = append(entries, agent.SessionEntry{
			Type: agent.EntryTypeToolUse, ToolName: "Read", ToolInput: raw,
		})
	}

	got, omitted := collectArtifacts(entries)

	require.Len(t, got, maxArtifacts, "the cap must actually bind")
	assert.Equal(t, 25, omitted, "what the cap dropped must be counted, not silently lost")

	rendered := RenderArtifacts(got, omitted)
	assert.Contains(t, rendered, "25 further path",
		"a truncated index must never render as though it were complete")
}

// TestRenderArtifacts_EmptyRendersNothing keeps the appendix from emitting a
// bare heading over an empty list.
func TestRenderArtifacts_EmptyRendersNothing(t *testing.T) {
	assert.Empty(t, RenderArtifacts(nil, 0))
	assert.Empty(t, RenderArtifacts([]Artifact{}, 0))
}

// TestAssembleBody_PinsArtifactsAndPlansAfterTheBody: both appendices are facts
// the transcript already carries exactly, re-attached after the LLM body so a
// language model never has a chance to paraphrase them.
func TestAssembleBody_PinsArtifactsAndPlansAfterTheBody(t *testing.T) {
	body := assembleBody("the summary", appendices{
		Plans:     []PlanBlock{{Index: 1, Label: "p", Content: "# Plan\n- step one"}},
		Artifacts: []Artifact{{Path: "/repo/a.go", Tools: []string{"Edit"}}},
	})

	assert.True(t, strings.HasPrefix(body, "the summary"), "the LLM body leads")
	assert.Contains(t, body, "/repo/a.go", "the pinned path must survive verbatim")
	assert.Contains(t, body, "step one", "the plan must survive verbatim")
	assert.Less(t, strings.Index(body, "/repo/a.go"), strings.Index(body, "step one"),
		"artifacts then plans, so the ordering is pinned rather than incidental")
}

// TestDistillPrompt_PromptDirOverridesTheEmbeddedPrompt: the flag exists so an
// evaluation harness can A/B a prompt variant without rebuilding.
func TestDistillPrompt_PromptDirOverridesTheEmbeddedPrompt(t *testing.T) {
	dir := t.TempDir()
	variant := "VARIANT PROMPT UNDER EVALUATION"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "session-distill.md"), []byte(variant), 0o644))

	c := &Compactor{config: CompactionConfig{PromptDir: dir, EssenceMaxChars: 4242}}
	got, err := c.distillPrompt()
	require.NoError(t, err)

	assert.Contains(t, got, variant, "the on-disk variant must be what reaches the model")
	assert.NotContains(t, got, "You are a session summarizer",
		"the embedded prompt must not leak in alongside the variant")
	assert.Contains(t, got, "4242", "the budget is still injected over the variant")
}

// TestDistillPrompt_MissingPromptFailsLoudly. Falling back to the embedded
// prompt would report a measurement attributed to the variant under test while
// actually measuring the built-in one — a wrong number that looks right.
func TestDistillPrompt_MissingPromptFailsLoudly(t *testing.T) {
	c := &Compactor{config: CompactionConfig{PromptDir: t.TempDir(), EssenceMaxChars: 1000}}

	_, err := c.distillPrompt()

	require.Error(t, err, "a missing prompt must fail, never silently use the embedded one")
	assert.Contains(t, err.Error(), "session-distill", "the error must name the prompt it wanted")
}

// TestCompact_DistillsInExactlyOneLLMCall is the whole point of the change: a
// transcript is summarized by ONE call that sees all of it, never by several
// whose partial summaries are then merged by a pass that cannot check them
// against the source.
func TestCompact_DistillsInExactlyOneLLMCall(t *testing.T) {
	testsupport.Isolate(t)

	// Comfortably larger than the old 8,000-token chunk size, so under the old
	// pipeline this fixture would have produced several map calls plus a reduce.
	big := strings.Repeat("decision content that must be summarized. ", 4000)
	mockBe := &mockBackend{history: &mockSessionHistory{
		currentSession: &agent.Session{
			ID: "one-call-session",
			Entries: []agent.SessionEntry{
				{Type: agent.EntryTypeUser, Content: big},
				{Type: agent.EntryTypeAssistant, Content: big},
				{Type: agent.EntryTypeUser, Content: big},
			},
		},
	}}

	var calls int
	mockClient := &pb.MockClient{
		RunFunc: func(ctx context.Context, req *pb.RunStart, stdout, stderr io.Writer) (int32, error) {
			calls++
			_, _ = stdout.Write([]byte("---\nsummary: one call\n---\n\n### Open Items\n- none\n"))
			return 0, nil
		},
	}

	compactor, err := NewCompactor(CompactionConfig{
		BackendOverride: mockBe,
		ClientFactory:   pb.MockClientFactory(mockClient),
		OutputDir:       t.TempDir(),
		HarpName:        "compactor-under-test",
	})
	require.NoError(t, err)

	result, err := compactor.Compact(context.Background())
	require.NoError(t, err)

	assert.Equal(t, 1, calls,
		"a session must be distilled by exactly one LLM call — more means chunking is back")
	assert.False(t, result.InputReduced,
		"this fixture fits the budget, so nothing should have been compressed away")

	data, err := os.ReadFile(result.DistilledPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), "one call", "the model's output must reach the essence")
}

// TestCompact_OversizedTranscriptStillOneCallAndReportsReduction: past the
// budget the transcript is REDUCED, not split. The report flag matters because
// the token counts alone cannot tell a caller that early detail was thinned.
func TestCompact_OversizedTranscriptStillOneCallAndReportsReduction(t *testing.T) {
	testsupport.Isolate(t)

	// Past SinglePassInputTokens once rendered.
	huge := strings.Repeat("y", tokens.Budget(SinglePassInputTokens))
	mockBe := &mockBackend{history: &mockSessionHistory{
		currentSession: &agent.Session{
			ID: "oversized-session",
			Entries: []agent.SessionEntry{
				{Type: agent.EntryTypeUser, Content: huge},
				{Type: agent.EntryTypeAssistant, Content: huge},
			},
		},
	}}

	var calls int
	var sawBytes int
	mockClient := &pb.MockClient{
		RunFunc: func(ctx context.Context, req *pb.RunStart, stdout, stderr io.Writer) (int32, error) {
			calls++
			sawBytes = len(req.Prompt.Content)
			_, _ = stdout.Write([]byte("---\nsummary: reduced\n---\n\n### Open Items\n- none\n"))
			return 0, nil
		},
	}

	compactor, err := NewCompactor(CompactionConfig{
		BackendOverride: mockBe,
		ClientFactory:   pb.MockClientFactory(mockClient),
		OutputDir:       t.TempDir(),
		HarpName:        "compactor-under-test",
	})
	require.NoError(t, err)

	result, err := compactor.Compact(context.Background())
	require.NoError(t, err)

	assert.Equal(t, 1, calls, "an oversized transcript is reduced, never split into several calls")
	assert.True(t, result.InputReduced, "the caller must be told early detail was compressed")
	require.NotZero(t, sawBytes, "the distiller never ran, so this proves nothing")
	assert.Less(t, sawBytes, len(huge)*2,
		"the prompt must carry the REDUCED transcript, not the whole thing")
}
