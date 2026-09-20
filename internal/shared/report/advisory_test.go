package report_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// TestAdvisory_RendersTheTextAndFatalToNothing: a preview's findings are
// advice — the text reaches the sink, and no finding is fatal.
func TestAdvisory_RendersTheTextAndFatalToNothing(t *testing.T) {
	var got report.Findings
	rep := report.To(report.Advisory(&got))

	rep.FailOncef(report.KindBundle, "run `ctxloom deps pull`", "failed to load bundle %q", "gone")
	rep.Warnf("plain warning")

	assert.Equal(t, []string{`failed to load bundle "gone"`, "plain warning"}, got.Texts(), "every text is forwarded")
	assert.Empty(t, got.Fatal(), "and none of it is fatal")
	assert.True(t, got[0].Once, "the once-ness of a finding is the renderer's and survives")
}

// TestAdvisory_NilSinkDiscards: an advisory over no sink is Discard, not a
// nil dereference.
func TestAdvisory_NilSinkDiscards(t *testing.T) {
	assert.NotPanics(t, func() { report.To(report.Advisory(nil)).Warnf("dropped") })
}
