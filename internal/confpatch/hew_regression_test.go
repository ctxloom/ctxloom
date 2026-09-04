package confpatch

import (
	"encoding/json"
	"path/filepath"
	"testing"

	hew "github.com/benjaminabbitt/hew/go"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setPath sets a value at an arbitrary path whose parent already exists in
// the fixture being applied against.
func setPath(path string, val any) Build {
	return func(doc *hew.Doc, cur hew.Document) (int, error) {
		p, err := hew.ParsePathIn(doc.Format(), path)
		if err != nil {
			return 0, err
		}
		doc.AtPath(p).Set(val)
		return 1, nil
	}
}

// surgicalSet performs a plain hew edit directly against the target, bypassing
// Store entirely -- what an independent second writer does. No record is
// written.
func surgicalSet(t *testing.T, fs afero.Fs, target, path string, val any) {
	t.Helper()
	before, err := afero.ReadFile(fs, target)
	require.NoError(t, err)
	format, ok := hew.DetectFormat(filepath.Base(target))
	require.True(t, ok)
	doc, err := hew.OpenBytes(target, before, hew.As(format))
	require.NoError(t, err)
	p, err := hew.ParsePathIn(doc.Format(), path)
	require.NoError(t, err)
	doc.AtPath(p).Set(val)
	after, err := doc.Bytes()
	require.NoError(t, err)
	require.NoError(t, afero.WriteFile(fs, target, after, 0o644))
}

// Defect 2 from taskloom moonlit-sprawl: reversal is context-sensitive
// across disjoint paths, and corrupts.
//
// Reported: seed settings.json with a hooks entry; apply a hooks change
// through confpatch; insert a /statusLine sibling with a plain surgical hew
// edit (an independent second writer); apply ANOTHER hooks change through
// confpatch. The Restored document confpatch computed contained duplicated
// entries wrapped in spurious nested arrays inside the hooks array. This
// triggers only when a tracked value is modified repeatedly while something
// else touches a sibling key in between.
//
// The assertion is on the RESTORED and final DOCUMENT CONTENT, never on
// err == nil: this defect produces corrupted bytes silently, not a refusal,
// so a bare error check would pass against the broken library and prove
// nothing.
//
// STILL RED at github.com/benjaminabbitt/hew/go
// v0.1.1-0.20260904151914-916fbee740f4 -- the "reparse before each
// transform" upstream fix (feat/sequential-resolution) fixed the sibling
// taskloom moonlit-sprawl's other defect (batched two-new-siblings-into-{}
// dropping a separator) but NOT this one: the restored document still
// carries the un-reversed prior entry plus two duplicate entries wrapped in
// spurious nested arrays, byte-for-byte the same corruption as on the
// previous pin. Do not delete this test on a green run alone -- it is red on
// both pins tried so far. Keep it until it goes green against a pin that
// actually fixes it.
//
// A companion defect (batched sibling adds emit invalid JSON) was
// reproduced alongside this one and went GREEN on the pin above; that test
// was removed once confirmed passing, since it exercised hew's own fixed
// behaviour rather than ctxloom's, and hew's own branch already carries
// test(sequential-resolution) and test(corpus) coverage for it.
func TestDefect2_ReversalCorruptedByDisjointSiblingWrite(t *testing.T) {
	s, fs := newStore(t)
	const target = "/proj/settings.json"

	const seed = `{
  "hooks": {
    "PreToolUse": [
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "echo seed"}]}
    ]
  }
}`
	require.NoError(t, afero.WriteFile(fs, target, []byte(seed), 0o644))

	preToolUse := func(cmd string) []any {
		return []any{
			map[string]any{
				"matcher": "Bash",
				"hooks": []any{
					map[string]any{"type": "command", "command": cmd},
				},
			},
		}
	}

	// 1. Apply a hooks change through confpatch.
	_, err := s.Apply(fs, target, setPath("/hooks/PreToolUse", preToolUse("echo first")))
	require.NoError(t, err)

	// 2. An independent second writer touches a disjoint sibling path
	// directly with hew -- NOT through confpatch, so no record is kept.
	surgicalSet(t, fs, target, "/statusLine", map[string]any{
		"type": "command", "command": "echo status",
	})
	midway := mustRead(t, fs, target)
	require.Contains(t, midway, "echo status", "the surgical writer's edit must actually have landed")

	// 3. Apply ANOTHER hooks change through confpatch. This must reverse the
	// FIRST confpatch application against a document that has since gained an
	// unrelated sibling key.
	res, err := s.Apply(fs, target, setPath("/hooks/PreToolUse", preToolUse("echo second")))
	// Log unconditionally, BEFORE require.NoError: res.Restored is populated
	// by confpatch's closure before an internal error return, so a corrupted
	// restored document is visible here even when the second Apply itself
	// errors while lowering/applying the build against that corruption.
	t.Logf("err: %v", err)
	t.Logf("restored: %s", res.Restored)
	t.Logf("after: %s", res.After)
	require.NoError(t, err)

	// Assert the RESTORED document's content, not merely that Apply
	// succeeded.
	require.True(t, json.Valid(res.Restored), "restored document must be valid JSON; got: %s", res.Restored)

	var restored settingsFixture
	require.NoError(t, json.Unmarshal(res.Restored, &restored),
		"restored hooks must decode into the real shape, not a nested array; got: %s", res.Restored)

	require.Len(t, restored.Hooks["PreToolUse"], 1, "exactly one matcher entry, not duplicated; got: %s", res.Restored)
	require.Len(t, restored.Hooks["PreToolUse"][0].Hooks, 1, "got: %s", res.Restored)
	assert.Equal(t, "echo seed", restored.Hooks["PreToolUse"][0].Hooks[0].Command,
		"restored means the FIRST confpatch application reversed back to the seed, not corrupted")

	// The final on-disk document must ALSO be sane: the second hooks change
	// landed, and the independent writer's statusLine survived untouched.
	final := mustRead(t, fs, target)
	require.True(t, json.Valid([]byte(final)), "final document must be valid JSON; got: %s", final)

	var afterDoc settingsFixture
	require.NoError(t, json.Unmarshal([]byte(final), &afterDoc),
		"final document must decode into the real shape; got: %s", final)
	require.Len(t, afterDoc.Hooks["PreToolUse"], 1, "exactly one matcher entry after the second write; got: %s", final)
	assert.Equal(t, "echo second", afterDoc.Hooks["PreToolUse"][0].Hooks[0].Command)
	require.NotNil(t, afterDoc.StatusLine, "the independent writer's statusLine must survive")
	assert.Equal(t, "echo status", afterDoc.StatusLine["command"])
}

type hookEntryFixture struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}

type hookMatcherFixture struct {
	Matcher string             `json:"matcher"`
	Hooks   []hookEntryFixture `json:"hooks"`
}

type settingsFixture struct {
	Hooks      map[string][]hookMatcherFixture `json:"hooks"`
	StatusLine map[string]any                  `json:"statusLine"`
}
