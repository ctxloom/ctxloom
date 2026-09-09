package confpatch

import (
	"strings"
	"testing"

	hew "github.com/benjaminabbitt/hew/go"
	_ "github.com/benjaminabbitt/hew/go/ext/json"
	_ "github.com/benjaminabbitt/hew/go/ext/toml"
	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/collections"
	"github.com/ctxloom/ctxloom/internal/testsupport"
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

// setServer is the Build a caller hands to Apply: put one server entry under
// /mcpServers.
//
// Addressing /mcpServers/<name> against a file with no /mcpServers is HEW013
// no-match, so the container is stated whole when it is absent — that is what
// the read view is for.
func setServer(name string, entry map[string]any) Build {
	return func(doc *hew.Doc, cur hew.Document) (int, error) {
		if _, ok := cur.Root().Member("mcpServers"); !ok {
			p, err := hew.ParsePathIn(doc.Format(), "/mcpServers")
			if err != nil {
				return 0, err
			}
			doc.AtPath(p).Set(map[string]any{name: entry})
			return 1, nil
		}
		p, err := hew.ParsePathIn(doc.Format(), "/mcpServers/"+name)
		if err != nil {
			return 0, err
		}
		doc.AtPath(p).Set(entry)
		return 1, nil
	}
}

// recordNothing is the deliberate no-op: ctxloom's desired set is empty, so the
// reversal alone is the whole change.
func recordNothing() Build {
	return func(*hew.Doc, hew.Document) (int, error) { return 0, nil }
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

	tl := setServer("ctxloom", map[string]any{"command": "ctxloom", "args": []any{"mcp", "serve"}})
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

	first, err := s.Apply(fs, target, setServer("ctxloom", map[string]any{"command": "old-binary"}))
	require.NoError(t, err)
	require.NotEmpty(t, first.RecordPath)

	mid, err := afero.ReadFile(fs, target)
	require.NoError(t, err)
	require.Contains(t, string(mid), "old-binary")

	second, err := s.Apply(fs, target, setServer("ctxloom", map[string]any{"command": "new-binary"}))
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

	_, err := s.Apply(fs, target, setServer("ctxloom", map[string]any{"command": "x"}))
	require.NoError(t, err)

	second, err := s.Apply(fs, target, setServer("ctxloom", map[string]any{"command": "y"}))
	require.NoError(t, err)

	require.NotEmpty(t, second.Restored, "comparing against an empty restored image would be trivially true")
	assert.Equal(t, foreign, string(second.Restored),
		"reversing ctxloom's application must reproduce the user's file byte-for-byte")
}

func TestApplyWritesOneRecordCarryingAParseableReversal(t *testing.T) {
	s, fs := newStore(t)
	const target = "/proj/mcp.json"
	require.NoError(t, afero.WriteFile(fs, target, []byte(foreign), 0o644))

	res, err := s.Apply(fs, target, setServer("ctxloom", map[string]any{"command": "x"}))
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
	tl, err := hew.ParseSingle([]byte(rec.Reversal))
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

	_, err := s.Apply(fs, target, setServer("ctxloom", map[string]any{"command": "x"}))
	require.NoError(t, err)

	// The user edits ctxloom's entry by hand.
	edited := strings.Replace(mustRead(t, fs, target), `"command": "x"`, `"command": "MINE"`, 1)
	require.Contains(t, edited, "MINE", "the fixture must actually have been edited")
	require.NoError(t, afero.WriteFile(fs, target, []byte(edited), 0o644))

	_, err = s.Apply(fs, target, setServer("ctxloom", map[string]any{"command": "z"}))
	require.Error(t, err, "a drifted target must be refused")
	assert.Contains(t, err.Error(), "drifted")

	assert.Equal(t, edited, mustRead(t, fs, target),
		"a refused write must leave the user's file exactly as they left it")
}

// Creating a missing target: the caller must build ops that create the PARENT
// too. Setting a nested path whose parent is absent is HEW013 no-match, and
// ops within one Doc do not compose — each resolves against the document as
// opened, so a container an earlier op created is invisible to a later one
// addressing into it. A caller writing a file from nothing therefore states
// the whole container in a single op.
func TestApplyCreatesAMissingTarget(t *testing.T) {
	s, fs := newStore(t)
	const target = "/proj/nested/mcp.json"

	res, err := s.Apply(fs, target, setServer("ctxloom", map[string]any{"command": "x"}))
	require.NoError(t, err)
	assert.True(t, res.Changed)

	got := mustRead(t, fs, target)
	assert.Contains(t, got, `"ctxloom"`)
	assert.NotEmpty(t, res.RecordPath, "creating a file is an application and must be recorded")
}

// The empty desired set: ctxloom wants no servers at all, so the reversal alone
// is the change. The user's file must come back exactly as they wrote it.
func TestEmptyDesiredSetRemovesCtxloomAndRestoresTheUser(t *testing.T) {
	s, fs := newStore(t)
	const target = "/proj/mcp.json"
	require.NoError(t, afero.WriteFile(fs, target, []byte(foreign), 0o644))

	_, err := s.Apply(fs, target, setServer("ctxloom", map[string]any{"command": "x"}))
	require.NoError(t, err)
	require.Contains(t, mustRead(t, fs, target), "ctxloom")

	res, err := s.Apply(fs, target, recordNothing())
	require.NoError(t, err)
	assert.True(t, res.Reversed)

	assert.Equal(t, foreign, mustRead(t, fs, target),
		"with nothing desired, the user is left with exactly the file they wrote")
}

// A write that changes nothing writes nothing.
func TestUnchangedApplyWritesNoNewRecord(t *testing.T) {
	s, fs := newStore(t)
	const target = "/proj/mcp.json"
	require.NoError(t, afero.WriteFile(fs, target, []byte(foreign), 0o644))

	_, err := s.Apply(fs, target, setServer("ctxloom", map[string]any{"command": "x"}))
	require.NoError(t, err)
	before := mustRead(t, fs, target)

	res, err := s.Apply(fs, target, setServer("ctxloom", map[string]any{"command": "x"}))
	require.NoError(t, err)
	assert.False(t, res.Changed, "re-applying the same set changes nothing")
	assert.Empty(t, res.RecordPath, "an unchanged apply must not grow the audit trail")

	entries, err := afero.ReadDir(fs, "/home/u/.ctxloom/records")
	require.NoError(t, err)
	assert.Len(t, entries, 1, "still exactly one record")
	assert.Equal(t, before, mustRead(t, fs, target))
}

// A target that no longer EXISTS has not drifted. The record is home-rooted and
// outlives the file it describes, so a regenerated target directory (`profile
// materialize --target out`) routinely presents an absent file against a live
// record. Reversing into the empty document stands in for it fails HEW010
// no-match, and ctxloom used to turn that into a refusal to write at all —
// exit 3, "aborting startup", with a fix: line that re-runs the same failure.
//
// Nothing can be clobbered in a file that is not there: the previous
// application is already reversed, vacuously, so the apply proceeds forward.
func TestApplyRecreatesATargetDeletedSinceTheRecordWasWritten(t *testing.T) {
	s, fs := newStore(t)
	const target = "/proj/mcp.json"
	require.NoError(t, afero.WriteFile(fs, target, []byte(foreign), 0o644))

	_, err := s.Apply(fs, target, setServer("ctxloom", map[string]any{"command": "x"}))
	require.NoError(t, err)

	// The target directory is regenerated out from under the record.
	require.NoError(t, fs.Remove(target))

	res, err := s.Apply(fs, target, setServer("ctxloom", map[string]any{"command": "x"}))
	require.NoError(t, err, "an absent target must not read as drift")
	assert.True(t, res.Changed)
	assert.False(t, res.Reversed, "there was nothing on disk to reverse out of")

	got := mustRead(t, fs, target)
	assert.Contains(t, got, `"ctxloom"`, "the desired set is written afresh")
	assert.NotContains(t, got, "taskloom",
		"the deleted file's foreign content is NOT resurrected from the record")
}

// The record must survive the round trip for the shape ctxloom ACTUALLY writes:
// an MCP server body carries an `args` ARRAY, not just scalars. Store.Last is
// the production read path — every Apply calls it to find the prior
// application — so a record that cannot be re-read turns the SECOND write to a
// target into a refusal, permanently. Observed against a real .mcp.json:
// "parse record ...: cannot unmarshal !!seq into yaml.Node", after which
// ctxloom declined to manage the file at all.
func TestRecordRoundTripsAnArrayValued(t *testing.T) {
	s, fs := newStore(t)
	const target = "/proj/mcp.json"
	require.NoError(t, afero.WriteFile(fs, target, []byte(foreign), 0o644))

	// Seed the server, so the NEXT apply changes fields in place rather than
	// adding a whole mapping. That distinction is the whole bug: an inverse
	// built from a whole-entry add is a MAPPING, which decodes into a *Node
	// fine; an inverse built from changed leaves is scalars and sequences,
	// which does not.
	_, err := s.Apply(fs, target, setServer("ctxloom", map[string]any{
		"command": "/old/ctxloom",
		"args":    []any{"mcp", "serve"},
	}))
	require.NoError(t, err)

	// Change the leaves. This record's inverse carries `value: "/old/ctxloom"`
	// and `value: ["mcp", "serve"]` — a scalar and a sequence.
	_, err = s.Apply(fs, target, setServer("ctxloom", map[string]any{
		"command": "/new/ctxloom",
		"args":    []any{"mcp", "serve", "--verbose"},
	}))
	require.NoError(t, err)

	rec, found, err := s.Last(target)
	require.NoError(t, err, "confpatch must be able to re-read the record it just wrote")
	require.True(t, found)
	require.NotEmpty(t, rec.Reversal)

	// FIDELITY, not just absence-of-error. Through a *yaml.Node field a MAPPING
	// value decodes with no error at all and yields null — the silent half of
	// this bug, and the half a "did Last() return an error?" assertion sails
	// straight past. Re-marshal what came back and require the content.
	var sawValue bool
	for _, op := range rec.Targets[0].Inverse {
		if op.Value.IsZero() {
			continue
		}
		sawValue = true
		round, merr := yamlv3.Marshal(&op.Value)
		require.NoError(t, merr)
		assert.NotEqual(t, "null\n", string(round),
			"op %s %s: the record's value read back as null — it was silently dropped", op.Op, op.Path)
	}
	require.True(t, sawValue, "fixture must produce at least one valued inverse op, or it proves nothing")

	// And the write that has to re-read that record must still go through.
	_, err = s.Apply(fs, target, setServer("ctxloom", map[string]any{
		"command": "/newer/ctxloom",
		"args":    []any{"mcp", "serve"},
	}))
	require.NoError(t, err, "a write that must re-read a valued record is refused")
	assert.Contains(t, mustRead(t, fs, target), "/newer/ctxloom")
	assert.NotContains(t, mustRead(t, fs, target), "/old/ctxloom")
}

// The record must say WHICH add it was. hew's OP-02/03/04 all resolve to
// `add`, and they differ only by on_conflict: fail, replace, keep. A record
// that drops it says "something was added here" and cannot answer whether
// ctxloom meant to overwrite a user's value, seed one, or refuse — which is the
// difference between the audit statement §9.7 asks for and a note.
//
// hew.ResolvedOp did not always carry OnConflict; this asserts ctxloom reads
// the field rather than discarding it.
func TestRecordCarriesTheAddPolicy(t *testing.T) {
	s, fs := newStore(t)
	const target = "/proj/mcp.json"
	require.NoError(t, afero.WriteFile(fs, target, []byte(foreign), 0o644))

	// setServer uses Sel.Set — OP-03 upsert, add + on_conflict: replace.
	_, err := s.Apply(fs, target, setServer("ctxloom", map[string]any{"command": "x"}))
	require.NoError(t, err)

	rec, found, err := s.Last(target)
	require.NoError(t, err)
	require.True(t, found)
	require.NotEmpty(t, rec.Targets[0].Transforms)

	var sawAdd bool
	for _, op := range rec.Targets[0].Transforms {
		if op.Op != "add" {
			continue
		}
		sawAdd = true
		assert.Equal(t, "replace", op.OnConflict,
			"an upsert recorded as a bare `add` cannot be told from a fail-if-present add")
	}
	require.True(t, sawAdd, "fixture must record an add, or it proves nothing")
}

func TestApplyRefusesAFormatHewCannotName(t *testing.T) {
	s, fs := newStore(t)
	_, err := s.Apply(fs, "/proj/settings.unknownext", recordNothing())
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

// The companion to TestDriftRefusesAndLeavesTheTargetUntouched, and the line
// between them is the whole point: the refusal exists to protect a user's edit
// to the region ctxloom MANAGES, and it must not extend one inch past that.
//
// A user editing their OWN server — content ctxloom never wrote and has no
// business in — used to wedge the file permanently: the stored reversal
// asserted the neighbouring member as CONTEXT (hew's §9.4-R2 sibling radius
// defaults to 1), so it no longer applied, and every later write refused with
// a drift error naming /mcpServers/remote-thing — a path ctxloom does not own.
func TestForeignEditDoesNotRefuseAndSurvivesTheWrite(t *testing.T) {
	s, fs := newStore(t)
	const target = "/proj/mcp.json"
	require.NoError(t, afero.WriteFile(fs, target, []byte(foreign), 0o644))

	_, err := s.Apply(fs, target, setServer("ctxloom", map[string]any{"command": "x"}))
	require.NoError(t, err)

	// The user edits THEIR OWN server, inside a neighbouring entry.
	const edit = "https://mcp.example.com/v2-EDITED"
	edited := strings.Replace(mustRead(t, fs, target), "https://mcp.example.com/v1", edit, 1)
	require.Contains(t, edited, edit, "the fixture must actually have been edited")
	require.NoError(t, afero.WriteFile(fs, target, []byte(edited), 0o644))

	res, err := s.Apply(fs, target, setServer("ctxloom", map[string]any{"command": "z"}))
	require.NoError(t, err, "an edit to content ctxloom does not own must not refuse the write")
	assert.True(t, res.Reversed, "the prior application must still have been reversed")

	got := mustRead(t, fs, target)
	// The user's edit SURVIVED — not merely "the write succeeded".
	assert.Contains(t, got, edit, "the user's own edit must survive ctxloom's write")
	assert.NotContains(t, got, "https://mcp.example.com/v1", "the user's edit must not be reverted")
	// And ctxloom's own write landed, exactly once.
	assert.Contains(t, got, `"command": "z"`)
	assert.NotContains(t, got, `"command": "x"`, "the previous ctxloom entry must be gone, not merged over")
	assert.Equal(t, 1, strings.Count(got, `"ctxloom"`))
	// The rest of the user's file is untouched.
	assert.Contains(t, got, `"$schema": "https://example.com/mcp.schema.json"`)
	assert.Contains(t, got, `"headers": {"Authorization": "Bearer abc123"}`)
}

// The mechanism behind the test above, asserted directly: the stored reversal
// must make claims about ctxloom's OWN entry and nothing else.
//
// Two things ride on the scope. A reversal that asserts a neighbour refuses to
// apply once the user edits that neighbour, wedging the file; and it copies the
// user's adjacent content — here a bearer token, in a server ctxloom does not
// manage — into ctxloom's home-rooted record store.
func TestTheStoredReversalAssertsOnlyCtxloomsOwnEntry(t *testing.T) {
	s, fs := newStore(t)
	const target = "/proj/mcp.json"
	require.NoError(t, afero.WriteFile(fs, target, []byte(foreign), 0o644))

	res, err := s.Apply(fs, target, setServer("ctxloom", map[string]any{"command": "x"}))
	require.NoError(t, err)

	rec, found, err := s.Last(target)
	require.NoError(t, err)
	require.True(t, found)
	require.NotEmpty(t, rec.Reversal, "an empty reversal would satisfy every assertion below trivially")
	require.NotEmpty(t, res.RecordPath)

	// It names what it undoes...
	assert.Contains(t, rec.Reversal, "ctxloom", "the reversal must still name the entry it removes")

	// ...and asserts nothing of the user's. The line that matters is what a
	// neighbour contributes: a `~` HINT carries the key alone and can never
	// fail a match, so `remote-thing` may appear as one — that is what lets the
	// reversal still place itself after the user edits around it. A `test`
	// carrying the neighbour's VALUE is the thing this forbids, and the three
	// assertions below are the values.
	assert.NotContains(t, rec.Reversal, "Bearer abc123",
		"ctxloom must not copy the user's adjacent secrets into its own record store")
	assert.NotContains(t, rec.Reversal, "mcp.example.com",
		"the reversal must not assert the user's own values")
	assert.NotContains(t, rec.Reversal, "https://",
		"no value of the user's may ride the reversal, by any spelling")
	for _, line := range strings.Split(rec.Reversal, "\n") {
		if strings.HasPrefix(line, "~") {
			assert.NotContains(t, line, ":",
				"a hint names a neighbour and carries no value: %q", line)
		}
	}
}

// TestASecondCtxloomsEntryIsHealedNotRefused pins the case Apply's refusal was
// never meant to catch. ctxloom writes the RUNNING binary's absolute path into
// its own entry, so the copy on PATH and one built in a working tree write
// different values. Neither left a record the other can reverse, so the second
// one read the first's entry as a hand edit and refused — and because one
// wedged target fails the whole apply, that took down every hook and MCP server
// ctxloom manages, in a file nobody had touched.
func TestASecondCtxloomsEntryIsHealedNotRefused(t *testing.T) {
	s, fs := newStore(t)
	const target = "/proj/mcp.json"
	testsupport.WriteFileString(t, fs, target, foreign, 0o644)

	// The copy on PATH applies, and records what it wrote.
	_, err := s.Apply(fs, target, setServer("ctxloom", map[string]any{"command": "/opt/bin/ctxloom"}))
	require.NoError(t, err)

	// A ctxloom built in a working tree writes its own path WITHOUT going
	// through this store — a different binary, so a different record history.
	other := strings.Replace(mustRead(t, fs, target), `"command": "/opt/bin/ctxloom"`, `"command": "/home/u/src/ctxloom"`, 1)
	require.Contains(t, other, "/home/u/src/ctxloom", "the fixture must actually carry the other binary's path")
	testsupport.WriteFileString(t, fs, target, other, 0o644)

	res, err := s.Apply(fs, target, setServer("ctxloom", map[string]any{"command": "/opt/bin/ctxloom"}))
	require.NoError(t, err, "a second ctxloom's own entry is not a user edit and must not refuse")
	assert.Equal(t, []string{"/mcpServers/ctxloom"}, res.HealedPaths,
		"the heal must name what it took back out, so a caller can say so")

	got := mustRead(t, fs, target)
	assert.Contains(t, got, `"/opt/bin/ctxloom"`, "the applying binary's entry is what lands")
	assert.NotContains(t, got, "/home/u/src/ctxloom", "the superseded entry is taken back out, not left beside the new one")
	assert.Equal(t, 1, strings.Count(got, "ctxloom\":"), "exactly one ctxloom server entry survives")

	// The user's own content is still untouched — the whole point of the
	// refusal this narrows.
	assert.Contains(t, got, `"$schema": "https://example.com/mcp.schema.json"`)
	assert.Contains(t, got, `"url": "https://mcp.example.com/v1"`)
	assert.Contains(t, got, `"headers": {"Authorization": "Bearer abc123"}`)
}

// TestAUserWrapperAtCtxloomsPathStillRefuses is the other arm, and it is the one
// that must not regress: the heal keys on the EXECUTABLE the entry runs, never
// on the entry's NAME. A user who points the "ctxloom" key at their own wrapper
// owns those bytes, and taking them out is the clobber this package exists to
// prevent.
func TestAUserWrapperAtCtxloomsPathStillRefuses(t *testing.T) {
	s, fs := newStore(t)
	const target = "/proj/mcp.json"
	testsupport.WriteFileString(t, fs, target, foreign, 0o644)

	_, err := s.Apply(fs, target, setServer("ctxloom", map[string]any{"command": "/opt/bin/ctxloom"}))
	require.NoError(t, err)

	edited := strings.Replace(mustRead(t, fs, target), `"command": "/opt/bin/ctxloom"`, `"command": "/usr/local/bin/my-wrapper"`, 1)
	require.Contains(t, edited, "my-wrapper", "the fixture must actually have been edited")
	testsupport.WriteFileString(t, fs, target, edited, 0o644)

	_, err = s.Apply(fs, target, setServer("ctxloom", map[string]any{"command": "/opt/bin/ctxloom"}))
	require.Error(t, err, "an entry running something other than ctxloom is the user's, and must still refuse")
	assert.Contains(t, err.Error(), "drifted")
	assert.Equal(t, edited, mustRead(t, fs, target),
		"a refused write must leave the user's file exactly as they left it")
}

// indentedWithCtxloom is a .mcp.json as a tool or a human writes one: INDENTED,
// one member per line, already carrying a ctxloom entry — the shape `claude mcp
// add` produces and the shape an older ctxloom left behind.
const indentedWithCtxloom = `{
  "mcpServers": {
    "ctxloom": {
      "command": "/old/path/ctxloom",
      "_marker": "left-by-an-older-install"
    },
    "user-server": {
      "command": "/usr/bin/user-mcp"
    }
  }
}`

// TestOwnedEntryWithNoRecordIsAdoptedNotReplaced pins the failure that stood
// red on release/0.7 as TestClaudeCodeHookWriter_UpdatesSCMMCPServer.
//
// With no record to reverse, ctxloom REPLACED its own leftover entry in place.
// hew re-renders a container it edits in its own layout, so the indented entry
// above came back collapsed onto one line, the reversal could then no longer
// reproduce the user's bytes, and Apply refused — which in production means
// every hook and MCP server fails to configure, over an entry ctxloom itself
// had written.
func TestOwnedEntryWithNoRecordIsAdoptedNotReplaced(t *testing.T) {
	s, fs := newStore(t)
	const target = "/proj/mcp.json"
	testsupport.WriteFileString(t, fs, target, indentedWithCtxloom, 0o644)

	res, err := s.Apply(fs, target,
		setServer("ctxloom", map[string]any{"command": "/opt/bin/ctxloom"}),
		WithOwnedPaths("/mcpServers/ctxloom"))
	require.NoError(t, err, "ctxloom's own leftover entry must not make its next write refuse")
	assert.Equal(t, []string{"/mcpServers/ctxloom"}, res.AdoptedPaths,
		"the leftover was re-adopted, which is what makes the reversal renderable")

	got := mustRead(t, fs, target)
	assert.Contains(t, got, "/opt/bin/ctxloom", "the new entry landed")
	assert.NotContains(t, got, "/old/path/ctxloom", "the leftover is gone, not left beside it")
	assert.NotContains(t, got, "left-by-an-older-install",
		"the whole leftover entry goes, including keys the new one does not set")
	assert.Contains(t, got, `"command": "/usr/bin/user-mcp"`, "the user's own server is untouched")
	assert.NotEmpty(t, res.RecordPath, "the write is recorded, so the next one can reverse it")
}

// TestAnUnownedEntryAtAnOwnedPathIsNotAdopted is the guard on the above: the
// caller naming a path does NOT make what sits there ctxloom's. Ownership is
// proved from the executable the entry runs, so a user's own command parked at
// a name ctxloom manages is never quietly removed as "ctxloom's leftover".
func TestAnUnownedEntryAtAnOwnedPathIsNotAdopted(t *testing.T) {
	s, fs := newStore(t)
	const target = "/proj/mcp.json"
	testsupport.WriteFileString(t, fs, target,
		strings.Replace(indentedWithCtxloom, "/old/path/ctxloom", "/usr/local/bin/my-wrapper", 1), 0o644)

	before := mustRead(t, fs, target)

	_, err := s.Apply(fs, target,
		setServer("ctxloom", map[string]any{"command": "/opt/bin/ctxloom"}),
		WithOwnedPaths("/mcpServers/ctxloom"))

	// NOT adopted, and the proof is that nothing changed: ctxloom fell back to
	// replacing the entry in place, which is what it has always done to a name
	// it manages, and the byte-exact reversal guard then refused rather than
	// hand the user their file back in a layout they did not write. Refusing is
	// the safe direction here — the alternative is ctxloom silently absorbing a
	// command somebody else put there.
	require.Error(t, err, "a foreign entry at a managed name must not be quietly taken over")
	assert.Equal(t, before, mustRead(t, fs, target),
		"a refused write leaves the user's file exactly as they left it")
}

// TestRepeatedAppliesLeaveOneRecordPerTarget pins the retention rule. Every
// apply used to leave a file behind — one per apply per target, forever, in a
// home-rooted directory nothing swept. A real project accumulated 1522 of them.
//
// Only the newest is live, and that is a property of Apply rather than a policy
// chosen for convenience: each apply reverses the previous application before
// computing the next, so the reversal it stores already runs all the way back
// to the user's own content. The assertion below is that the survivor really
// does undo everything — a prune that kept the wrong file, or that pruned the
// live one, would show up here rather than as a mysterious wedged target later.
func TestRepeatedAppliesLeaveOneRecordPerTarget(t *testing.T) {
	s, fs := newStore(t)
	const target = "/proj/mcp.json"
	testsupport.WriteFileString(t, fs, target, foreign, 0o644)

	for i, cmd := range []string{"/opt/a/ctxloom", "/opt/b/ctxloom", "/opt/c/ctxloom", "/opt/d/ctxloom"} {
		_, err := s.Apply(fs, target, setServer("ctxloom", map[string]any{"command": cmd}))
		require.NoError(t, err, "apply %d", i)
	}

	records, err := afero.ReadDir(fs, "/home/u/.ctxloom/records")
	require.NoError(t, err)
	assert.Len(t, records, 1, "four applies against one target must leave ONE record, not four")

	// The survivor is the live one: it undoes the LAST write, not an earlier.
	rec, found, err := s.Last(target)
	require.NoError(t, err)
	require.True(t, found)
	assert.Contains(t, rec.Reversal, "/opt/d/ctxloom",
		"the surviving record must reverse the most recent application")

	// And it is a complete undo, back to the user's own file.
	res, err := s.Apply(fs, target, recordNothing())
	require.NoError(t, err)
	assert.True(t, res.Reversed)
	assert.Equal(t, foreign, mustRead(t, fs, target),
		"the retained record must still restore exactly the file the user wrote")
}

// TestPruningIsPerTarget pins the scope: a second target's record is not
// collateral. Pruning by directory rather than by target would delete the undo
// for every OTHER file ctxloom manages on this machine.
func TestPruningIsPerTarget(t *testing.T) {
	s, fs := newStore(t)
	const a, b = "/proj-a/mcp.json", "/proj-b/mcp.json"
	testsupport.WriteFileString(t, fs, a, foreign, 0o644)
	testsupport.WriteFileString(t, fs, b, foreign, 0o644)

	for _, target := range []string{a, b, a, b} {
		_, err := s.Apply(fs, target, setServer("ctxloom", map[string]any{"command": "/opt/bin/ctxloom"}))
		require.NoError(t, err)
	}

	records, err := afero.ReadDir(fs, "/home/u/.ctxloom/records")
	require.NoError(t, err)
	assert.Len(t, records, 2, "one record per target survives, not one in total")

	for _, target := range []string{a, b} {
		_, found, lerr := s.Last(target)
		require.NoError(t, lerr)
		assert.True(t, found, "%s must keep its own live record", target)
	}
}

// setServers writes SEVERAL servers under /mcpServers in one apply — the real
// shape: ctxloom manages its own entry alongside the ones a bundle ships, which
// run npx, a companion binary, anything.
func setServers(entries map[string]any) Build {
	return func(doc *hew.Doc, cur hew.Document) (int, error) {
		if _, ok := cur.Root().Member("mcpServers"); !ok {
			p, err := hew.ParsePathIn(doc.Format(), "/mcpServers")
			if err != nil {
				return 0, err
			}
			doc.AtPath(p).Set(entries)
			return 1, nil
		}
		recorded := 0
		for _, name := range collections.SortedKeys(entries) {
			p, err := hew.ParsePathIn(doc.Format(), "/mcpServers/"+name)
			if err != nil {
				return 0, err
			}
			doc.AtPath(p).Set(entries[name])
			recorded++
		}
		return recorded, nil
	}
}

// TestAManagedEntryRunningAnotherProgramDoesNotBlockTheHeal pins the flaw that
// reached a live machine: ownership was proved ONLY by "does this entry run
// ctxloom", but ctxloom also writes entries that run something else — a
// bundle's MCP server invoking npx, a companion binary. One such entry made
// every path unownable, so the heal bailed and the whole apply refused,
// over an entry nobody had touched.
//
// The second proof is untouched-since-written: the record says what ctxloom put
// there, and if the bytes still match, no user edit is at stake.
func TestAManagedEntryRunningAnotherProgramDoesNotBlockTheHeal(t *testing.T) {
	s, fs := newStore(t)
	const target = "/proj/mcp.json"
	testsupport.WriteFileString(t, fs, target, foreign, 0o644)

	managed := map[string]any{
		"ctxloom":             map[string]any{"command": "/opt/bin/ctxloom", "args": []any{"mcp", "serve"}},
		"sequential-thinking": map[string]any{"command": "npx", "args": []any{"-y", "server"}},
	}
	_, err := s.Apply(fs, target, setServers(managed))
	require.NoError(t, err)

	// A second ctxloom rewrites ONLY its own entry, out of band. The npx entry
	// is untouched — exactly as the record left it.
	other := strings.Replace(mustRead(t, fs, target), "/opt/bin/ctxloom", "/home/u/src/ctxloom", 1)
	require.Contains(t, other, "/home/u/src/ctxloom")
	testsupport.WriteFileString(t, fs, target, other, 0o644)

	res, err := s.Apply(fs, target, setServers(managed))
	require.NoError(t, err,
		"an untouched entry running npx must not make ctxloom refuse to fix its own")
	assert.Contains(t, res.HealedPaths, "/mcpServers/ctxloom")

	got := mustRead(t, fs, target)
	assert.Contains(t, got, "/opt/bin/ctxloom", "ctxloom's entry is corrected")
	assert.NotContains(t, got, "/home/u/src/ctxloom", "the superseded entry is gone")
	assert.Contains(t, got, `"npx"`, "the bundle's own server survives")
	assert.Contains(t, got, `"headers": {"Authorization": "Bearer abc123"}`, "the user's server is untouched")
}

// TestAUserEditToAManagedNonCtxloomEntryStillRefuses is the guard on the proof
// above: untouched-since-written is what makes an npx entry reclaimable, so an
// entry that has CHANGED since ctxloom wrote it is not, whatever it runs.
func TestAUserEditToAManagedNonCtxloomEntryStillRefuses(t *testing.T) {
	s, fs := newStore(t)
	const target = "/proj/mcp.json"
	testsupport.WriteFileString(t, fs, target, foreign, 0o644)

	managed := map[string]any{
		"ctxloom":             map[string]any{"command": "/opt/bin/ctxloom"},
		"sequential-thinking": map[string]any{"command": "npx"},
	}
	_, err := s.Apply(fs, target, setServers(managed))
	require.NoError(t, err)

	// The user retargets the npx server at their own build.
	edited := strings.Replace(mustRead(t, fs, target), `"command": "npx"`, `"command": "/home/u/my-npx"`, 1)
	require.Contains(t, edited, "my-npx")
	testsupport.WriteFileString(t, fs, target, edited, 0o644)

	_, err = s.Apply(fs, target, setServers(managed))
	require.Error(t, err, "an entry the user changed is theirs, whatever it runs")
	assert.Equal(t, edited, mustRead(t, fs, target),
		"a refused write leaves the user's file exactly as they left it")
}
