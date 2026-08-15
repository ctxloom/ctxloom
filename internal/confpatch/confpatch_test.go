package confpatch

import (
	"strings"
	"testing"

	hew "github.com/benjaminabbitt/hew/go"
	_ "github.com/benjaminabbitt/hew/go/ext/json"
	_ "github.com/benjaminabbitt/hew/go/ext/toml"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	yamlv3 "gopkg.in/yaml.v3"
)

// foreign is a .mcp.json as a USER would author it: a top-level key ctxloom
// models nowhere ($schema), and a REMOTE server whose type/url/headers shape
// ctxloom models nowhere either. Every assertion below is about these bytes
// surviving — that is the defect this package exists to fix.
const foreign = `{
  "$schema": "https://example.com/mcp.schema.json",
  "mcpServers": {
    "remote-thing": {
      "type": "http",
      "url": "https://mcp.example.com/v1",
      "headers": {"Authorization": "Bearer abc123"}
    }
  }
}`

// want builds the transform list a caller hands to Apply: set one server entry.
func want(t *testing.T, src []byte, name string, entry map[string]any) hew.TransformList {
	t.Helper()
	doc, err := hew.OpenBytes("mcp.json", src, hew.As(hew.FormatJSON))
	require.NoError(t, err)
	p, err := hew.ParsePathIn(hew.FormatJSON, "/mcpServers/"+name)
	require.NoError(t, err)
	doc.AtPath(p).Set(entry)
	tl, err := doc.Transforms()
	require.NoError(t, err)
	return tl
}

func newStore(t *testing.T) (*Store, afero.Fs) {
	t.Helper()
	fs := afero.NewMemMapFs()
	s, err := NewStore(fs, "/home/u/.ctxloom/records")
	require.NoError(t, err)
	return s, fs
}

func TestApplyPreservesForeignContent(t *testing.T) {
	s, fs := newStore(t)
	const target = "/proj/mcp.json"
	require.NoError(t, afero.WriteFile(fs, target, []byte(foreign), 0o644))

	tl := want(t, []byte(foreign), "ctxloom", map[string]any{"command": "ctxloom", "args": []any{"mcp", "serve"}})
	res, err := s.Apply(fs, target, tl)
	require.NoError(t, err)
	assert.True(t, res.Changed)
	assert.False(t, res.Reversed, "there was no prior record to reverse")

	out, err := afero.ReadFile(fs, target)
	require.NoError(t, err)
	got := string(out)

	// The user's own bytes, verbatim — not merely "a $schema key exists".
	assert.Contains(t, got, `"$schema": "https://example.com/mcp.schema.json"`)
	assert.Contains(t, got, `"url": "https://mcp.example.com/v1"`)
	assert.Contains(t, got, `"headers": {"Authorization": "Bearer abc123"}`)
	assert.NotContains(t, got, `"command": ""`, "ctxloom must not invent a command on a server it does not manage")
	// And ctxloom's own entry landed.
	assert.Contains(t, got, `"ctxloom"`)
	assert.Contains(t, got, `"serve"`)
}

// The core claim: a SECOND write takes the first write's entry back out before
// applying the new one, leaving the user's file with exactly one ctxloom entry
// and their own content untouched.
func TestSecondApplyReversesTheFirst(t *testing.T) {
	s, fs := newStore(t)
	const target = "/proj/mcp.json"
	require.NoError(t, afero.WriteFile(fs, target, []byte(foreign), 0o644))

	first, err := s.Apply(fs, target, want(t, []byte(foreign), "ctxloom", map[string]any{"command": "old-binary"}))
	require.NoError(t, err)
	require.NotEmpty(t, first.RecordPath)

	mid, err := afero.ReadFile(fs, target)
	require.NoError(t, err)
	require.Contains(t, string(mid), "old-binary")

	second, err := s.Apply(fs, target, want(t, mid, "ctxloom", map[string]any{"command": "new-binary"}))
	require.NoError(t, err)
	assert.True(t, second.Reversed, "the prior application must have been reversed")

	out, err := afero.ReadFile(fs, target)
	require.NoError(t, err)
	got := string(out)

	assert.NotContains(t, got, "old-binary", "the previous ctxloom entry must be gone, not merged over")
	assert.Contains(t, got, "new-binary")
	assert.Contains(t, got, `"$schema": "https://example.com/mcp.schema.json"`)
	assert.Contains(t, got, `"headers": {"Authorization": "Bearer abc123"}`)

	// Reversing then re-applying must not accumulate: exactly one ctxloom key.
	assert.Equal(t, 1, strings.Count(got, `"ctxloom"`), "ctxloom must appear once, not once per write")
}

// The restored image must be the user's file EXACTLY — byte-for-byte, not
// "close enough". This is the assertion that would catch a reformatting applier.
func TestReversalRestoresTheUsersBytesExactly(t *testing.T) {
	s, fs := newStore(t)
	const target = "/proj/mcp.json"
	require.NoError(t, afero.WriteFile(fs, target, []byte(foreign), 0o644))

	_, err := s.Apply(fs, target, want(t, []byte(foreign), "ctxloom", map[string]any{"command": "x"}))
	require.NoError(t, err)

	after, err := afero.ReadFile(fs, target)
	require.NoError(t, err)

	second, err := s.Apply(fs, target, want(t, after, "ctxloom", map[string]any{"command": "y"}))
	require.NoError(t, err)

	require.NotEmpty(t, second.Restored, "comparing against an empty restored image would be trivially true")
	assert.Equal(t, foreign, string(second.Restored),
		"reversing ctxloom's application must reproduce the user's file byte-for-byte")
}

func TestApplyWritesOneRecordCarryingAParseableReversal(t *testing.T) {
	s, fs := newStore(t)
	const target = "/proj/mcp.json"
	require.NoError(t, afero.WriteFile(fs, target, []byte(foreign), 0o644))

	res, err := s.Apply(fs, target, want(t, []byte(foreign), "ctxloom", map[string]any{"command": "x"}))
	require.NoError(t, err)

	entries, err := afero.ReadDir(fs, "/home/u/.ctxloom/records")
	require.NoError(t, err)
	assert.Len(t, entries, 1, "exactly one record per apply")

	data, err := afero.ReadFile(fs, res.RecordPath)
	require.NoError(t, err)
	var rec Record
	require.NoError(t, yamlv3.Unmarshal(data, &rec))

	require.NotEmpty(t, rec.Reversal, "a record with no reversal cannot undo anything")
	// The stored reversal must be a REAL patch: hew's own parser has to accept
	// it, and it has to name the entry it would remove.
	tl, err := hew.ParseSingle(rec.Reversal)
	require.NoError(t, err, "the stored reversal must be parseable by hew")
	assert.NotEmpty(t, tl.Transform, "a reversal with no transforms undoes nothing")
	assert.Contains(t, string(rec.Reversal), "ctxloom")

	require.Len(t, rec.Targets, 1)
	assert.Equal(t, target, rec.Targets[0].Target)
	assert.Equal(t, "json", rec.Targets[0].Format)
	assert.NotEqual(t, rec.Targets[0].Before, rec.Targets[0].After, "before and after digests must differ on a real change")
}

// Drift must REFUSE, not clobber. If the user edits the region ctxloom manages,
// the stored reversal no longer fits, and writing anyway would destroy their
// edit.
func TestDriftRefusesAndLeavesTheTargetUntouched(t *testing.T) {
	s, fs := newStore(t)
	const target = "/proj/mcp.json"
	require.NoError(t, afero.WriteFile(fs, target, []byte(foreign), 0o644))

	_, err := s.Apply(fs, target, want(t, []byte(foreign), "ctxloom", map[string]any{"command": "x"}))
	require.NoError(t, err)

	// The user edits ctxloom's entry by hand.
	edited := strings.Replace(mustRead(t, fs, target), `"command": "x"`, `"command": "MINE"`, 1)
	require.Contains(t, edited, "MINE", "the fixture must actually have been edited")
	require.NoError(t, afero.WriteFile(fs, target, []byte(edited), 0o644))

	_, err = s.Apply(fs, target, want(t, []byte(edited), "ctxloom", map[string]any{"command": "z"}))
	require.Error(t, err, "a drifted target must be refused")
	assert.Contains(t, err.Error(), "drifted")

	assert.Equal(t, edited, mustRead(t, fs, target),
		"a refused write must leave the user's file exactly as they left it")
}

// Creating a missing target: the caller must build ops that create the PARENT
// too. Setting a nested path whose parent is absent is HEW013 no-match — the
// fluent API has no create-or-open yet (hew task reclusive-grazing), so a
// caller writing a file from nothing states the whole container.
func TestApplyCreatesAMissingTarget(t *testing.T) {
	s, fs := newStore(t)
	const target = "/proj/nested/mcp.json"

	doc, err := hew.OpenBytes("mcp.json", []byte("{}"), hew.As(hew.FormatJSON))
	require.NoError(t, err)
	p, err := hew.ParsePathIn(hew.FormatJSON, "/mcpServers")
	require.NoError(t, err)
	doc.AtPath(p).Set(map[string]any{"ctxloom": map[string]any{"command": "x"}})
	tl, err := doc.Transforms()
	require.NoError(t, err)

	res, err := s.Apply(fs, target, tl)
	require.NoError(t, err)
	assert.True(t, res.Changed)

	got := mustRead(t, fs, target)
	assert.Contains(t, got, `"ctxloom"`)
	assert.NotEmpty(t, res.RecordPath, "creating a file is an application and must be recorded")
}

func TestApplyRefusesAFormatHewCannotName(t *testing.T) {
	s, fs := newStore(t)
	_, err := s.Apply(fs, "/proj/settings.unknownext", hew.TransformList{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not recognize")
}

func mustRead(t *testing.T, fs afero.Fs, path string) string {
	t.Helper()
	b, err := afero.ReadFile(fs, path)
	require.NoError(t, err)
	require.NotEmpty(t, b, "reading an empty file would make the comparison trivially true")
	return string(b)
}
