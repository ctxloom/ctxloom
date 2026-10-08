package kit

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/present"
)

var bothRoots = present.Traits{Roots: []present.RootKind{present.RootSessionHome, present.RootProjectRoot}, Channel: present.ChannelFile}

func bothStart(t *testing.T) (present.Start, string, string) {
	t.Helper()
	home, project := t.TempDir(), t.TempDir()
	return present.New(present.OnHost(present.Paths{ProjectRoot: present.Root{Host: project}, SessionHome: present.Root{Host: home}})), home, project
}

// TestApproach_RootedPicksTheRelForTheSelectedRoot: homeRel beneath the
// session home, projectRel beneath the project root.
func TestApproach_RootedPicksTheRelForTheSelectedRoot(t *testing.T) {
	start, home, project := bothStart(t)
	a := Approach{Engine: "e", ApproachName: "a", T: bothRoots, Private: true}
	r, err := a.Rooted(start, present.RootSessionHome, "h.json", "dir/p.json")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, "h.json"), r.Build().HostPath)
	r, err = a.Rooted(start, present.RootProjectRoot, "h.json", "dir/p.json")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(project, "dir", "p.json"), r.Build().HostPath)
	assert.Equal(t, "a", a.Name())
	assert.Equal(t, bothRoots, a.Traits())
}

// TestApproach_RootedRefusesARootTheTraitsDoNotOffer, naming the engine and
// the approach.
func TestApproach_RootedRefusesARootTheTraitsDoNotOffer(t *testing.T) {
	start, _, _ := bothStart(t)
	a := Approach{Engine: "e", ApproachName: "a", T: present.Traits{Roots: []present.RootKind{present.RootSessionHome}}}
	_, err := a.Rooted(start, present.RootProjectRoot, "x", "x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "e/a: root")
	assert.Contains(t, err.Error(), "is not one this approach offers")
}

// TestApproach_PrivateRefusesAnUnrootedSessionHome: Private refuses a start
// with no session home; without it the double is served as asked.
func TestApproach_PrivateRefusesAnUnrootedSessionHome(t *testing.T) {
	start := present.ProjectOnHost(t.TempDir())
	_, err := Approach{ApproachName: "a", T: bothRoots, Private: true}.Rooted(start, present.RootSessionHome, "x", "x")
	require.ErrorIs(t, err, agent.ErrUnrootedSessionHome)
	_, err = Approach{ApproachName: "a", T: bothRoots}.Rooted(start, present.RootSessionHome, "x", "x")
	require.NoError(t, err)
	_, err = Approach{ApproachName: "a", T: bothRoots, Private: true}.Rooted(start, present.RootProjectRoot, "x", "x")
	require.NoError(t, err, "the guard is the session home's, never the project root's")
}

// TestAppendedSection_ClaimsTheTextAfterTheFilesOwn: one appended-section
// claim on the presentation's file, holding a copy of the text.
func TestAppendedSection_ClaimsTheTextAfterTheFilesOwn(t *testing.T) {
	text := []byte("ctx")
	p := present.Presentation{HostPath: "/p/CTX.md"}
	d := AppendedSection(p, text)
	text[0] = 'X'
	assert.Equal(t, p, d.Presented)
	assert.Equal(t, map[string][]present.Claim{"/p/CTX.md": {{Pointer: present.AppendedSection, Value: []byte("ctx")}}}, d.Claims)
	assert.Empty(t, d.Files)
}
