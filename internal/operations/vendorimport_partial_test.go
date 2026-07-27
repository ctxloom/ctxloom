package operations

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/transcript"
)

// partialFailAdapter writes n real entries via rec.Record and then fails —
// standing in for a vendor Convert that dies partway through a large
// transcript (a bad byte on line 500 of 1000, a read error after some
// progress), which is exactly what a lazily-created transcript.Recorder
// (recorder.go's NewRecorder doc) cannot distinguish from "a complete,
// legitimately captured transcript" once the fact of failure is thrown away.
type partialFailAdapter struct{ n int }

func (a partialFailAdapter) Convert(_ context.Context, rec transcript.Recorder, _ string) error {
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

// U145-F02: Convert failing partway through a transcript still leaves
// whatever it already wrote on disk (transcript.Recorder creates its file on
// the first SUCCESSFUL Record — recorder.go's NewRecorder doc — and every
// Record before the failure succeeded). hasCanonicalTranscript's guard is
// presence-only, so a LATER call for the same harp saw that partial file and
// treated the harp as "already captured" forever — masking the original
// failure permanently and never giving a fixed parser (or a corrected vendor
// file) a chance to produce a complete import.
func TestConvertVendorTranscript_FailurePartwayDoesNotPermanentlyMaskAsCaptured(t *testing.T) {
	testsupport.Isolate(t)
	harp := "convert-partial-failure-harp"

	orig := vendorImportRegistry["codex"]
	vendorImportRegistry["codex"] = vendorImportEntry{
		adapter: partialFailAdapter{n: 3},
		locate:  orig.locate,
	}
	defer func() { vendorImportRegistry["codex"] = orig }()

	e := sessions.Entry{HarpName: harp, Backend: "codex", TranscriptPath: codexFixturePath}

	converted, err := ConvertVendorTranscript(context.Background(), e)
	assert.True(t, converted, "Convert was genuinely attempted")
	require.Error(t, err, "a partial failure must surface as an error the first time")

	assert.False(t, hasCanonicalTranscript(harp),
		"a failed import must not leave a canonical file that a later retry mistakes for a complete, already-captured transcript")

	// A second call must genuinely retry, not silently no-op as "already
	// captured" — confirm the same partial-then-fail signature reproduces
	// (not "converted=false, nil" as a stale-guard no-op would give).
	converted2, err2 := ConvertVendorTranscript(context.Background(), e)
	assert.True(t, converted2, "a prior failed attempt must not permanently block retry")
	assert.Error(t, err2)
}
