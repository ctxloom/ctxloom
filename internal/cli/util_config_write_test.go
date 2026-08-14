package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	hew "github.com/benjaminabbitt/hew/go"
	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/afero"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// configWriteTestCmd builds a bare command wired the way the real
// configWriteCmd's RunE expects: --format registered (emit reads it) and
// stdin/stdout set to buffers the test controls.
func configWriteTestCmd(stdin string) (*cobra.Command, *bytes.Buffer) {
	cmd := &cobra.Command{}
	cmd.Flags().String("format", formatText, "")
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetIn(strings.NewReader(stdin))
	return cmd, buf
}

// --- Rule 1: resolve+validate the real path ---

func TestValidateRealFilePath(t *testing.T) {
	cases := []struct {
		name    string
		path    string
		wantErr string
	}{
		{"empty", "", "required"},
		{"relative", "config/settings.json", "absolute"},
		{"tilde-relative", "~/config/settings.json", "absolute"},
		{"dollar-home-relative", "$HOME/config/settings.json", "absolute"},
		{"embedded-dollar-in-absolute", "/home/$USER/.config/zed/settings.json", "unexpanded ~ or $"},
		{"embedded-tilde-in-absolute", "/home/user/~old/config/settings.json", "unexpanded ~ or $"},
		{"absolute-clean", "/home/user/.config/zed/settings.json", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateRealFilePath(tc.path)
			if tc.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// --- Rule 3: JSON parse-merge, preserving foreign keys ---

func TestRunConfigWrite_JSONMerge_PreservesForeignKeysAndAddsNew(t *testing.T) {
	fs := afero.NewMemMapFs()
	path := "/home/user/.config/zed/settings.json"
	original := `{"foreign_setting":"keep-me","agent_servers":{"other-client":{"command":"other","args":["run"]}}}`
	require.NoError(t, afero.WriteFile(fs, path, []byte(original), 0644))

	patch := `{"agent_servers":{"ctxloom: dev":{"command":"/usr/local/bin/ctxloom","args":["acp","--agent","dev"]}}}`
	cmd, _ := configWriteTestCmd(patch)

	result, err := runConfigWrite(fs, cmd, path, "")
	require.NoError(t, err)
	assert.False(t, result.Created)
	assert.True(t, result.Verified)
	assert.Equal(t, []string{"agent_servers"}, result.Merged)

	out, err := afero.ReadFile(fs, path)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(out, &got))

	assert.Equal(t, "keep-me", got["foreign_setting"], "a key the patch never mentioned must survive untouched")
	servers := got["agent_servers"].(map[string]any)
	other := servers["other-client"].(map[string]any)
	assert.Equal(t, "other", other["command"], "a sibling entry under the same merged object must survive")

	dev := servers["ctxloom: dev"].(map[string]any)
	assert.Equal(t, "/usr/local/bin/ctxloom", dev["command"])
	assert.Equal(t, []any{"acp", "--agent", "dev"}, dev["args"])
}

// --- Rule 3: TOML parse-merge, preserving foreign keys ---

func TestRunConfigWrite_TOMLMerge_PreservesForeignKeysAndAddsNew(t *testing.T) {
	fs := afero.NewMemMapFs()
	path := "/home/user/.config/nori/config.toml"
	original := "[foreign_setting]\nkeep = \"yes\"\n\n[agent_servers.other-client]\ncommand = \"other\"\n"
	require.NoError(t, afero.WriteFile(fs, path, []byte(original), 0644))

	patch := `{"agent_servers":{"ctxloom: dev":{"command":"/usr/local/bin/ctxloom","args":["acp","--agent","dev"]}}}`
	cmd, _ := configWriteTestCmd(patch)

	result, err := runConfigWrite(fs, cmd, path, "")
	require.NoError(t, err)
	assert.True(t, result.Verified)
	assert.Equal(t, configWriteFiletypeTOML, result.Filetype, "filetype inferred from .toml extension")

	out, err := afero.ReadFile(fs, path)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, toml.Unmarshal(out, &got))

	foreign := got["foreign_setting"].(map[string]any)
	assert.Equal(t, "yes", foreign["keep"], "a table the patch never mentioned must survive untouched")

	servers := got["agent_servers"].(map[string]any)
	other := servers["other-client"].(map[string]any)
	assert.Equal(t, "other", other["command"], "a sibling table entry must survive")

	dev := servers["ctxloom: dev"].(map[string]any)
	assert.Equal(t, "/usr/local/bin/ctxloom", dev["command"])
}

// --- Rule 4: fail loud on unparseable, NEVER overwrite ---

// TestRunConfigWrite_UnparseableExisting_RefusesAndPreservesBytes is the
// headline test: config-write's entire reason to exist is refusing to clobber
// a file it can't safely edit. This proves the original bytes are BYTE-FOR-
// BYTE unchanged after a refused write, not merely that an error was returned.
func TestRunConfigWrite_UnparseableExisting_RefusesAndPreservesBytes(t *testing.T) {
	fs := afero.NewMemMapFs()
	path := "/home/user/.config/zed/settings.json"
	original := []byte(`{"this is": not valid json,`)
	require.NoError(t, afero.WriteFile(fs, path, original, 0644))

	patch := `{"agent_servers":{"ctxloom: dev":{"command":"ctxloom"}}}`
	cmd, _ := configWriteTestCmd(patch)

	_, err := runConfigWrite(fs, cmd, path, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), path)
	assert.Contains(t, err.Error(), "refusing to overwrite")

	after, readErr := afero.ReadFile(fs, path)
	require.NoError(t, readErr)
	assert.True(t, bytes.Equal(original, after), "the original bytes must survive byte-for-byte; got: %s", after)
}

// --- Rule 2: timestamped backup BEFORE any edit ---

// --- Rule 5: re-read + verify catches a bad write ---

// tamperedReadFs wraps an afero.Fs and, from the Nth Open of a chosen path
// onward, returns corrupt bytes instead of delegating — simulating "what got
// written is not what a subsequent read sees" (e.g. a concurrent clobber or
// storage fault) so the verify step has something real to catch.
type tamperedReadFs struct {
	afero.Fs
	path       string
	corruptAt  int
	opens      int
	corruptVal []byte
}

func (f *tamperedReadFs) Open(name string) (afero.File, error) {
	if name == f.path {
		f.opens++
		if f.opens >= f.corruptAt {
			mem := afero.NewMemMapFs()
			if err := afero.WriteFile(mem, "corrupt", f.corruptVal, 0600); err != nil {
				return nil, err
			}
			return mem.Open("corrupt")
		}
	}
	return f.Fs.Open(name)
}

func TestRunConfigWrite_ReReadVerify_CatchesBadWrite(t *testing.T) {
	base := afero.NewMemMapFs()
	path := "/home/user/.config/zed/settings.json"
	original := []byte(`{"foreign_setting":"keep-me"}`)
	require.NoError(t, afero.WriteFile(base, path, original, 0644))

	// opens==1 is our own pre-write base read (must see the REAL file so the
	// merge is computed correctly); every open from opens==2 onward (the
	// our own post-write verify read) sees corrupt bytes instead. The count
	// dropped by one when the atomic-write helper's internal backup read was
	// retired along with the backup itself.
	fs := &tamperedReadFs{Fs: base, path: path, corruptAt: 2, corruptVal: []byte("{not json at all")}

	patch := `{"agent_servers":{"ctxloom: dev":{"command":"ctxloom"}}}`
	cmd, _ := configWriteTestCmd(patch)

	result, err := runConfigWrite(fs, cmd, path, "")
	require.Error(t, err, "a verify step that never checks anything would return nil here")
	assert.Contains(t, err.Error(), "verify failed")
	// No backup is kept any more, so the failure must say so and point the
	// user at the file itself rather than at a recovery path that no longer
	// exists — a message naming a nonexistent backup is worse than none.
	assert.Contains(t, err.Error(), "no backup is kept")
	assert.Contains(t, err.Error(), path)
	assert.False(t, result.Verified)

	// And nothing dated was dropped beside it: this writer edits FOREIGN tool
	// config directories, where ctxloom leaving debris is the complaint that
	// retired the mechanism.
	matches, gerr := afero.Glob(base, path+".bak.*")
	require.NoError(t, gerr)
	assert.Empty(t, matches, "no dated backup sibling may be left in a third-party config dir")
	_ = original
}

// --- silent-no-op guard: an empty or no-op patch must fail loud ---

func TestRunConfigWrite_EmptyStdinPatch_Refuses(t *testing.T) {
	fs := afero.NewMemMapFs()
	path := "/home/user/.config/zed/settings.json"
	cmd, _ := configWriteTestCmd("   \n")

	_, err := runConfigWrite(fs, cmd, path, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty patch")

	exists, _ := afero.Exists(fs, path)
	assert.False(t, exists, "an empty patch must not create the file either")
}

func TestRunConfigWrite_EmptyObjectPatch_Refuses(t *testing.T) {
	fs := afero.NewMemMapFs()
	cmd, _ := configWriteTestCmd(`{}`)

	_, err := runConfigWrite(fs, cmd, "/home/user/.config/zed/settings.json", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty JSON object")
}

// --- missing file: created fresh, no spurious backup ---

// --- filetype resolution ---

func TestResolveConfigFiletype(t *testing.T) {
	ft, err := resolveConfigFiletype("/x/settings.json", "")
	require.NoError(t, err)
	assert.Equal(t, configWriteFiletypeJSON, ft)

	ft, err = resolveConfigFiletype("/x/config.toml", "")
	require.NoError(t, err)
	assert.Equal(t, configWriteFiletypeTOML, ft)

	ft, err = resolveConfigFiletype("/x/config.toml", "json")
	require.NoError(t, err)
	assert.Equal(t, configWriteFiletypeJSON, ft, "an explicit --filetype overrides the extension")

	_, err = resolveConfigFiletype("/x/config.yaml", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot infer")

	_, err = resolveConfigFiletype("/x/config.toml", "yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported")
}

// --- verify normalization: JSON-vs-TOML numeric round-trip must not false-negative ---

func TestContainsConfigPatch_NormalizesNumericTypes(t *testing.T) {
	patch := map[string]any{"port": float64(8080)} // as JSON always decodes it
	data := map[string]any{"port": int64(8080)}    // as go-toml may decode it
	assert.True(t, containsConfigPatch(data, patch))

	mismatched := map[string]any{"port": int64(9090)}
	assert.False(t, containsConfigPatch(mismatched, patch))
}

// --- end-to-end through the real cobra command + real OS filesystem ---

// TestConfigWriteCmd_RunE_EndToEnd exercises the actual wiring an agent
// invokes: --file/--filetype flags, a JSON patch on stdin, --format json
// output — against a real file on the real OS filesystem (t.TempDir()), not
// the in-memory fs the rule-level tests above use. Package-level flag vars
// are set directly (mirroring how cobra would after flag parsing) since this
// test builds a bare *cobra.Command rather than routing through rootCmd.
func TestConfigWriteCmd_RunE_EndToEnd(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"foreign":"keep"}`), 0644))

	configWriteFile = path
	configWriteFiletype = ""
	t.Cleanup(func() { configWriteFile, configWriteFiletype = "", "" })

	cmd := &cobra.Command{}
	cmd.Flags().String("format", formatText, "")
	require.NoError(t, cmd.Flags().Set("format", "json"))
	cmd.SetIn(strings.NewReader(`{"agent_servers":{"ctxloom: dev":{"command":"ctxloom","args":["acp","--agent","dev"]}}}`))
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	require.NoError(t, configWriteCmd.RunE(cmd, nil))

	var got configWriteResult
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got), "output: %s", buf.String())
	assert.Equal(t, path, got.File)
	assert.True(t, got.Verified)

	onDisk, err := os.ReadFile(path)
	require.NoError(t, err)
	var cfg map[string]any
	require.NoError(t, json.Unmarshal(onDisk, &cfg))
	assert.Equal(t, "keep", cfg["foreign"])
	servers := cfg["agent_servers"].(map[string]any)
	assert.Contains(t, servers, "ctxloom: dev")

	dated, gerr := filepath.Glob(path + ".bak.*")
	require.NoError(t, gerr)
	assert.Empty(t, dated, "no dated backup sibling may be left in a third-party config dir")
}

// --- P5 slice 1: JSON path rides hew ---

// TestRunConfigWrite_JSONPatch_ModePreserved locks in that hew's byte-
// preserving apply did not disturb AtomicWriteFile's existing "reuse the
// file's own mode" behavior (settings_io.go's AtomicWriteFile doc): the
// write path itself is unchanged by this slice, only how the OUT bytes it
// receives are computed, but that is exactly the kind of thing a refactor
// can break by accident if a new branch bypassed the shared write helper.
func TestRunConfigWrite_JSONPatch_ModePreserved(t *testing.T) {
	fs := afero.NewMemMapFs()
	path := "/home/user/.config/zed/settings.json"
	require.NoError(t, afero.WriteFile(fs, path, []byte(`{"foreign":"keep"}`), 0640))

	cmd, _ := configWriteTestCmd(`{"agent_servers":{"dev":{"command":"ctxloom"}}}`)
	result, err := runConfigWrite(fs, cmd, path, "")
	require.NoError(t, err)
	assert.True(t, result.Verified)

	info, err := fs.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0640), info.Mode().Perm(), "a pre-existing file's mode must survive the hew-based JSON write untouched")
}

// TestRunConfigWrite_JSONPatch_BytePreservingFormatting is the parity
// measurement the task called for: hew is a byte-preserving STRUCTURAL
// applier (only the byte ranges a transform touches change; every other
// byte of the source is copied verbatim), unlike the retired
// json.MarshalIndent(deepMergeConfigMaps(...)) path, which re-serialized the
// WHOLE file — alphabetized keys, 2-space indent — on every write. This test
// pins that difference precisely rather than leaving it implicit: an
// untouched region keeps its EXACT original bytes (odd spacing, key order,
// single-line — whatever the source had), while the touched region is
// spliced with hew's own compact JSON style, not re-indented to match.
func TestRunConfigWrite_JSONPatch_BytePreservingFormatting(t *testing.T) {
	fs := afero.NewMemMapFs()
	path := "/home/user/.config/zed/settings.json"
	// Deliberately odd, non-canonical formatting and out-of-alphabetical key
	// order: "zeta" before "alpha", single-line, no space after ":". The old
	// re-serializing path would have alphabetized and 2-space-indented this
	// on ANY merge; hew must not.
	original := `{"zeta":"last","alpha":"first"}`
	require.NoError(t, afero.WriteFile(fs, path, []byte(original), 0644))

	cmd, _ := configWriteTestCmd(`{"beta":"new"}`)
	result, err := runConfigWrite(fs, cmd, path, "")
	require.NoError(t, err)
	assert.True(t, result.Verified)

	out, err := afero.ReadFile(fs, path)
	require.NoError(t, err)
	// The untouched "zeta" and "alpha" members must survive byte-for-byte,
	// odd formatting and original order included — proof this is a splice,
	// not a re-serialization.
	assert.Contains(t, string(out), `"zeta":"last"`)
	assert.Contains(t, string(out), `"alpha":"first"`)
	assert.True(t, strings.Index(string(out), "zeta") < strings.Index(string(out), "alpha"),
		"source key order must survive; a re-serializing merge would alphabetize")
	// The added member arrives in hew's own compact house style, not
	// 2-space-indented — this line is the "IS a behavior change" the task
	// asked to measure rather than assume: it fails the moment hew's
	// formatting choice changes underneath this pin.
	assert.Contains(t, string(out), `"beta": "new"`, "hew's JSON encoder spaces after ':'; the retired path's json.MarshalIndent also did, but on the WHOLE file, not just the edited member")

	var got map[string]any
	require.NoError(t, json.Unmarshal(out, &got))
	assert.Equal(t, "first", got["alpha"])
	assert.Equal(t, "last", got["zeta"])
	assert.Equal(t, "new", got["beta"])
}

// TestRunConfigWrite_JSONPatch_NullDeletesKey pins the deliberate semantic
// upgrade this slice makes: a null patch value now DELETES the target key
// (RFC 7386 merge-patch semantics), where the retired deepMergeConfigMaps
// had no delete semantics at all and would have written a literal JSON null.
func TestRunConfigWrite_JSONPatch_NullDeletesKey(t *testing.T) {
	fs := afero.NewMemMapFs()
	path := "/home/user/.config/zed/settings.json"
	require.NoError(t, afero.WriteFile(fs, path, []byte(`{"keep":"yes","drop":"me"}`), 0644))

	cmd, _ := configWriteTestCmd(`{"drop":null}`)
	result, err := runConfigWrite(fs, cmd, path, "")
	require.NoError(t, err)
	assert.True(t, result.Verified)

	out, err := afero.ReadFile(fs, path)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(out, &got))
	assert.Equal(t, "yes", got["keep"])
	_, stillThere := got["drop"]
	assert.False(t, stillThere, "a null patch value must delete the key, not write a literal null")
}

// TestRunConfigWrite_JSONPatch_NullOnAbsentKey_NoOp proves the delete is
// Optional: deleting a key that was never there is a no-op, matching RFC
// 7386 (removing something absent is not an error) rather than the HEW013
// no-match failure a non-optional remove would raise.
func TestRunConfigWrite_JSONPatch_NullOnAbsentKey_NoOp(t *testing.T) {
	fs := afero.NewMemMapFs()
	path := "/home/user/.config/zed/settings.json"
	require.NoError(t, afero.WriteFile(fs, path, []byte(`{"keep":"yes"}`), 0644))

	cmd, _ := configWriteTestCmd(`{"never-existed":null}`)
	result, err := runConfigWrite(fs, cmd, path, "")
	require.NoError(t, err, "deleting an absent key must not error")
	assert.True(t, result.Verified)

	out, err := afero.ReadFile(fs, path)
	require.NoError(t, err)
	assert.JSONEq(t, `{"keep":"yes"}`, string(out))
}

// TestRunConfigWrite_JSONPatch_NestedMergePreservesSiblingsBeyondOneLevel
// extends the existing nested-merge parity case one level deeper: hew's
// `add` can only create ONE missing path segment per transform
// (hewjson.planInsert requires the immediate parent to already resolve), so
// appendJSONPatch must stop recursing and emit one whole-subtree add the
// moment the target doesn't already hold a like-shaped object — this proves
// that boundary lands at the RIGHT level for a two-deep new key, not one
// level too shallow (which would 500 on "parent is not an object" / a
// resolve failure) or one level too deep (which would silently drop a
// sibling the boundary should have protected).
func TestRunConfigWrite_JSONPatch_NestedMergePreservesSiblingsBeyondOneLevel(t *testing.T) {
	fs := afero.NewMemMapFs()
	path := "/home/user/.config/zed/settings.json"
	original := `{"agent_servers":{"other-client":{"command":"other","nested":{"already":"here"}}}}`
	require.NoError(t, afero.WriteFile(fs, path, []byte(original), 0644))

	patch := `{"agent_servers":{"other-client":{"nested":{"added":"now"}}}}`
	cmd, _ := configWriteTestCmd(patch)
	result, err := runConfigWrite(fs, cmd, path, "")
	require.NoError(t, err)
	assert.True(t, result.Verified)

	out, err := afero.ReadFile(fs, path)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(out, &got))
	nested := got["agent_servers"].(map[string]any)["other-client"].(map[string]any)["nested"].(map[string]any)
	assert.Equal(t, "here", nested["already"], "a sibling two levels deep must survive the merge")
	assert.Equal(t, "now", nested["added"])
}

// TestRunConfigWrite_JSONPatch_ApplicationRecord_WrittenWithContent is the
// payload assertion the record needs (silent-no-op discipline applies to
// the record just as much as to the primary write — a Record field pointing
// at nothing, or at an empty file, would be worse than no field at all): a
// successful JSON apply must produce a real §9.7 record file, and its
// content — before/after digests matching the ACTUAL bytes, and the
// resolved transform this patch executed — must be verifiable, not just
// present.
func TestRunConfigWrite_JSONPatch_ApplicationRecord_WrittenWithContent(t *testing.T) {
	fs := afero.NewMemMapFs()
	path := "/home/user/.config/zed/settings.json"
	// agent_servers already exists as an object, so the patch's "dev" key
	// resolves to a LEAF-level pointer (/agent_servers/dev) — the case that
	// actually exercises hew.Resolve doing path resolution, versus a
	// brand-new top-level key, which would resolve to itself trivially.
	original := []byte(`{"foreign":"keep","agent_servers":{"other":{"command":"other"}}}`)
	require.NoError(t, afero.WriteFile(fs, path, original, 0644))

	patchBytes := []byte(`{"agent_servers":{"dev":{"command":"ctxloom"}}}`)
	cmd, _ := configWriteTestCmd(string(patchBytes))
	result, err := runConfigWrite(fs, cmd, path, "")
	require.NoError(t, err)
	require.True(t, result.Verified)
	require.NotEmpty(t, result.Record, "a successful JSON apply must report where its application record went")

	after, err := afero.ReadFile(fs, path)
	require.NoError(t, err)

	recordBytes, err := afero.ReadFile(fs, result.Record)
	require.NoError(t, err)
	require.NotEmpty(t, recordBytes, "the record file must not be empty — an empty record is the silent-no-op failure mode applied to the audit trail")

	var rec map[string]any
	require.NoError(t, yaml.Unmarshal(recordBytes, &rec))
	assert.EqualValues(t, 1, rec["hew-record"])
	assert.NotEmpty(t, rec["applied_at"])

	patchNode := rec["patch"].(map[string]any)
	assert.Equal(t, "sha256:"+sha256Hex(patchBytes), patchNode["digest"])

	targets, ok := rec["targets"].([]any)
	require.True(t, ok)
	require.Len(t, targets, 1)
	target := targets[0].(map[string]any)
	assert.Equal(t, path, target["target"])
	assert.Equal(t, "json", target["format"])
	assert.Equal(t, "sha256:"+sha256Hex(original), target["before"], "before digest must match the bytes as READ, not some other snapshot")
	assert.Equal(t, "sha256:"+sha256Hex(after), target["after"], "after digest must match the bytes actually WRITTEN")
	assert.True(t, target["committed"].(bool))

	transforms, ok := target["transforms"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, transforms, "the record must carry the RESOLVED transform list that actually executed, not an empty stand-in")
	op := transforms[0].(map[string]any)
	assert.Equal(t, "add", op["op"])
	assert.Equal(t, "/agent_servers/dev", op["path"], "the resolved op's path must be the concrete RFC 6901 pointer hew actually wrote to")
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// TestRunConfigWrite_TOMLPatch_NoApplicationRecord states explicitly what
// this slice does NOT do: hew ships only a JSON applier today (task brief,
// unit 2), so a TOML target keeps the old deep-merge path untouched and
// gets no application record at all — Record stays "".
func TestRunConfigWrite_TOMLPatch_NoApplicationRecord(t *testing.T) {
	fs := afero.NewMemMapFs()
	path := "/home/user/.config/nori/config.toml"
	require.NoError(t, afero.WriteFile(fs, path, []byte("[foreign]\nkeep = \"yes\"\n"), 0644))

	cmd, _ := configWriteTestCmd(`{"added":"value"}`)
	result, err := runConfigWrite(fs, cmd, path, "")
	require.NoError(t, err)
	assert.True(t, result.Verified)
	assert.Empty(t, result.Record, "TOML has no hew applier yet; config-write must not claim a record it did not write")
}

// --- appendJSONPatch: the conversion's recursion boundary, in isolation ---

func TestAppendJSONPatch_RecursionBoundary(t *testing.T) {
	base := map[string]any{
		"existing_obj": map[string]any{"sibling": "survives"},
		"scalar_key":   "old",
	}
	patch := map[string]any{
		"existing_obj":  map[string]any{"added": "new"},                // must recurse (base has a like-shaped object)
		"brand_new_obj": map[string]any{"a": map[string]any{"b": "c"}}, // must NOT recurse (base has nothing here)
		"scalar_key":    "new",                                         // plain replace
		"to_delete":     nil,                                           // remove
	}
	tl := buildJSONTransformList("/x/settings.json", base, patch)
	require.NotEmpty(t, tl.Transform)

	byPath := map[string]hew.Transform{}
	for _, tr := range tl.Transform {
		byPath[tr.Path.String()] = tr
	}

	// existing_obj recursed: the transform touches the LEAF ("added"), not
	// the whole object, so "sibling" is never in the edit's blast radius.
	added, ok := byPath["/existing_obj/added"]
	require.True(t, ok, "existing_obj must recurse to its leaf, not become one whole-object replace")
	assert.Equal(t, hew.OpAdd, added.Op)
	assert.Equal(t, hew.ConflictReplace, added.OnConflict)
	_, sawWholeObjectReplace := byPath["/existing_obj"]
	assert.False(t, sawWholeObjectReplace, "recursing into an existing object must not ALSO emit a whole-object transform")

	// brand_new_obj: base has nothing there, so it must be ONE atomic add of
	// the whole subtree, never a leaf-level add three-deep (which would
	// require a parent — /brand_new_obj/a — that does not exist yet).
	whole, ok := byPath["/brand_new_obj"]
	require.True(t, ok, "a brand-new nested object must be one whole-subtree add, not decomposed to non-existent parents")
	assert.Equal(t, hew.OpAdd, whole.Op)
	_, sawLeafThreeDeep := byPath["/brand_new_obj/a/b"]
	assert.False(t, sawLeafThreeDeep)

	scalar, ok := byPath["/scalar_key"]
	require.True(t, ok)
	assert.Equal(t, hew.OpAdd, scalar.Op)
	assert.Equal(t, hew.ConflictReplace, scalar.OnConflict)

	del, ok := byPath["/to_delete"]
	require.True(t, ok)
	assert.Equal(t, hew.OpRemove, del.Op)
	assert.True(t, del.Optional, "a delete must be optional so removing an absent key is a no-op, not HEW013")
}
