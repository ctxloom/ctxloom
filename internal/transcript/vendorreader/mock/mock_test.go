// This suite is self-contained and independently triggerable, matching the
// claude adapter suite's convention: it imports only internal/transcript (the
// Recorder sink), internal/paths, and internal/testsupport (env/HOME
// isolation) — no other engine's adapter.
//
//	go test ./internal/transcript/vendorreader/mock/...
package mock

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/transcript"
)

const fixtureHarp = "mock-fixture-harp"

// TestMain closes the walk-up from this package's working directory, which
// would otherwise adopt the repo's own .ctxloom as the project under test.
// testsupport.Isolate only roots HOME; the sandbox is what closes the rest.
func TestMain(m *testing.M) { os.Exit(testsupport.SandboxedMain(m)) }

// fixturePath resolves testdata/<name> from THIS FILE's own location rather
// than the working directory. SandboxedMain relocates the cwd, so a relative
// "testdata/..." resolves to nothing — and it fails as a missing FILE, which
// reads exactly like a genuine conversion failure. Anchor it instead.
func fixturePath(t *testing.T, name string) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller failed")
	return filepath.Join(filepath.Dir(thisFile), "testdata", name)
}

// runConvert converts testdata/<fixture> into a fresh, isolated Recorder and
// returns the Records written, in file order.
func runConvert(t *testing.T, fixture string) []transcript.Record {
	t.Helper()
	testsupport.Isolate(t)

	rec, err := transcript.NewRecorder(fixtureHarp, "mock")
	require.NoError(t, err)

	require.NoError(t, Adapter{}.Convert(context.Background(), rec, fixturePath(t, fixture)))
	require.NoError(t, rec.Close())

	path, err := paths.HarpCanonicalTranscriptPath(fixtureHarp)
	require.NoError(t, err)
	return readRecords(t, path)
}

func readRecords(t *testing.T, path string) []transcript.Record {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()

	var recs []transcript.Record
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var r transcript.Record
		require.NoError(t, json.Unmarshal([]byte(line), &r), "unmarshal line: %s", line)
		recs = append(recs, r)
	}
	require.NoError(t, scanner.Err())
	return recs
}

// TestConvert_MapsRolesAndSkipsEverythingElse asserts the CONTENT written, not
// merely that something was. The fixture deliberately carries five lines of
// which only three are conversational: an exact count is what catches an
// adapter that silently drops a turn or promotes an administrative line.
func TestConvert_MapsRolesAndSkipsEverythingElse(t *testing.T) {
	recs := runConvert(t, "basic.jsonl")

	require.Len(t, recs, 3, "two non-conversational lines (administrative + malformed) must contribute nothing")

	for i, rec := range recs {
		require.NotNil(t, rec.Entry, "record %d must carry an entry payload", i)
		assert.Equal(t, "mock", rec.Engine, "record %d must carry the registered engine name", i)
		assert.Equal(t, i, rec.Seq, "seq must follow the source file's own order")
	}

	assert.Equal(t, "user", recs[0].Entry.Type)
	assert.Equal(t, "first question", recs[0].Entry.Content)
	assert.Equal(t, "assistant", recs[1].Entry.Type)
	assert.Equal(t, "first answer", recs[1].Entry.Content)
	assert.Equal(t, "user", recs[2].Entry.Type)
	assert.Equal(t, "second question", recs[2].Entry.Content,
		"a conversational line AFTER the malformed one must still be read — skipping must not abort the file")
}

// TestConvert_MissingFileIsAnError pins the one failure that IS fatal at the
// open step, distinct from the malformed-line case above which is not.
func TestConvert_MissingFileIsAnError(t *testing.T) {
	testsupport.Isolate(t)

	rec, err := transcript.NewRecorder(fixtureHarp, "mock")
	require.NoError(t, err)
	defer func() { _ = rec.Close() }()

	err = Adapter{}.Convert(context.Background(), rec, fixturePath(t, "does-not-exist.jsonl"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mock", "the error must name the vendor so a multi-adapter failure is attributable")
}

// TestConvert_CancelledContextIsFatal pins the contract that ctx cancellation
// aborts rather than silently truncating the conversion — the shape this
// project calls a silent no-op.
func TestConvert_CancelledContextIsFatal(t *testing.T) {
	testsupport.Isolate(t)

	rec, err := transcript.NewRecorder(fixtureHarp, "mock")
	require.NoError(t, err)
	defer func() { _ = rec.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = Adapter{}.Convert(ctx, rec, fixturePath(t, "basic.jsonl"))
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

// TestVersionedAdapters_DeclaresACoveredValidatedVersion holds mock's own
// range and citation together, the way
// operations.TestVendorReaderRanges_ContainThePinnedTestedVersion does for the
// real engines. Mock is deliberately absent from that test's pin map — it has
// no vendor release feed to drift against — so this is the only thing keeping
// its declaration self-consistent.
func TestVersionedAdapters_DeclaresACoveredValidatedVersion(t *testing.T) {
	require.Len(t, VersionedAdapters, 1, "a degenerate adapter declares exactly one range")

	for _, a := range VersionedAdapters {
		require.NotEmpty(t, a.ValidatedVersion)
		assert.GreaterOrEqual(t, a.ValidatedVersion, a.Versions.MinInclusive,
			"the cited validated version must fall inside the declared range")
		assert.Less(t, a.ValidatedVersion, a.Versions.MaxExclusive,
			"the cited validated version must fall inside the declared range")
	}
}

// TestConvert_WrongFormatRefusesRatherThanEmptyingTheFile is the guard that
// matters most here, and it is not hypothetical: RefreshVendorTranscript
// ATOMICALLY REPLACES the canonical transcript with whatever Convert produces.
// An adapter handed a file in someone else's format finds no lines it
// recognizes, and if it returns nil it has just destroyed a real transcript
// and reported success — this project's characteristic silent no-op, in its
// most damaging form.
//
// The fixture is a well-formed CANONICAL transcript, which is precisely the
// file that was being fed to this adapter when the failure was found: every
// line parses as JSON, so the malformed path never fires, and only the
// zero-entries floor catches it.
func TestConvert_WrongFormatRefusesRatherThanEmptyingTheFile(t *testing.T) {
	testsupport.Isolate(t)

	rec, err := transcript.NewRecorder(fixtureHarp, "mock")
	require.NoError(t, err)
	defer func() { _ = rec.Close() }()

	err = Adapter{}.Convert(context.Background(), rec, fixturePath(t, "wrong-format.jsonl"))
	require.Error(t, err, "converting a file in another format must REFUSE, never report success having written nothing")
	assert.Contains(t, err.Error(), "ZERO transcript entries",
		"the refusal must say what happened, so a caller is not left guessing why its transcript is empty")
	assert.Contains(t, err.Error(), vendorName, "the refusal must name the adapter that refused")
}
