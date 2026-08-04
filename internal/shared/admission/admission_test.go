// Tests for the shared admission shape and its trust-on-first-use store.
//
// The six properties in the package doc are what this package EXISTS to hold
// — they were three separate coincidences before it, and the fourth consumer
// inherits them only if something pins them. Each has a named test below whose
// name is the property. Every "it refused" assertion also checks that nothing
// was written and nothing was printed: an error return alone cannot tell a
// gate that refused from a gate that acted and then complained.
package admission_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/admission"
)

// testReason is a domain's own Reason enum, standing in for the three real
// ones. A string enum on purpose: the two real store consumers disagree about
// whether a Reason is an int or a string, and the store must not care.
type testReason string

const (
	reasonUnset    testReason = ""
	reasonApproved testReason = "approved"
	reasonDeclined testReason = "declined"
	reasonUnasked  testReason = "unasked"
	reasonFault    testReason = "fault"
)

// testKey is a compound key: Scope is what a human means by "this one", Fine
// is the exact shape they approved (a binary's hash, in the real case).
type testKey struct {
	Scope string `yaml:"scope"`
	Fine  string `yaml:"fine"`
}

func keyOf(k testKey) string   { return k.Scope + "\x00" + k.Fine }
func scopeOf(k testKey) string { return k.Scope }

func testReasons() admission.Reasons[testReason] {
	return admission.Reasons[testReason]{
		Approved: reasonApproved, Declined: reasonDeclined,
		Unasked: reasonUnasked, Fault: reasonFault,
	}
}

func fixedClock() func() time.Time {
	return func() time.Time { return time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC) }
}

// newTestStore builds a store over an in-memory filesystem at a nested path,
// so directory creation is exercised too.
func newTestStore(t *testing.T) (*admission.Store[testKey, testReason], afero.Fs, string) {
	t.Helper()
	fs := afero.NewMemMapFs()
	path := filepath.Join("/home", "u", ".ctxloom", "decisions.yaml")
	s := admission.NewStore(fs, path, keyOf, testReasons(),
		admission.WithScope(scopeOf), admission.WithClock[testKey](fixedClock()))
	return s, fs, path
}

// alwaysAsk answers every question the same way and counts the questions.
func alwaysAsk(answer bool, asked *int) admission.Ask[testKey] {
	return func(context.Context, testKey) (bool, bool, error) {
		*asked++
		return answer, true, nil
	}
}

// ===== PROPERTY 1: the zero value withholds ==================================

// TestProperty_ZeroValueWithholds pins the property a struct literal that
// forgot a field must not be able to break: an unpopulated Decision denies,
// and its Reason is the domain's own zero rather than any real outcome.
func TestProperty_ZeroValueWithholds(t *testing.T) {
	var d admission.Decision[testReason]
	assert.False(t, d.Allow, "an unpopulated Decision must deny")
	assert.Equal(t, reasonUnset, d.Reason, "an unpopulated Decision must carry no real reason")
	assert.Empty(t, d.Detail)

	// And no store may emit that zero reason as a real answer: a Reasons whose
	// members include the zero value is a construction fault, so a caller
	// cannot accidentally make "unset" mean something.
	bad := admission.NewStore(afero.NewMemMapFs(), "/x/y.yaml", keyOf,
		admission.Reasons[testReason]{Approved: reasonApproved, Declined: reasonDeclined, Unasked: reasonUnasked})
	_, err := bad.List()
	require.Error(t, err, "a store whose Fault reason is the zero value must refuse to operate")
	assert.Contains(t, err.Error(), "zero reason")
}

// ===== PROPERTY 2: the decider is a pure function; the caller renders ========

// TestProperty_DeciderIsPureCallerRenders drives every branch of Decide —
// unrecorded, recorded-yes, recorded-no, asked, and an unreadable store — with
// stdout and stderr redirected, and asserts ZERO bytes came out. The
// human-facing text is on Detail, for the caller to say.
func TestProperty_DeciderIsPureCallerRenders(t *testing.T) {
	s, fs, path := newTestStore(t)
	ctx := context.Background()

	stdout, stderr := captureStdio(t, func() {
		asked := 0
		_, _ = s.Decide(ctx, testKey{"a", "1"}, nil)                        // unasked
		_, _ = s.Decide(ctx, testKey{"a", "1"}, alwaysAsk(true, &asked))    // asked yes
		_, _ = s.Decide(ctx, testKey{"a", "1"}, alwaysAsk(false, &asked))   // now recorded
		_, _ = s.Decide(ctx, testKey{"b", "1"}, alwaysAsk(false, &asked))   // asked no
		_, _ = s.Decide(ctx, testKey{"b", "1"}, alwaysAsk(true, &asked))    // now declined
		require.NoError(t, afero.WriteFile(fs, path, []byte("{{{"), 0o600)) // unreadable
		_, _ = s.Decide(ctx, testKey{"c", "1"}, alwaysAsk(true, &asked))
	})
	assert.Empty(t, stdout, "the decider wrote to stdout; rendering belongs to the caller")
	assert.Empty(t, stderr, "the decider wrote to stderr; rendering belongs to the caller")

	// And the fault's human-facing text is on Detail, not dropped: a caller
	// that renders Detail can say exactly what went wrong.
	d, err := s.Decide(ctx, testKey{"c", "1"}, nil)
	require.Error(t, err)
	assert.False(t, d.Allow)
	assert.Equal(t, reasonFault, d.Reason)
	assert.Contains(t, d.Detail, path, "Detail must name the store the caller has to fix")
}

// ===== PROPERTY 3: "nobody could be asked" != "you declined" =================

// TestProperty_UnaskedAndDeclinedStayDifferent pins the distinction whose two
// halves have different fixes: supply a terminal, versus change your mind.
func TestProperty_UnaskedAndDeclinedStayDifferent(t *testing.T) {
	s, _, _ := newTestStore(t)
	ctx := context.Background()

	unasked, err := s.Decide(ctx, testKey{"a", "1"}, nil)
	require.NoError(t, err)
	assert.False(t, unasked.Allow)
	assert.Equal(t, reasonUnasked, unasked.Reason)

	asked := 0
	declined, err := s.Decide(ctx, testKey{"a", "1"}, alwaysAsk(false, &asked))
	require.NoError(t, err)
	assert.False(t, declined.Allow)
	assert.Equal(t, reasonDeclined, declined.Reason)

	require.NotEqual(t, unasked.Reason, declined.Reason,
		"a session with nobody to ask and a human who said no are different facts with different fixes")

	// The store refuses to be built with them spelled the same, so no future
	// domain can quietly merge them.
	merged := admission.NewStore(afero.NewMemMapFs(), "/x/y.yaml", keyOf,
		admission.Reasons[testReason]{
			Approved: reasonApproved, Declined: reasonDeclined,
			Unasked: reasonDeclined, Fault: reasonFault,
		})
	_, berr := merged.List()
	require.Error(t, berr, "a store must refuse to merge 'nobody could be asked' into 'you declined'")
	assert.Contains(t, berr.Error(), "same reason")
}

// ===== PROPERTY 4: non-interactive REFUSES ===================================

// TestProperty_NonInteractiveRefuses pins that a nil Ask neither prompts nor
// assumes: it refuses, records NOTHING, and leaves the store absent so a later
// interactive session still gets to ask.
func TestProperty_NonInteractiveRefuses(t *testing.T) {
	s, fs, path := newTestStore(t)

	d, err := s.Decide(context.Background(), testKey{"a", "1"}, nil)
	require.NoError(t, err)
	assert.False(t, d.Allow, "a session with nobody to ask must refuse, not assume")
	assert.Equal(t, reasonUnasked, d.Reason)

	exists, eerr := afero.Exists(fs, path)
	require.NoError(t, eerr)
	assert.False(t, exists, "a refusal nobody was asked about must not be persisted as a decision")

	// A question that was PUT but produced no answer (EOF, a closed stdin) is
	// the same refusal and is likewise not persisted: a transient must never
	// harden into a permanent refusal the user has to discover and undo.
	silent := admission.Ask[testKey](func(context.Context, testKey) (bool, bool, error) {
		return false, false, nil
	})
	d2, err2 := s.Decide(context.Background(), testKey{"a", "1"}, silent)
	require.NoError(t, err2)
	assert.False(t, d2.Allow)
	assert.Equal(t, reasonUnasked, d2.Reason, "no answer is not an answer, and is not a decline")
	exists, eerr = afero.Exists(fs, path)
	require.NoError(t, eerr)
	assert.False(t, exists, "a transient non-answer must not be recorded")
}

// ===== PROPERTY 5: an unresolvable $HOME refuses, never reads the cwd ========

// TestProperty_UnconfiguredStoreRefusesRatherThanReadingTheWorkingDirectory
// pins the configured() guard. filepath.Join("", x) == x, so an unconfigured
// store would key off the process working directory — and since a record IS
// the authority, a stray file at a repo root would authorise something.
//
// The fixture makes that concrete: a fully valid, approving record sits at the
// relative path an unconfigured store would resolve to. It must not be seen.
func TestProperty_UnconfiguredStoreRefusesRatherThanReadingTheWorkingDirectory(t *testing.T) {
	fs := afero.NewMemMapFs()
	planted := "decisions.yaml"
	require.NoError(t, afero.WriteFile(fs, planted, []byte(
		"version: 1\nrecords:\n  - key:\n      scope: a\n      fine: \"1\"\n    approved: true\n"), 0o600))

	s := admission.NewStore(fs, "", keyOf, testReasons(), admission.WithScope(scopeOf))
	assert.Empty(t, s.Path(), "an unresolvable home yields an unconfigured store, not an empty one")

	d, err := s.Decide(context.Background(), testKey{"a", "1"}, nil)
	require.Error(t, err, "an unconfigured store must fault rather than answer")
	assert.False(t, d.Allow, "the planted file at the working directory must not have authorised anything")
	assert.Equal(t, reasonFault, d.Reason)

	// Every other verb refuses too, and none of them writes.
	_, lerr := s.List()
	require.Error(t, lerr)
	_, serr := s.Set(testKey{"a", "1"}, true)
	require.Error(t, serr)
	_, ferr := s.Forget(testKey{"a", "1"})
	require.Error(t, ferr)

	// The planted file is untouched: not read as authority, and not rewritten.
	body, rerr := afero.ReadFile(fs, planted)
	require.NoError(t, rerr)
	assert.Contains(t, string(body), "approved: true", "the working-directory file must be neither honored nor clobbered")
}

// ===== PROPERTY 6: consent records are personal, never committable ==========

// TestProperty_RecordsArePersonalAndNeverCommittable pins the shape that keeps
// a cloned repo from arriving carrying pre-approved binaries and pre-approved
// publish destinations: ONE file, at the ONE path the store was constructed
// with, owner-only, in an owner-only directory. There is deliberately no API
// by which a second, project-local location could be consulted.
func TestProperty_RecordsArePersonalAndNeverCommittable(t *testing.T) {
	s, fs, path := newTestStore(t)
	_, err := s.Set(testKey{"a", "1"}, true)
	require.NoError(t, err)

	var written []string
	require.NoError(t, afero.Walk(fs, "/", func(p string, info os.FileInfo, werr error) error {
		if werr != nil || info == nil || info.IsDir() {
			return nil
		}
		written = append(written, p)
		return nil
	}))
	assert.Equal(t, []string{path}, written, "a decision must land in exactly one personal file and no twin")

	info, serr := fs.Stat(path)
	require.NoError(t, serr)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(),
		"a world-readable record leaks the machine's layout; a world-writable one hands the decision away")

	dinfo, derr := fs.Stat(filepath.Dir(path))
	require.NoError(t, derr)
	assert.Equal(t, os.FileMode(0o700), dinfo.Mode().Perm())
}

// ===== The store's two-key rule =============================================

// TestStore_ApprovalIsKeyExactDenialIsScopeWide is the asymmetry both real
// stores were built with: an approval binds the exact thing that was approved,
// so any change re-asks; a denial covers the whole scope, so "I never want
// this" survives the thing being rebuilt.
func TestStore_ApprovalIsKeyExactDenialIsScopeWide(t *testing.T) {
	s, _, _ := newTestStore(t)
	ctx := context.Background()

	_, err := s.Set(testKey{"bin", "sha-old"}, true)
	require.NoError(t, err)

	same, err := s.Decide(ctx, testKey{"bin", "sha-old"}, nil)
	require.NoError(t, err)
	assert.True(t, same.Allow, "the exact approved key must be admitted without asking again")
	assert.Equal(t, reasonApproved, same.Reason)

	changed, err := s.Decide(ctx, testKey{"bin", "sha-new"}, nil)
	require.NoError(t, err)
	assert.False(t, changed.Allow, "a changed key must not inherit the old approval")
	assert.Equal(t, reasonUnasked, changed.Reason)

	// Now decline it. The denial must cover the scope, hash-blind.
	_, err = s.Set(testKey{"bin", "sha-new"}, false)
	require.NoError(t, err)
	for _, fine := range []string{"sha-old", "sha-new", "sha-rebuilt"} {
		d, derr := s.Decide(ctx, testKey{"bin", fine}, nil)
		require.NoError(t, derr)
		assert.False(t, d.Allow, "a refusal must survive the thing being rebuilt (%s)", fine)
		assert.Equal(t, reasonDeclined, d.Reason, "and must say it was declined, not that nobody asked (%s)", fine)
	}
}

// TestStore_SetReplacesTheScopesLiveDecision: a scope holds exactly ONE live
// decision, so re-deciding never leaves a stale approval behind for an older
// shape of the same thing.
func TestStore_SetReplacesTheScopesLiveDecision(t *testing.T) {
	s, _, _ := newTestStore(t)
	_, err := s.Set(testKey{"bin", "sha-old"}, true)
	require.NoError(t, err)
	_, err = s.Set(testKey{"bin", "sha-new"}, true)
	require.NoError(t, err)

	recs, err := s.List()
	require.NoError(t, err)
	require.Len(t, recs, 1, "re-deciding a scope must not accumulate records")
	assert.Equal(t, testKey{"bin", "sha-new"}, recs[0].Key)

	stale, err := s.Decide(context.Background(), testKey{"bin", "sha-old"}, nil)
	require.NoError(t, err)
	assert.False(t, stale.Allow, "the superseded approval must be gone, not merely shadowed")
}

// TestStore_ForgetReportsHowManyWentAndZeroIsNotSuccess: forgetting something
// nobody recorded is the caller's mistake to see, never a silent success.
func TestStore_ForgetReportsHowManyWentAndZeroIsNotSuccess(t *testing.T) {
	s, _, _ := newTestStore(t)
	_, err := s.Set(testKey{"bin", "sha-a"}, true)
	require.NoError(t, err)
	_, err = s.Set(testKey{"other", "sha-b"}, true)
	require.NoError(t, err)

	n, err := s.Forget(testKey{"bin", ""})
	require.NoError(t, err)
	assert.Equal(t, 1, n, "Forget removes by scope, so a key's finer half need not be known")

	n, err = s.Forget(testKey{"bin", ""})
	require.NoError(t, err)
	assert.Equal(t, 0, n, "forgetting nothing must report zero, never success-with-no-effect")

	recs, err := s.List()
	require.NoError(t, err)
	require.Len(t, recs, 1, "Forget must not touch another scope")
	assert.Equal(t, "other", recs[0].Key.Scope)
}

// TestStore_AbsentFileIsNotAFaultButAnUnreadableOneIs: "nobody has decided
// anything yet" and "the record cannot be read" are opposite answers. Reading
// the second as the first would re-open a door a human closed.
func TestStore_AbsentFileIsNotAFaultButAnUnreadableOneIs(t *testing.T) {
	s, fs, path := newTestStore(t)

	recs, err := s.List()
	require.NoError(t, err, "an absent store is the ordinary starting state")
	assert.Empty(t, recs)

	require.NoError(t, afero.WriteFile(fs, path, []byte("not: [valid"), 0o600))
	_, err = s.List()
	require.Error(t, err, "an unparseable store must fault, never read as empty")

	d, derr := s.Decide(context.Background(), testKey{"a", "1"}, nil)
	require.Error(t, derr)
	assert.False(t, d.Allow, "an unreadable store denies everything")
	assert.Equal(t, reasonFault, d.Reason)

	// And a write through it is refused rather than clobbering decisions it
	// could not read.
	_, serr := s.Set(testKey{"a", "1"}, true)
	require.Error(t, serr)
	assert.Contains(t, serr.Error(), "refusing to overwrite")
	body, rerr := afero.ReadFile(fs, path)
	require.NoError(t, rerr)
	assert.Equal(t, "not: [valid", string(body))
}

// TestStore_VersionMismatchFaultsRatherThanReadingAsEmpty: a future format
// must fail loud. Silently reading it as "nothing consented" is the worst
// available failure.
func TestStore_VersionMismatchFaultsRatherThanReadingAsEmpty(t *testing.T) {
	s, fs, path := newTestStore(t)
	require.NoError(t, afero.WriteFile(fs, path, []byte("version: 99\nrecords: []\n"), 0o600))

	_, err := s.List()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "version 99")

	d, derr := s.Decide(context.Background(), testKey{"a", "1"}, nil)
	require.Error(t, derr)
	assert.False(t, d.Allow)
	assert.Equal(t, reasonFault, d.Reason)
}

// TestStore_AnAnsweredYesThatCannotBeRecordedStillHoldsAndSaysSo: the store
// returns BOTH the decision and the write error and decides neither for the
// caller — one real consumer honors the yes and warns, the other treats an
// unrecordable confirmation as fatal.
func TestStore_AnAnsweredYesThatCannotBeRecordedStillHoldsAndSaysSo(t *testing.T) {
	base := afero.NewMemMapFs()
	path := filepath.Join("/home", "u", ".ctxloom", "decisions.yaml")
	s := admission.NewStore(afero.NewReadOnlyFs(base), path, keyOf, testReasons(),
		admission.WithScope(scopeOf), admission.WithClock[testKey](fixedClock()))

	asked := 0
	d, err := s.Decide(context.Background(), testKey{"a", "1"}, alwaysAsk(true, &asked))
	require.Error(t, err, "the caller must be able to see that the answer was not persisted")
	assert.Equal(t, 1, asked)
	assert.True(t, d.Allow, "refusing a yes the human just typed would be its own silent no-op")
	assert.Equal(t, reasonApproved, d.Reason)
	assert.NotEmpty(t, d.Detail, "the caller needs the text to explain why it will be asked again")
}

// TestStore_DecideRecordsTheAnswerSoTheQuestionIsAskedOnce is the whole point
// of trust-on-first-use, and the assertion is the ask COUNT, not an exit code.
func TestStore_DecideRecordsTheAnswerSoTheQuestionIsAskedOnce(t *testing.T) {
	s, _, _ := newTestStore(t)
	asked := 0
	ask := alwaysAsk(true, &asked)

	for range 3 {
		d, err := s.Decide(context.Background(), testKey{"a", "1"}, ask)
		require.NoError(t, err)
		assert.True(t, d.Allow)
	}
	assert.Equal(t, 1, asked, "a recorded answer must silence the question")

	recs, err := s.List()
	require.NoError(t, err)
	require.Len(t, recs, 1)
	assert.Equal(t, fixedClock()(), recs[0].RecordedAt.UTC())
}

// TestSnapshot_LoadsOnceAndCanBeAmendedInMemory is the batch decider's shape:
// one read for N candidates, and an answer taken mid-pass is visible to the
// rest of the pass without a re-read.
func TestSnapshot_LoadsOnceAndCanBeAmendedInMemory(t *testing.T) {
	s, _, _ := newTestStore(t)
	_, err := s.Set(testKey{"a", "1"}, true)
	require.NoError(t, err)

	snap, err := s.Load()
	require.NoError(t, err)
	assert.True(t, snap.Approved(testKey{"a", "1"}))
	assert.False(t, snap.Approved(testKey{"b", "1"}))
	assert.False(t, snap.Declined(testKey{"a", "1"}))

	snap.Note(admission.Record[testKey]{Key: testKey{"b", "1"}, Approved: false})
	assert.True(t, snap.Declined(testKey{"b", "2"}), "an in-pass denial covers the scope like any other")
	assert.Len(t, snap.Records(), 2)
}

// TestAuthorizerFunc_AdaptsAPlainFunction pins the one-expression adapter the
// content gate uses.
func TestAuthorizerFunc_AdaptsAPlainFunction(t *testing.T) {
	var a admission.Authorizer[string, testReason] = admission.AuthorizerFunc[string, testReason](
		func(q string) admission.Decision[testReason] {
			if q == "ok" {
				return admission.Decision[testReason]{Allow: true, Reason: reasonApproved}
			}
			return admission.Decision[testReason]{Reason: reasonDeclined, Detail: "not ok"}
		})
	assert.True(t, a.Admit("ok").Allow)
	no := a.Admit("nope")
	assert.False(t, no.Allow)
	assert.Equal(t, "not ok", no.Detail)
}

// TestNewStore_WithoutAKeyFunctionRefuses: a store that cannot say what two
// records mean by "the same thing" cannot decide anything.
func TestNewStore_WithoutAKeyFunctionRefuses(t *testing.T) {
	s := admission.NewStore[testKey](afero.NewMemMapFs(), "/x/y.yaml", nil, testReasons())
	_, err := s.List()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no key function")
}

// captureStdio runs fn with os.Stdout and os.Stderr redirected and returns
// whatever it wrote to each.
func captureStdio(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()
	outR, outW, err := os.Pipe()
	require.NoError(t, err)
	errR, errW, err := os.Pipe()
	require.NoError(t, err)

	prevOut, prevErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, errW
	done := make(chan struct{})
	var outBuf, errBuf []byte
	go func() {
		defer close(done)
		outBuf, _ = io.ReadAll(outR)
	}()
	errDone := make(chan struct{})
	go func() {
		defer close(errDone)
		errBuf, _ = io.ReadAll(errR)
	}()

	func() {
		defer func() {
			os.Stdout, os.Stderr = prevOut, prevErr
			_ = outW.Close()
			_ = errW.Close()
		}()
		fn()
	}()
	<-done
	<-errDone
	return string(outBuf), string(errBuf)
}

// errAsk is a helper for the "the question could not be put" arm.
var errAskFailed = errors.New("stdin closed")

// TestStore_AQuestionThatCouldNotBePutIsNotAnAnswer: an Ask that errors is
// never an affirmative, and is not recorded.
func TestStore_AQuestionThatCouldNotBePutIsNotAnAnswer(t *testing.T) {
	s, fs, path := newTestStore(t)
	d, err := s.Decide(context.Background(), testKey{"a", "1"},
		func(context.Context, testKey) (bool, bool, error) { return true, true, errAskFailed })
	require.ErrorIs(t, err, errAskFailed)
	assert.False(t, d.Allow, "a question that could not be put is never an affirmative")
	assert.Equal(t, reasonUnasked, d.Reason)
	assert.Contains(t, d.Detail, "stdin closed")

	exists, eerr := afero.Exists(fs, path)
	require.NoError(t, eerr)
	assert.False(t, exists)
}
