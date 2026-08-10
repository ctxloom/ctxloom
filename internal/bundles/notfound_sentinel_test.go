package bundles

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/errs"
)

// "This item does not exist" must be one fact with one detectable answer,
// regardless of how the caller spelled the reference.
//
// The withheld branch of every *FromBundle function wraps its sentinel, and so
// does the bare-name SEARCH path — searchFragment/searchCommand/searchSkill all
// return errs.Err*NotFound, and loader_skills_test already matches on it. The
// bundle-qualified path did not: it returned a bare fmt.Errorf, so
// errors.Is(err, errs.ErrFragmentNotFound) answered true for GetFragment("x")
// and false for GetFragment("demo#fragments/x") — the same missing fragment,
// detectable or not depending on reference syntax. A caller cannot write a
// correct not-found branch against a contract that holds only half the time.
func TestBundleQualifiedRef_MissingItemWrapsItsNotFoundSentinel(t *testing.T) {
	seed := map[string]*Bundle{
		"demo": {
			Fragments: map[string]BundleFragment{"present": {Content: "body"}},
			Commands:  map[string]BundleCommand{"present": {Content: "body"}},
			Skills:    map[string]BundleSkill{"present": {}},
		},
	}
	for k, b := range seed {
		b.Name = k
	}
	l := ungated(NewLoader(seedLocal(seed)), false)

	// The fixture must resolve the PRESENT items, or "not found" below would be
	// reporting a broken loader rather than a missing item.
	_, err := l.GetFragment("demo#fragments/present")
	require.NoError(t, err, "the fixture bundle must resolve through the same path")
	_, err = l.GetCommand("demo#commands/present")
	require.NoError(t, err)

	t.Run("fragment", func(t *testing.T) {
		_, err := l.GetFragment("demo#fragments/absent")
		require.Error(t, err)
		assert.True(t, errors.Is(err, errs.ErrFragmentNotFound), "got %v", err)
		assert.Contains(t, err.Error(), "demo", "the message must still name the bundle")
		assert.Contains(t, err.Error(), "absent", "the message must still name the item")
	})
	t.Run("command", func(t *testing.T) {
		_, err := l.GetCommand("demo#commands/absent")
		require.Error(t, err)
		assert.True(t, errors.Is(err, errs.ErrCommandNotFound), "got %v", err)
	})
	t.Run("skill", func(t *testing.T) {
		_, err := l.GetSkill("demo#skills/absent")
		require.Error(t, err)
		assert.True(t, errors.Is(err, errs.ErrSkillNotFound), "got %v", err)
	})
}

// The same contract on the per-commit-version path, which has its own copies of
// the lookup.
func TestVersionedRef_MissingItemWrapsItsNotFoundSentinel(t *testing.T) {
	def := &Bundle{
		Fragments: map[string]BundleFragment{"solid": {Content: "default body"}},
		Commands:  map[string]BundleCommand{"solid": {Content: "default body"}},
	}
	versions := map[string]*Bundle{
		"c1": {
			Fragments: map[string]BundleFragment{"solid": {Content: "v1 body"}},
			Commands:  map[string]BundleCommand{"solid": {Content: "v1 body"}},
		},
	}
	// AdmitAll: this test is about the not-found sentinel, so it states that it
	// gates nothing rather than leaving the authorizer out (which withholds).
	l := versionedLoader(t, cqRef, def, versions, AdmitAll())

	// Fixture check: the present item resolves at c1, so a "not found" below is
	// about the item and not about the version resolver.
	_, err := l.GetFragmentAtVersion(cqRef+"#fragments/solid", "c1")
	require.NoError(t, err)

	_, err = l.GetFragmentAtVersion(cqRef+"#fragments/absent", "c1")
	require.Error(t, err)
	assert.True(t, errors.Is(err, errs.ErrFragmentNotFound), "got %v", err)

	_, err = l.GetPromptAtVersion(cqRef+"#commands/absent", "c1")
	require.Error(t, err)
	assert.True(t, errors.Is(err, errs.ErrCommandNotFound), "got %v", err)
}
