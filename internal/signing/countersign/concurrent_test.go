package countersign

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/signing"
)

// concurrentWriters is the width of the reproduction below. 20 matches the
// figure task tall-nanny measured on config Save() — atomic writes in place,
// no engaged lock — where 13 of 20 concurrent writes were lost.
const concurrentWriters = 20

// TestStore_ConcurrentSignatureWrites_LoseNothing establishes which half of
// this store is NOT at risk, so the fix (and the reader) can stop looking here.
//
// A signature record's filename is content-addressed: indexHash(header,
// payload) + a key tag. Two concurrent writers recording two DIFFERENT
// decisions therefore write two DIFFERENT files, and two writers recording the
// SAME decision write byte-identical content to one path. Neither is a
// read-modify-write, so neither has a lost-update window — atomicity is
// sufficient here, which is exactly why the absence of a lock is not by itself
// a defect. The DECISIONS survive concurrency.
func TestStore_ConcurrentSignatureWrites_LoseNothing(t *testing.T) {
	signer, _ := testSigner(t)
	fs := afero.NewOsFs()
	dir := t.TempDir()
	s := NewStore(dir, fs)

	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, concurrentWriters)
	for i := range concurrentWriters {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := s.WriteRefReject(fmt.Sprintf("acme/tooling#fragments/f%02d", i), signer); err != nil {
				errs <- err
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	matches, err := afero.Glob(fs, dir+"/*.sig")
	require.NoError(t, err)
	assert.Len(t, matches, concurrentWriters,
		"every recorded decision must survive: signature records are content-addressed, so they cannot clobber one another")
}

// TestStore_ConcurrentIndexAppends_LoseNothing is the REPRODUCTION.
//
// Store.AppendIndex is a read-modify-write of the whole sidecar index:
// readIndex, append one entry, writeIndex the lot. The write is atomic
// (iox.WriteFileAtomicFs — unique temp file, fsync, rename) and there is no
// lock anywhere in this package. Atomicity prevents a TORN file; it does
// nothing about writer B reading the index before writer A's rename lands and
// then rewriting it without A's entry. Those are different problems and only
// one of them has a fix here.
//
// What is lost is the record that labels an item UPDATE rather than NEW and
// supplies review's diff base. AppendIndex's own doc calls that "a
// review-integrity loss, not a cosmetic one": an item whose index entry
// vanished presents to `ctxloom review` as first-time content, so substituted
// bytes are shown without the diff that would expose the substitution.
func TestStore_ConcurrentIndexAppends_LoseNothing(t *testing.T) {
	fs := afero.NewOsFs()
	dir := t.TempDir()
	s := NewStore(dir, fs)
	require.NoError(t, s.writeIndex(nil))

	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range concurrentWriters {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_ = s.AppendIndex(IndexEntry{
				Ref:        fmt.Sprintf("acme/tooling#fragments/f%02d", i),
				Kind:       "fragment",
				Form:       string(signing.FormRaw),
				Assertion:  string(signing.AssertionApprove),
				ReviewedAt: time.Now().UTC().Format(time.RFC3339Nano),
			})
		}()
	}
	close(start)
	wg.Wait()

	entries, err := s.readIndex()
	require.NoError(t, err)
	assert.Len(t, entries, concurrentWriters,
		"every appended index record must survive concurrent appends; atomic writes do not serialize a read-modify-write")
}
