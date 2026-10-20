package operations

import (
	"context"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/transcript"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// partialFailAdapter writes n real entries via rec.Record and then fails —
// standing in for a vendor Convert that dies partway through a large
// transcript (a bad byte on line 500 of 1000, a read error after some
// progress), which is exactly what a lazily-created transcript.Recorder
// (recorder.go's NewRecorder doc) cannot distinguish from "a complete,
// legitimately captured transcript" once the fact of failure is thrown away.
type partialFailAdapter struct{ n int }

func (a partialFailAdapter) Convert(_ context.Context, _ afero.Fs, rec transcript.Recorder, _ string) error {
	for i := 0; i < a.n; i++ {
		if err := rec.Record(agent.ChatEvent{Entry: &agent.SessionEntry{
			Type:    agent.EntryTypeUser,
			Content: "partial content",
		}}); err != nil {
			return err
		}
	}
	return assert.AnError
}

// A Convert failing partway through a harp's FIRST conversion has already
// recorded some lines (transcript.Recorder creates its file on the first
// SUCCESSFUL Record — recorder.go's NewRecorder doc). None of them may reach
// the canonical path: a partial file there reads back as a complete,
// captured transcript, masking the failure from every reader. The failure
// must surface, and a later call must genuinely retry it.
func TestRefreshVendorTranscript_FailurePartwayLeavesNoCanonicalTranscript(t *testing.T) {
	testsupport.Isolate(t)
	harp := "convert-partial-failure-harp"

	engine := registerReaderFixture(t, partialFailAdapter{n: 3})

	e := sessions.Entry{HarpName: harp, Backend: engine, TranscriptPath: claudeFixturePath, EngineVersion: stubEngineVersion}

	converted, err := RefreshVendorTranscript(context.Background(), safefs.New(), engines.Registry(), e)
	assert.True(t, converted, "Convert was genuinely attempted")
	require.Error(t, err, "a partial failure must surface as an error the first time")

	assert.Nil(t, canonicalBytes(t, harp),
		"a failed import must not leave a canonical file a reader mistakes for a complete transcript")

	// A second call genuinely retries: the same partial-then-fail signature
	// reproduces.
	converted2, err2 := RefreshVendorTranscript(context.Background(), safefs.New(), engines.Registry(), e)
	assert.True(t, converted2, "a prior failed attempt must not permanently block retry")
	assert.Error(t, err2)
}

// zeroLineAdapter succeeds (nil error) without ever calling rec.Record —
// standing in for a legitimately-empty-but-parseable vendor transcript
// (every line malformed-but-skippable, or a genuinely empty session):
// vendorreader.VendorAdapter's own contract treats this as a valid outcome, not
// a failure.
type zeroLineAdapter struct{}

func (zeroLineAdapter) Convert(_ context.Context, _ afero.Fs, _ transcript.Recorder, _ string) error {
	return nil
}

// converted reports bytes committed, not Convert succeeding: a Convert that
// records zero lines leaves no canonical file at all, and reporting
// "converted" for it would be a standing lie repeated on every call, since
// nothing on disk changes between them.
func TestRefreshVendorTranscript_ZeroLinesIsNotReportedAsConverted(t *testing.T) {
	testsupport.Isolate(t)
	harp := "convert-zero-lines-harp"

	engine := registerReaderFixture(t, zeroLineAdapter{})

	e := sessions.Entry{HarpName: harp, Backend: engine, TranscriptPath: claudeFixturePath, EngineVersion: stubEngineVersion}

	converted, err := RefreshVendorTranscript(context.Background(), safefs.New(), engines.Registry(), e)
	require.NoError(t, err)
	assert.False(t, converted,
		"a Convert that wrote zero canonical lines must not be reported as \"converted\" — nothing was actually delivered")
	assert.Nil(t, canonicalBytes(t, harp), "no canonical file should exist when nothing was recorded")
}
