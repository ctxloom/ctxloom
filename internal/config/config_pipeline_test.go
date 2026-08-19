package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// These cover the PIPELINE itself rather than any one migration: that it stamps
// the current version, that it is a no-op on an already-current config, and
// that it refuses to touch a document it cannot read. They stay in package
// config, and outlive every per-version package under migrate/, because they
// are properties of the frame and not of any source version.

// runConfigUpgrades drives the assembled pipeline over raw bytes, mirroring
// what loadConfigFile does on load.
func runConfigUpgrades(in string) (root map[string]any, applied []string) {
	out, applied := newConfigUpgrades(nil).Run([]byte(in))
	_ = yaml.Unmarshal(out, &root)
	return root, applied
}

func TestConfigUpgrades_StampsCurrentVersion(t *testing.T) {
	// Even an already-key-correct but unversioned config upgrades by gaining the
	// current schema version stamp.
	out, applied := newConfigUpgrades(nil).Run([]byte("llm:\n  configs:\n    claude-code: { type: claude-code }\n"))
	require.NotEmpty(t, applied, "unversioned config must upgrade (stamp version)")
	assert.Contains(t, string(out), "version: 6")

	var root map[string]any
	require.NoError(t, yaml.Unmarshal(out, &root))
	assert.Equal(t, CurrentConfigVersion, root["version"])
}

func TestConfigUpgrades_NoOpWhenCurrent(t *testing.T) {
	// A config already at the current version is returned verbatim (no rewrite).
	in := []byte("version: 6\nllm:\n  defaults:\n    primary: claude-code\n")
	out, applied := newConfigUpgrades(nil).Run(in)
	assert.Empty(t, applied)
	assert.Equal(t, string(in), string(out), "current config must not be reserialized")
}

func TestConfigUpgrades_MalformedYAMLUnchanged(t *testing.T) {
	in := []byte("llm: [unterminated\n")
	out, applied := newConfigUpgrades(nil).Run(in)
	assert.Empty(t, applied)
	assert.Equal(t, in, out)
}

// The collision branch — a hand-authored agents.default already exists, so
// profiles.defaults cannot be synthesized into it — DELETES the user's
// default profile list and says nothing. That is an irreversible on-disk
// loss (the migration rewrites the file), and the next run silently launches
// with a different profile set than the one the user configured.
//
// migrateLLMv3 already does the right thing for its own lossy branch:
// recordMigrationWarning, surfaced as WarnKindMigrationLossy, fatal in strict
// mode. This branch is the same class of loss and must be reported the same
// way.

// A config whose `version:` key is present but not an integer is not a
// pre-versioning document — it is a document whose version cannot be read, and
// the two must not be treated the same. Reading it as generation 0 re-ran every
// migration from the start over a file that is probably corrupt, and stamped
// the current version on the way out: the loud "cannot unmarshal `banana` into
// int" the caller would have got is replaced by a clean parse of a rewritten
// file the user is then prompted to persist.
func TestConfigUpgrades_UnreadableVersion_AppliesNothing(t *testing.T) {
	for _, in := range []string{
		"version: banana\nllm:\n  defaults:\n    primary: claude-code\n",
		"version: 6.5\nllm:\n  defaults:\n    primary: claude-code\n",
		"version:\n  nested: 6\nllm:\n  defaults:\n    primary: claude-code\n",
	} {
		out, applied := newConfigUpgrades(nil).Run([]byte(in))
		assert.Empty(t, applied, "input %q", in)
		assert.Equal(t, in, string(out), "input %q must survive verbatim", in)
	}
}
