package profiles

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// invalidProfiles are documents profile-schema.json rejects but yaml.v3 decodes
// without complaint — the shapes that used to load silently. Each still
// decodes, so the profile ENUMERATES; the schema violation is what must not
// pass quietly. The value is the text the finding must carry to name the
// violation.
var invalidProfiles = map[string]struct{ body, names string }{
	"wrong type": {
		body:  "llm: 5\nbundles: [real]\n",
		names: "/llm",
	},
	"unknown key": {
		body:  "select_tagz: [go]\nbundles: [real]\n",
		names: "select_tagz",
	},
	"empty list entry": {
		body:  "bundles:\n  - \n  - real\n",
		names: "/bundles/0",
	},
	"parents with null value": {
		body:  "parents:\nbundles: [real]\n",
		names: "/parents",
	},
}

func loadInvalid(t *testing.T, body string) (*Profile, report.Findings) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bad.yaml"), []byte(body), 0o644))
	var found report.Collector
	p, err := NewLoader([]string{dir}, WithReporter(&found)).Load("bad")
	require.NoError(t, err, "a schema violation is a finding, not a load error: the profile must still enumerate")
	return p, found.All()
}

// TestLoad_SchemaViolation_RefusesByDefault: every violation of the declared
// profile schema is a fatal config finding, so the strict startup gate refuses
// the launch and names what is wrong and where.
func TestLoad_SchemaViolation_RefusesByDefault(t *testing.T) {
	for name, tc := range invalidProfiles {
		t.Run(name, func(t *testing.T) {
			_, found := loadInvalid(t, tc.body)

			fatal := found.Fatal()
			require.NotEmpty(t, fatal, "a profile the schema rejects must not load silently")
			assert.Equal(t, report.KindConfig, fatal[0].Kind)
			said := strings.Join(fatal.Texts(), "\n")
			assert.Contains(t, said, "bad.yaml", "the finding names the file")
			assert.Contains(t, said, tc.names, "the finding names the violation")

			strict := strictness.Mode{}
			err := strict.ListingError("refusing to start", strict.Actionable(found))
			require.Error(t, err, "strict mode refuses to launch on an invalid profile")
		})
	}
}

// TestLoad_SchemaViolation_DegradedWarnsAndLaunches is the other arm, and the
// one that keeps --degraded honest: the SAME invalid profile is reported but
// does not stop the launch. A finding marked non-degradable would pass the
// refusal test above and fail this one.
func TestLoad_SchemaViolation_DegradedWarnsAndLaunches(t *testing.T) {
	for name, tc := range invalidProfiles {
		t.Run(name, func(t *testing.T) {
			_, found := loadInvalid(t, tc.body)

			require.NotEmpty(t, found.Fatal(), "degraded still validates: the finding is recorded")
			degraded := strictness.Mode{Degraded: true}
			assert.Empty(t, degraded.Actionable(found), "--degraded downgrades the finding to a warning")
			assert.NoError(t, degraded.ListingError("refusing to start", degraded.Actionable(found)), "--degraded launches")
		})
	}
}

// TestLoad_EmptyFragmentsEntry_IsRefused: a bare `- ` fragments entry was
// already a hard decode error (FragmentRef refuses an empty name); the schema
// now names it too, at its location, before decoding refuses it.
func TestLoad_EmptyFragmentsEntry_IsRefused(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bad.yaml"), []byte("fragments:\n  - \n  - real\n"), 0o644))
	var found report.Collector
	_, err := NewLoader([]string{dir}, WithReporter(&found)).Load("bad")
	require.Error(t, err, "an empty fragment reference cannot be decoded")
	said := strings.Join(found.All().Fatal().Texts(), "\n")
	assert.Contains(t, said, "/fragments/0", "the schema finding names the entry")
}

// The control: a profile using every key correctly says NOTHING. Without this,
// the refusal tests above would also pass against a check that rejected
// everything.
func TestLoad_WellFormedProfile_IsSilent(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "good.yaml"), []byte(
		"description: fine\nllm: big\nparents: [base]\ntags: [t]\nselect_tags: [go]\n"+
			"bundles: [b]\nbundle_items: [\"b#fragments/x\"]\nfragments:\n  - plain\n  - name: pri\n    priority: 3\n"+
			"commands: [\"b#commands/c\"]\nskills: [\"b#skills/s\"]\nvariables:\n  K: v\n"+
			"exclude_fragments: [ef]\nexclude_mcp: [em]\ndeny_tools: [Bash]\n"+
			"hooks:\n  unified:\n    pre_tool:\n      - command: x\n        type: command\n"), 0o644))

	var out report.Collector
	_, err := NewLoader([]string{dir}, WithReporter(&out)).Load("good")
	require.NoError(t, err)
	assert.Empty(t, out.All(),
		"every key here is real; a gate that refuses a correct profile is worse than none")
}

// TestShippedProfiles_ValidateAgainstTheSchema loads every profile this
// repository ships or uses — the seed `ctxloom init` copies verbatim and the
// project's own — through the real loader. Refusing on violation means a
// shipped profile that drifted from the schema would refuse every launch that
// selects it, so the real files are asserted, not fixtures.
func TestShippedProfiles_ValidateAgainstTheSchema(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	var checked int
	for _, dir := range []string{
		filepath.Join(root, "resources", "profiles"),
		filepath.Join(root, ".ctxloom", "profiles"),
	} {
		files, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
		require.NoError(t, err)
		require.NotEmpty(t, files, "%s holds no profiles; this test would check nothing", dir)
		for _, f := range files {
			name := strings.TrimSuffix(filepath.Base(f), ".yaml")
			t.Run(filepath.Join(filepath.Base(filepath.Dir(dir)), name), func(t *testing.T) {
				var found report.Collector
				_, err := NewLoader([]string{dir}, WithReporter(&found)).Load(name)
				require.NoError(t, err)
				assert.Empty(t, found.All().Fatal(), "shipped profile %s violates profile-schema.json", f)
			})
			checked++
		}
	}
	require.NotZero(t, checked)
}

// TestLoad_UnknownKey_SuggestsTheNearKey: an unknown key's finding names the
// key the author most likely meant, wherever the misspelled key sits — at the
// top level, or inside a fragments entry that only matches one branch of a
// oneOf. Naming the offending key alone leaves the reader to diff it against
// the schema by eye.
func TestLoad_UnknownKey_SuggestsTheNearKey(t *testing.T) {
	cases := map[string]struct{ body, typo, meant string }{
		"top level":       {body: "select_tagz: [go]\nbundles: [real]\n", typo: "select_tagz", meant: "select_tags"},
		"fragments entry": {body: "fragments:\n  - name: pri\n    priorty: 3\n", typo: "priorty", meant: "priority"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, "bad.yaml"), []byte(tc.body), 0o644))
			var found report.Collector
			_, _ = NewLoader([]string{dir}, WithReporter(&found)).Load("bad")
			said := strings.Join(found.All().Fatal().Texts(), "\n")
			assert.Contains(t, said, tc.typo, "the finding names the unknown key")
			assert.Contains(t, said, "`"+tc.meant+"`", "the finding names the key the author meant")
		})
	}
}
