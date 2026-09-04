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

// setRootKey sets a single top-level key. The document root always exists, so
// unlike setServer in confpatch_test.go this needs no cur.Root().Member check
// for an absent parent container.
func setRootKey(key string, val any) Build {
	return func(doc *hew.Doc, cur hew.Document) (int, error) {
		p, err := hew.ParsePathIn(doc.Format(), "/"+key)
		if err != nil {
			return 0, err
		}
		doc.AtPath(p).Set(val)
		return 1, nil
	}
}

// setRootKeys batches setting TWO top-level keys against one Doc, in one
// Apply -- the exact shape reported broken: two brand-new sibling keys added
// to an empty object in a single batch.
func setRootKeys(key1 string, val1 any, key2 string, val2 any) Build {
	return func(doc *hew.Doc, cur hew.Document) (int, error) {
		p1, err := hew.ParsePathIn(doc.Format(), "/"+key1)
		if err != nil {
			return 0, err
		}
		doc.AtPath(p1).Set(val1)
		p2, err := hew.ParsePathIn(doc.Format(), "/"+key2)
		if err != nil {
			return 0, err
		}
		doc.AtPath(p2).Set(val2)
		return 2, nil
	}
}

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

// --- Defect 1: batched sibling adds emit invalid JSON (taskloom moonlit-sprawl) ---
//
// Reported: adding exactly TWO brand-new sibling keys to an empty object {}
// in one batch dropped the separating comma, producing
// {"hooks":{...} "statusLine":{...}} -- invalid JSON. Narrowed empirically:
// one key into {} is fine; two new keys alongside existing keys is fine;
// exactly two-new-into-{} is broken. All three shapes are asserted here so
// the test says which shapes work, not merely that one does.

func TestDefect1_OneNewKeyIntoEmptyObject(t *testing.T) {
	s, fs := newStore(t)
	const target = "/proj/one.json"
	require.NoError(t, afero.WriteFile(fs, target, []byte("{}"), 0o644))

	res, err := s.Apply(fs, target, setRootKey("hooks", map[string]any{
		"PreToolUse": []any{"x"},
	}))
	require.NoError(t, err)
	t.Logf("after: %s", res.After)

	require.True(t, json.Valid(res.After), "one key into {} must be valid JSON; got: %s", res.After)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(res.After, &doc))
	assert.Contains(t, doc, "hooks")
}

func TestDefect1_TwoNewKeysAlongsideExistingKey(t *testing.T) {
	s, fs := newStore(t)
	const target = "/proj/two_existing.json"
	require.NoError(t, afero.WriteFile(fs, target, []byte(`{"existing":"keep-me"}`), 0o644))

	res, err := s.Apply(fs, target, setRootKeys(
		"hooks", map[string]any{"PreToolUse": []any{"x"}},
		"statusLine", map[string]any{"type": "command", "command": "echo hi"},
	))
	require.NoError(t, err)
	t.Logf("after: %s", res.After)

	require.True(t, json.Valid(res.After), "two new keys alongside an existing key must be valid JSON; got: %s", res.After)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(res.After, &doc))
	assert.Contains(t, doc, "hooks")
	assert.Contains(t, doc, "statusLine")
	assert.Equal(t, "keep-me", doc["existing"], "the pre-existing sibling must survive untouched")
}

func TestDefect1_TwoNewKeysIntoEmptyObject(t *testing.T) {
	s, fs := newStore(t)
	const target = "/proj/two_empty.json"
	require.NoError(t, afero.WriteFile(fs, target, []byte("{}"), 0o644))

	res, err := s.Apply(fs, target, setRootKeys(
		"hooks", map[string]any{"PreToolUse": []any{"x"}},
		"statusLine", map[string]any{"type": "command", "command": "echo hi"},
	))
	// Log unconditionally, BEFORE any assertion that might FailNow: res.After
	// and res.Restored are populated by confpatch's closure before it returns
	// an error, so the invalid/corrupted bytes are the evidence even when
	// Apply itself errors.
	t.Logf("err: %v", err)
	t.Logf("after: %s", res.After)
	require.NoError(t, err, "Apply itself must not error")

	require.True(t, json.Valid(res.After),
		"batching two brand-new siblings into {} must emit valid JSON, not drop the separating comma; got: %s", res.After)

	var doc map[string]any
	require.NoError(t, json.Unmarshal(res.After, &doc), "must parse as JSON; got: %s", res.After)
	require.Contains(t, doc, "hooks")
	require.Contains(t, doc, "statusLine")

	hooks, ok := doc["hooks"].(map[string]any)
	require.True(t, ok, "hooks must be an object")
	assert.Contains(t, hooks, "PreToolUse")

	sl, ok := doc["statusLine"].(map[string]any)
	require.True(t, ok, "statusLine must be an object")
	assert.Equal(t, "command", sl["type"])
	assert.Equal(t, "echo hi", sl["command"])
}

// --- Defect 2: reversal is context-sensitive across disjoint paths, and
// corrupts (taskloom moonlit-sprawl) ---
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
