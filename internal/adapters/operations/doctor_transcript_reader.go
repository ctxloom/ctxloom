package operations

import (
	"context"
	"fmt"
	"strings"

	"github.com/ctxloom/ctxloom/internal/adapters/transcript/vendorreader"
	"github.com/ctxloom/ctxloom/internal/core/config"
)

// doctorTranscriptReaderMarker is the DOCTOR-CHECK-* vocabulary entry for
// version-scoped transcript readers — see doctorCheckTranscriptReaders for
// the refusal this exists to make debuggable.
const doctorTranscriptReaderMarker = "DOCTOR-CHECK-TRANSCRIPT-READER-v2"

// engineVersionProbe is the shape of backends.ProbeEngineVersion, injected so
// doctorCheckTranscriptReaders can be tested against chosen versions without
// a real engine binary installed — the same discipline
// doctorCheckGitIdentity's gitConfig parameter uses.
type engineVersionProbe func(ctx context.Context, engine string) (string, error)

// doctorCheckTranscriptReaders reports, per configured engine, the engine
// version this machine actually has, the transcript reader that version
// selects, and the version ranges ctxloom carries readers for.
//
// It exists for ONE user-visible moment: a session's transcript refuses to
// convert. Reading an engine's own transcript store is version-scoped —
// vendorreader.SelectAdapter picks the reader validated for the version a
// session RECORDED, and refuses outright rather than guessing when no reader
// claims it (see internal/adapters/transcript/vendorreader/version.go for why a
// fallback to the newest reader is the wrong answer). The refusal messages
// themselves already carry the diagnosis, but they only appear at the moment
// of failure, in a warning during `ctxloom run`'s exit or in a recover_session
// result — never anywhere a user can go and LOOK. This is that place.
//
// Three states, worded apart on purpose:
//
//   - a version was detected and a reader claims it: the ordinary state, and
//     it names the reader and the range so "which reader am I on" is
//     answerable before anything goes wrong.
//   - a version was detected and NO reader claims it: sessions this engine
//     writes from now on will refuse to convert. That is a real problem the
//     user can act on (report the version; it is what gets a reader written),
//     so it is this check's warn.
//   - the version could not be probed at all: reported, not warned. An engine
//     that is not installed is DOCTOR-CHECK-DEPS-a1's finding, and repeating
//     it here as a second warn teaches a reader to ignore both.
//
// It deliberately says NOTHING about any particular session on disk. What a
// session recorded is a fact about that session (sessions.Entry.EngineVersion,
// recorded at start precisely because a probe reports what is installed NOW
// and never what wrote those bytes) — this check reports the CURRENT machine,
// which is the other half a user needs and the only half a probe can honestly
// supply.
func doctorCheckTranscriptReaders(ctx context.Context, cfg *config.Config, probe engineVersionProbe) DoctorCheck {
	engines := doctorConfiguredEngines(cfg)
	var lines []string
	refused := false

	for _, engine := range engines {
		adapters, ok := VendorReaderAdaptersFor(engine)
		if !ok {
			// No vendor reader for this engine at all. Every REGISTERED
			// backend has one, so this is the config-names-an-unknown-engine
			// case: a stale or hand-edited config naming a backend this build
			// does not carry. Silence is correct — naming it here would
			// report a missing READER when the real problem is an
			// unrecognized engine, which other checks own.
			continue
		}
		carried := doctorReaderRanges(adapters)

		version, err := probe(ctx, engine)
		if err != nil {
			lines = append(lines, fmt.Sprintf(
				"%s: version not detected (%v); ctxloom carries readers for %s", engine, err, carried))
			continue
		}

		selected, serr := vendorreader.SelectVersionedAdapter(engine, version, "", adapters)
		if serr != nil {
			refused = true
			lines = append(lines, fmt.Sprintf(
				"%s %s: NO reader carried (ctxloom carries %s) — transcripts written by this version will REFUSE to convert rather than be read by a reader nobody validated against it",
				engine, version, carried))
			continue
		}
		lines = append(lines, fmt.Sprintf(
			"%s %s: reader %T, validated for %s (checked against %s); ctxloom carries %s",
			engine, version, selected.Adapter, selected.Range.String(), selected.ValidatedVersion, carried))
	}

	if len(lines) == 0 {
		return DoctorCheck{Marker: doctorTranscriptReaderMarker, Status: DoctorInfo,
			Detail: "no configured engine reads a vendor-native transcript store (none names a registered backend with a reader), so no version-scoped reader applies"}
	}

	detail := strings.Join(lines, "; ")
	if refused {
		return DoctorCheck{Marker: doctorTranscriptReaderMarker, Status: DoctorWarn, Detail: detail}
	}
	return DoctorCheck{Marker: doctorTranscriptReaderMarker, Status: DoctorInfo, Detail: detail}
}

// doctorReaderRanges renders the version ranges a set of candidate readers
// declares, in declaration order — the same order
// vendorreader.SelectVersionedAdapter resolves them in, so a reader of this
// line sees the list the selector actually walks rather than a re-sorted one.
func doctorReaderRanges(adapters []vendorreader.VersionedAdapter) string {
	if len(adapters) == 0 {
		return "no ranges at all"
	}
	parts := make([]string, 0, len(adapters))
	for _, a := range adapters {
		parts = append(parts, a.Range.String())
	}
	return strings.Join(parts, ", ")
}
