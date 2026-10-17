package profiles

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/errs"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// A missing parent the project itself would have to hold — a bare name, or
// an explicit ctxloom+local: ref — is not something `ctxloom deps pull` can
// install: pulling fetches remote bundles and nothing else. Its finding must
// name a remedy that can work (create it, correct the ref, or drop it), and
// must not send the user round a pull that changes nothing. A missing REMOTE
// bundle-profile parent keeps the pull remedy, which is the one that fixes it.
func TestLoader_ResolveProfile_MissingParentRemedyFitsItsKind(t *testing.T) {
	tmpDir := projectProfilesDir(t)
	const remoteParent = "ctxloom+git://github.com/example/kit//bundles/kit#profiles/base"
	profile := "parents:\n" +
		"  - ghost\n" +
		"  - ctxloom+local:project#profiles/phantom\n" +
		"  - " + remoteParent + "\n" +
		"bundles:\n  - own-bundle\n"
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "child.yaml"), []byte(profile), 0o644))

	var found report.Findings
	loader := osLoader(t, tmpDir, WithReporter(&found))
	_, err := loader.ResolveProfile("child", nil)
	require.NoError(t, err)

	byParent := map[string]report.Finding{}
	for _, f := range found {
		for _, p := range []string{"ghost", "phantom", remoteParent} {
			if strings.Contains(f.Text, p) {
				byParent[p] = f
			}
		}
	}
	require.Len(t, byParent, 3, "one finding per missing parent: %v", found)

	for _, local := range []string{"ghost", "phantom"} {
		f := byParent[local]
		assert.Equal(t, report.KindRef, f.Kind, local)
		assert.True(t, f.Fatal(), "a missing local parent is still fail-loudly: %s", local)
		assert.NotContains(t, f.Remedy, "deps pull", "pulling cannot create a local profile: %s", local)
		assert.NotContains(t, f.Text, "deps pull", "nor may the text point at a pull: %s", local)
		assert.Contains(t, f.Remedy, "child", "the remedy names the profile whose parents carry it: %s", local)
	}
	remote := byParent[remoteParent]
	assert.Equal(t, report.KindRef, remote.Kind)
	assert.Contains(t, remote.Remedy, "ctxloom deps pull", "a remote parent is installed by a pull")
}

// The local miss is a typed sentinel, so callers branch on errors.Is rather
// than on the text — and it still IS a profile-not-found for every caller
// that only asks that.
func TestLoader_Load_LocalMissIsTyped(t *testing.T) {
	loader := osLoader(t, projectProfilesDir(t))
	_, err := loader.Load("ghost")
	require.Error(t, err)
	assert.True(t, errors.Is(err, errs.ErrLocalProfileNotFound))
	assert.True(t, errors.Is(err, errs.ErrProfileNotFound))

	_, err = loader.Load("ctxloom+git://github.com/example/kit//bundles/kit#profiles/base")
	require.Error(t, err)
	assert.False(t, errors.Is(err, errs.ErrLocalProfileNotFound), "a remote miss is not a local one")
	assert.True(t, errors.Is(err, errs.ErrProfileNotFound))
}
