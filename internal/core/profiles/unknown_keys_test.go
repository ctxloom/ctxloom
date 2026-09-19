package profiles

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// TestLoad_UnknownProfileKey_IsNamedAndSuggested pins the protection the inline
// profile arm used to provide and the directory arm never did.
//
// yaml.v3 ignores what it cannot map, so `select_tagz:` produced a profile that
// selected NOTHING, loaded with err=nil, and reported success everywhere — the
// characteristic silent no-op, on the only remaining way to author a profile.
func TestLoad_UnknownProfileKey_IsNamedAndSuggested(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "typo.yaml"),
		[]byte("descriptionn: oops\nselect_tagz:\n  - go\nbundles:\n  - real\n"), 0o644))

	var out bytes.Buffer
	restore := clidiag.SetSink(&out)
	defer restore()

	p, err := NewLoader([]string{dir}).Load("typo")
	require.NoError(t, err, "an unknown key WARNS; it must not stop the profile loading")
	said := out.String()

	assert.Contains(t, said, "descriptionn", "the offending key is named")
	assert.Contains(t, said, "select_tagz")
	assert.Contains(t, said, "typo.yaml", "and the file it is in")
	assert.Contains(t, said, "IGNORED", "and that it has no effect")
	assert.Contains(t, said, "did you mean `description`?", "a near miss suggests the real key")
	assert.Contains(t, said, "did you mean `select_tags`?")

	// The keys it DOES know still load: the warning is not a refusal, and the
	// check must not be passing by rejecting the whole document.
	assert.Equal(t, []string{"real"}, p.Bundles)
}

// The control: a profile using every key correctly says NOTHING. Without this,
// the assertions above would also pass against a check that warned on
// everything.
func TestLoad_WellFormedProfile_IsSilent(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "good.yaml"), []byte(
		"description: fine\nllm: big\nparents: [base]\ntags: [t]\nselect_tags: [go]\n"+
			"bundles: [b]\nbundle_items: [\"b#fragments/x\"]\nfragments:\n  - plain\n  - name: pri\n    priority: 3\n"+
			"commands: [\"b#commands/c\"]\nskills: [\"b#skills/s\"]\nvariables:\n  K: v\n"+
			"exclude_fragments: [ef]\nexclude_mcp: [em]\ndeny_tools: [Bash]\n"+
			"hooks:\n  unified:\n    pre_tool:\n      - command: x\n        type: command\n"), 0o644))

	var out bytes.Buffer
	restore := clidiag.SetSink(&out)
	defer restore()

	_, err := NewLoader([]string{dir}).Load("good")
	require.NoError(t, err)
	assert.Empty(t, out.String(),
		"every key here is real; a gate that warns on a correct profile is worse than none")
}
