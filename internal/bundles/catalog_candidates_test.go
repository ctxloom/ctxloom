package bundles

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/trust"
)

// candidateProbe is a CompanionProber over a fixed pass: the loadouts that
// were obtained and the discovered companions that yielded none.
func candidateProbe(los []CompanionLoadout, cands []CompanionCandidate) CompanionProber {
	return func(context.Context) (CompanionProbe, error) {
		return CompanionProbe{Loadouts: los, Candidates: cands}, nil
	}
}

// mixedCompanionCatalog resolves one pass covering every outcome a companion
// can have: contributing, refused, absent, wedged, and delivering bytes that
// will not parse.
func mixedCompanionCatalog(t *testing.T) Catalog {
	t.Helper()
	return Resolve(context.Background(), NewCompanionReader(candidateProbe(
		[]CompanionLoadout{
			{Bin: "ltk", Path: "/opt/bin/ltk", Bundle: readerBundleYAML},
			{Bin: "garbled", Path: "/opt/bin/garbled", Bundle: []byte(":\n  not a bundle")},
		},
		[]CompanionCandidate{
			{Bin: "taskloom", Path: "/opt/bin/taskloom", Reason: CandidateUnconsented},
			{Bin: "reprise", Path: "", Reason: CandidateAbsent},
			{Bin: "wedged", Path: "/opt/bin/wedged", Reason: CandidateProbeFailed},
		},
	)))
}

// TestCatalogCandidates_CarriesTheReasonForEachCompanionThatProducedNothing is
// the point of a candidate: "installed but never allowed to run", "not
// installed" and "ran and produced nothing usable" are three different facts
// about the machine with three different remedies, and a report that cannot
// tell them apart sends a user to install something they already have.
func TestCatalogCandidates_CarriesTheReasonForEachCompanionThatProducedNothing(t *testing.T) {
	cat := mixedCompanionCatalog(t)

	got := make(map[trust.BundleKey]Candidate)
	for _, c := range cat.Candidates() {
		got[c.Ref] = c
	}
	require.Len(t, got, 4, "every companion that produced no content must be reported exactly once")

	unconsented := got["ctxloom+companion:taskloom"]
	assert.Equal(t, CandidateUnconsented, unconsented.Reason)
	assert.Equal(t, "/opt/bin/taskloom", unconsented.Path,
		"an approval keys on the FILE, so a refusal that names no file names no remedy")

	absent := got["ctxloom+companion:reprise"]
	assert.Equal(t, CandidateAbsent, absent.Reason)
	assert.Empty(t, absent.Path, "nothing on this machine answers to it, so there is no path to name")

	assert.Equal(t, CandidateProbeFailed, got["ctxloom+companion:wedged"].Reason)

	// The unparseable loadout is the reader's OWN candidate: the prober handed
	// it over as content, and only parsing found there was none.
	garbled := got["ctxloom+companion:garbled"]
	assert.Equal(t, CandidateProbeFailed, garbled.Reason,
		"bytes that will not parse are a companion that produced nothing, not one that was never reached")
	assert.Equal(t, "/opt/bin/garbled", garbled.Path)
}

// TestCatalogReads_HoldNoCandidateAndNoNilBundle is the separation the two
// collections exist for. One nil-able list would make every Reads() consumer
// responsible for testing Bundle for nil, and the one that forgot would either
// panic or treat an identity nobody read as a bundle.
func TestCatalogReads_HoldNoCandidateAndNoNilBundle(t *testing.T) {
	cat := mixedCompanionCatalog(t)

	reads := cat.Reads()
	require.Len(t, reads, 1, "guard: exactly the one parseable loadout is content")
	require.NotEmpty(t, cat.Candidates(), "guard: there must BE candidates, or the exclusion below is vacuous")

	inReads := make(map[trust.BundleKey]bool, len(reads))
	for _, read := range reads {
		require.NotNil(t, read.Bundle, "no read may carry a nil bundle")
		inReads[read.Key()] = true
	}
	assert.Equal(t, trust.BundleKey("ctxloom+companion:ltk"), reads[0].Key())

	for _, c := range cat.Candidates() {
		assert.False(t, inReads[c.Ref], "a candidate is an identity with NO content and must not appear in Reads: %s", c.Ref)
		_, ok := cat.LookupKey(c.Ref)
		assert.False(t, ok, "a candidate must not resolve: %s", c.Ref)
	}
}

// TestCatalogCandidates_EmptyWhenEveryReaderHasNothingToSay guards the other
// direction: a set built from readers that know nothing about candidates
// reports none, rather than manufacturing rows out of its reads.
func TestCatalogCandidates_EmptyWhenEveryReaderHasNothingToSay(t *testing.T) {
	cat := NewLoader(projectReaderOver(t, "kit.yaml", "version: 1.0.0\n")).Catalog()
	require.Equal(t, 1, cat.Len(), "guard: the set must hold a read, or empty-in-empty-out proves nothing")
	assert.Empty(t, cat.Candidates())
}

// TestCatalogCandidates_SurviveAReaderThatAlsoFailed proves the collection is
// not conditional on a clean read: a prober that reported candidates AND an
// error still contributes them, because "we could not reach your companions"
// must not render as "you have none".
func TestCatalogCandidates_SurviveAReaderThatAlsoFailed(t *testing.T) {
	r := &failingCandidateReader{candidates: []Candidate{
		{Ref: "ctxloom+companion:taskloom", Path: "/opt/bin/taskloom", Reason: CandidateUnconsented},
	}}
	cat := Resolve(context.Background(), r)
	assert.Empty(t, cat.Reads())
	require.Len(t, cat.Candidates(), 1)
	assert.Equal(t, CandidateUnconsented, cat.Candidates()[0].Reason)
}

// failingCandidateReader answers Read with an error while still knowing which
// identities it found nothing for.
type failingCandidateReader struct{ candidates []Candidate }

func (r *failingCandidateReader) Read(context.Context) ([]BundleRead, error) {
	return nil, assert.AnError
}

func (r *failingCandidateReader) Candidates() []Candidate { return r.candidates }
