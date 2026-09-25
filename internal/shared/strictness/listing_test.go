package strictness_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

const (
	fixA = "fix the first thing"
	fixB = "fix the second thing"
)

// Listing is the one findings block, so it must carry EVERY finding's
// remedy: a listing that drops any of them leaves the user to guess.
func TestListing_RendersEveryRemedy(t *testing.T) {
	found := report.Findings{
		{Kind: report.KindIsolation, Text: "first", Remedy: fixA},
		{Kind: report.KindSync, Text: "second"},
		{Kind: report.KindIsolation, Text: "third", Remedy: fixB},
	}
	got := strictness.Mode{}.Listing("header:", found)
	want := "header:" +
		"\n  - [isolation] first" + clifmt.FixLine("    ", fixA) +
		"\n  - [sync] second" +
		"\n  - [isolation] third" + clifmt.FixLine("    ", fixB)
	assert.Equal(t, want, got)
	assert.Empty(t, strictness.Mode{}.Listing("header:", nil), "no findings renders nothing")
}

func TestFindingsError_FixOnlyForASingleFinding(t *testing.T) {
	restore := clidiag.SetSink(new(strings.Builder))
	t.Cleanup(func() { restore(); strictness.Reset() })
	mode := strictness.Mode{Prog: "ctxloom"}

	strictness.Reset()
	mark := strictness.Checkpoint()
	strictness.Record(report.KindConfig, fixA, "one")
	err := mode.FindingsError(mark)
	require.Error(t, err)
	fix, ok := clifmt.RemedyOf(err)
	assert.True(t, ok)
	assert.Equal(t, fixA, fix)

	strictness.Record(report.KindConfig, fixB, "two")
	err = mode.FindingsError(mark)
	require.Error(t, err)
	_, ok = clifmt.RemedyOf(err)
	assert.False(t, ok, "one remedy field cannot stand for several findings")
	assert.Contains(t, err.Error(), clifmt.FixLine("    ", fixA))
	assert.Contains(t, err.Error(), clifmt.FixLine("    ", fixB))
}
