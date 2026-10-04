package profiles

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// invalidProfiles are documents profile-schema.json rejects but yaml.v3 decodes
// without complaint — the shapes that used to load silently. The value is the
// text the refusal must carry to name the violation.
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
	"empty fragments entry": {
		body:  "fragments:\n  - \n  - real\n",
		names: "/fragments/0",
	},
}

// TestDecode_SchemaViolationIsRefused: every violation of the declared profile
// schema refuses the document, naming what is wrong and where. Decode is the
// one decoder every bundle's profile item goes through, so a bundle carrying
// such a profile does not load.
func TestDecode_SchemaViolationIsRefused(t *testing.T) {
	for name, tc := range invalidProfiles {
		t.Run(name, func(t *testing.T) {
			p, err := Decode([]byte(tc.body))
			require.ErrorIs(t, err, errProfileSchema)
			assert.Contains(t, err.Error(), tc.names, "the refusal names the violation")
			assert.Nil(t, p)
		})
	}
}

// The control: a profile using every key correctly decodes. Without this, the
// refusal test above would also pass against a check that rejected everything.
func TestDecode_WellFormedProfile(t *testing.T) {
	p, err := Decode([]byte(
		"description: fine\nllm: big\nparents: [base]\ntags: [t]\nselect_tags: [go]\n" +
			"bundles: [b]\nbundle_items: [\"b#fragments/x\"]\nfragments:\n  - plain\n  - name: pri\n    priority: 3\n" +
			"commands: [\"b#commands/c\"]\nskills: [\"b#skills/s\"]\nvariables:\n  K: v\n" +
			"exclude_fragments: [ef]\nexclude_mcp: [em]\ndeny_tools: [Bash]\n" +
			"hooks:\n  unified:\n    pre_tool:\n      - command: x\n        type: command\n"))
	require.NoError(t, err, "every key here is real; a gate that refuses a correct profile is worse than none")
	assert.Equal(t, "big", p.LLM)
}

// TestShippedProfiles_ValidateAgainstTheSchema decodes every profile this
// repository ships or uses — the seed `ctxloom init` copies verbatim and the
// project bundle's own — through Decode. A shipped profile that drifted from
// the schema would refuse to load, so the real files are asserted, not
// fixtures.
func TestShippedProfiles_ValidateAgainstTheSchema(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	var checked int
	for _, dir := range []string{
		filepath.Join(root, "resources", "profiles"),
		filepath.Join(root, ".ctxloom", "content", "bundles", "v2", "project", "profiles"),
	} {
		files, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
		require.NoError(t, err)
		require.NotEmpty(t, files, "%s holds no profiles; this test would check nothing", dir)
		for _, f := range files {
			t.Run(f, func(t *testing.T) {
				data, err := os.ReadFile(f)
				require.NoError(t, err)
				_, err = Decode(data)
				assert.NoError(t, err, "shipped profile %s violates profile-schema.json", f)
			})
			checked++
		}
	}
	require.NotZero(t, checked)
}

// TestDecode_UnknownKey_SuggestsTheNearKey: an unknown key's refusal names the
// key the author most likely meant, wherever the misspelled key sits — at the
// top level, or inside a fragments entry that only matches one branch of a
// oneOf. Naming the offending key alone leaves the reader to diff it against
// the schema by eye.
func TestDecode_UnknownKey_SuggestsTheNearKey(t *testing.T) {
	cases := map[string]struct{ body, typo, meant string }{
		"top level":       {body: "select_tagz: [go]\nbundles: [real]\n", typo: "select_tagz", meant: "select_tags"},
		"fragments entry": {body: "fragments:\n  - name: pri\n    priorty: 3\n", typo: "priorty", meant: "priority"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Decode([]byte(tc.body))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.typo, "the refusal names the unknown key")
			assert.Contains(t, err.Error(), "`"+tc.meant+"`", "the refusal names the key the author meant")
		})
	}
}
