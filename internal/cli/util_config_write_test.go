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
	"time"

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
// (ext/json planInsert requires the immediate parent to already resolve), so
// recordConfigPatch must stop recursing and emit one whole-subtree add the
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

// TestRenderConfigWriteResult_RecordLine covers renderConfigWriteResult's
// text-mode Record line in both states: present when a JSON apply wrote one,
// absent (not a blank/zero-value line — the whole line) when it didn't.
func TestRenderConfigWriteResult_RecordLine(t *testing.T) {
	var withRecord bytes.Buffer
	require.NoError(t, renderConfigWriteResult(&withRecord, configWriteResult{
		File: "/x/settings.json", Filetype: "json", Verified: true,
		Record: "/home/user/.ctxloom/records/x.hew-record.yaml",
	}))
	assert.Contains(t, withRecord.String(), "application record: /home/user/.ctxloom/records/x.hew-record.yaml")

	var noRecord bytes.Buffer
	require.NoError(t, renderConfigWriteResult(&noRecord, configWriteResult{
		File: "/x/config.toml", Filetype: "toml", Verified: true,
	}))
	assert.NotContains(t, noRecord.String(), "application record", "a TOML result with no Record must not print the line at all")
}

// TestRunConfigWrite_TOMLPatch_NoApplicationRecord states explicitly what
// this slice does NOT do: hew ships only a JSON applier today (task brief,
// unit 2), so a TOML target keeps the old deep-merge path untouched and
// gets no application record at all — Record stays "".
// TOML used to take a deep-merge path that re-serialized the whole file and
// wrote NO application record — this test pinned that absence. Now that hew
// ships a TOML document reader, TOML resolves and records exactly as JSON does,
// so the pin is inverted: the record must EXIST and state what happened.
//
// The digests are the load-bearing part. Asserting only that a record file
// exists would pass against a record describing some other write, which is the
// audit-trail form of this project's silent no-op.
func TestRunConfigWrite_TOMLPatch_WritesApplicationRecord(t *testing.T) {
	fs := afero.NewMemMapFs()
	path := "/home/user/.config/nori/config.toml"
	// agent_servers.other already exists, so the patch's "dev" key resolves to
	// a LEAF pointer rather than a trivially self-resolving top-level key —
	// the case that actually exercises resolution against the TOML tree.
	original := []byte("[foreign]\nkeep = \"yes\"\n\n[agent_servers.other]\ncommand = \"other\"\n")
	require.NoError(t, afero.WriteFile(fs, path, original, 0644))

	patchBytes := []byte(`{"agent_servers":{"dev":{"command":"ctxloom"}}}`)
	cmd, _ := configWriteTestCmd(string(patchBytes))
	result, err := runConfigWrite(fs, cmd, path, "")
	require.NoError(t, err)
	require.True(t, result.Verified)
	require.NotEmpty(t, result.Record, "a successful TOML apply must report where its application record went")

	after, err := afero.ReadFile(fs, path)
	require.NoError(t, err)

	recordBytes, err := afero.ReadFile(fs, result.Record)
	require.NoError(t, err)
	require.NotEmpty(t, recordBytes, "an empty record is the silent-no-op failure mode applied to the audit trail")

	var rec map[string]any
	require.NoError(t, yaml.Unmarshal(recordBytes, &rec))
	assert.EqualValues(t, 1, rec["hew-record"])

	targets, ok := rec["targets"].([]any)
	require.True(t, ok)
	require.Len(t, targets, 1)
	target := targets[0].(map[string]any)
	assert.Equal(t, path, target["target"])
	assert.Equal(t, "toml", target["format"], "the record must state the format actually applied, not a hardcoded json")
	assert.Equal(t, "sha256:"+sha256Hex(original), target["before"], "before digest must match the bytes as READ")
	assert.Equal(t, "sha256:"+sha256Hex(after), target["after"], "after digest must match the bytes actually WRITTEN")

	transforms, ok := target["transforms"].([]any)
	require.True(t, ok, "the record must carry the RESOLVED transforms, which is what needed a TOML document reader")
	require.NotEmpty(t, transforms)
}

// Creating a file that does not exist yet is the case where the EMPTY DOCUMENT
// is format-specific: "{}" is an empty JSON object but a parse error in TOML,
// where empty is zero bytes. Every other test here pre-writes its target, so
// without this one a shared "{}" literal would sail through — the mutation of
// emptyTargetFor that collapses both formats to "{}" survived until this
// existed.
func TestRunConfigWrite_CreatesMissingFile_PerFormatEmptyDocument(t *testing.T) {
	for _, c := range []struct {
		name, path, wantSubstr string
	}{
		{"toml", "/home/user/.config/nori/config.toml", `command = "ctxloom"`},
		{"json", "/home/user/.config/zed/settings.json", `"command"`},
	} {
		t.Run(c.name, func(t *testing.T) {
			fs := afero.NewMemMapFs()
			cmd, _ := configWriteTestCmd(`{"agent_servers":{"dev":{"command":"ctxloom"}}}`)

			result, err := runConfigWrite(fs, cmd, c.path, "")
			require.NoError(t, err, "creating a missing %s target must succeed", c.name)
			assert.True(t, result.Created, "the report must say the file was created")
			require.True(t, result.Verified)

			out, err := afero.ReadFile(fs, c.path)
			require.NoError(t, err)
			require.NotEmpty(t, out, "a created file with zero bytes is the silent no-op")
			assert.Contains(t, string(out), c.wantSubstr,
				"the patched value must be present in the created file")
		})
	}
}

// The foreign-key guarantee, now that TOML rides a byte-preserving applier
// rather than a re-serializing merge: a table the patch never mentions must
// survive with its bytes intact, comments and layout included. The old path
// could not have passed this — it rebuilt the file from a decoded map.
func TestRunConfigWrite_TOMLPatch_PreservesUnrelatedBytes(t *testing.T) {
	fs := afero.NewMemMapFs()
	path := "/home/user/.config/nori/config.toml"
	original := []byte("# keep this comment\n[foreign]\nkeep   =    \"yes\"   # and this alignment\n\n[agent_servers.other]\ncommand = \"other\"\n")
	require.NoError(t, afero.WriteFile(fs, path, original, 0644))

	cmd, _ := configWriteTestCmd(`{"agent_servers":{"dev":{"command":"ctxloom"}}}`)
	result, err := runConfigWrite(fs, cmd, path, "")
	require.NoError(t, err)
	require.True(t, result.Verified)

	after, err := afero.ReadFile(fs, path)
	require.NoError(t, err)
	out := string(after)

	assert.Contains(t, out, "# keep this comment", "a comment the patch never touched must survive")
	assert.Contains(t, out, `keep   =    "yes"   # and this alignment`,
		"the untouched line's exact spacing and trailing comment must survive byte-for-byte")
	assert.Contains(t, out, `command = "ctxloom"`, "the patched value must actually be present")
}

// The record's whole point is that a write can be UNDONE, so the test is the
// round trip: apply what the record calls the inverse and the file must come
// back byte-for-byte. Asserting merely that an `inverse:` key exists would pass
// against an inverse that undoes the wrong thing, or nothing.
func TestApplicationRecord_InverseRestoresTheOriginalBytes(t *testing.T) {
	fs := afero.NewMemMapFs()
	path := "/home/user/.config/zed/settings.json"
	// A foreign key, and an existing entry the patch REPLACES — the replace is
	// the case a naive inverse gets wrong, because undoing it means restoring
	// the old value, not deleting the key.
	original := []byte(`{"foreign":"keep","agent_servers":{"other":{"command":"other"}}}`)
	require.NoError(t, afero.WriteFile(fs, path, original, 0o644))

	cmd, _ := configWriteTestCmd(`{"agent_servers":{"other":{"command":"replaced"},"dev":{"command":"ctxloom"}}}`)
	result, err := runConfigWrite(fs, cmd, path, "")
	require.NoError(t, err)
	require.NotEmpty(t, result.Record)

	after, err := afero.ReadFile(fs, path)
	require.NoError(t, err)
	require.NotEqual(t, string(original), string(after), "the write must actually have changed the file")

	recordBytes, err := afero.ReadFile(fs, result.Record)
	require.NoError(t, err)
	var rec struct {
		Targets []struct {
			Inverse []struct {
				Op    string    `yaml:"op"`
				Path  string    `yaml:"path"`
				Value yaml.Node `yaml:"value"`
			} `yaml:"inverse"`
		} `yaml:"targets"`
	}
	require.NoError(t, yaml.Unmarshal(recordBytes, &rec))
	require.Len(t, rec.Targets, 1)
	require.NotEmpty(t, rec.Targets[0].Inverse, "the record must carry an inverse, or nothing can undo this write")

	// Rebuild the inverse as a transform list and apply it to the CURRENT file.
	var transforms []hew.Transform
	for _, op := range rec.Targets[0].Inverse {
		p, perr := hew.ParsePath(op.Path)
		require.NoError(t, perr, "a recorded inverse path must parse")
		tr := hew.Transform{Op: hew.OpKind(op.Op), Path: p}
		if op.Value.Kind != 0 {
			v := op.Value
			tr.Value = hew.NodeValue(&v)
		}
		if tr.Op == hew.OpAdd {
			tr.OnConflict = hew.ConflictReplace
		}
		transforms = append(transforms, tr)
	}
	binding, ok := hew.Lookup(hew.FormatJSON)
	require.True(t, ok)
	undone, err := binding.Applier(after, hew.TransformList{
		Target: path, Format: hew.FormatJSON, Transform: transforms,
	})
	require.NoError(t, err, "the recorded inverse must apply cleanly to the file it describes")

	var gotBack, want map[string]any
	require.NoError(t, json.Unmarshal(undone, &gotBack))
	require.NoError(t, json.Unmarshal(original, &want))
	assert.Equal(t, want, gotBack,
		"applying the inverse must restore the document the write started from")
}

// A §9.7 record is an audit-trail entry, and AtomicWriteFile overwrites — so
// the filename is the only thing standing between two applies and the loss of
// the first one's record. At second resolution it was not enough: two applies
// against the same target in the same second produced the same name, and the
// evidence needed to back out the first write was destroyed on a success path.
//
// The clock is fixed here deliberately. With time.Now() inlined, this case
// could only be provoked by racing two applies inside one second, which is not
// something a test can state.
func TestFreeRecordPath_NeverOverwritesAnExistingRecord(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/home/user/.ctxloom/records"
	require.NoError(t, fs.MkdirAll(dir, 0o755))
	target := "/home/user/.config/zed/settings.json"
	at := time.Date(2026, 8, 14, 22, 19, 51, 123456789, time.UTC)

	first, err := freeRecordPath(fs, dir, target, at)
	require.NoError(t, err)
	require.NoError(t, afero.WriteFile(fs, first, []byte("first record\n"), 0o644))

	second, err := freeRecordPath(fs, dir, target, at)
	require.NoError(t, err)
	assert.NotEqual(t, first, second,
		"a second apply at the SAME instant must not be handed the first record's path")

	require.NoError(t, afero.WriteFile(fs, second, []byte("second record\n"), 0o644))

	// The effect, not the report: both records are on disk with their own
	// content. Asserting only that the paths differ would pass against a
	// scheme that returned a fresh name and then clobbered anyway.
	got1, err := afero.ReadFile(fs, first)
	require.NoError(t, err)
	assert.Equal(t, "first record\n", string(got1), "the first record must survive the second apply")
	got2, err := afero.ReadFile(fs, second)
	require.NoError(t, err)
	assert.Equal(t, "second record\n", string(got2))

	// A third at the same instant keeps climbing rather than reusing -2.
	third, err := freeRecordPath(fs, dir, target, at)
	require.NoError(t, err)
	assert.NotEqual(t, first, third)
	assert.NotEqual(t, second, third)
}

// The timestamp must carry sub-second precision, and must stay lexically
// sortable — a record set is read in apply order.
func TestApplicationRecordFilename_IsNanosecondAndSortable(t *testing.T) {
	target := "/home/user/.config/zed/settings.json"
	base := time.Date(2026, 8, 14, 22, 19, 51, 0, time.UTC)

	early := applicationRecordFilename(target, base.Add(1*time.Nanosecond))
	late := applicationRecordFilename(target, base.Add(2*time.Nanosecond))

	assert.NotEqual(t, early, late,
		"two applies one nanosecond apart must not share a filename")
	assert.Less(t, early, late,
		"record filenames must sort in apply order, so the timestamp has to stay lexical")
}

// hew's Document takes the target as a diagnostics LABEL (it does no I/O with
// it). Passing the real path is only worth anything if it actually reaches the
// message, and ctxloom's own wrap names the file too — so asserting "the error
// mentions the path" would pass even when the label is dropped. This asserts
// hew's OWN rendering instead, "hew: <target>:", which is empty of the target
// when the label is not passed through.
//
// The branch is unreachable end to end (readExisting has already parsed the
// pre-image by the time a record is written), so it is driven directly.
func TestBuildAndWriteApplicationRecord_UnparseablePreImageNamesTheTargetToHew(t *testing.T) {
	fs := afero.NewMemMapFs()
	path := "/home/user/.config/zed/settings.json"

	_, err := buildAndWriteApplicationRecord(fs, path, hew.FormatJSON,
		hew.TransformList{Target: path, Format: hew.FormatJSON},
		[]byte(`{"a":1}`), []byte("{not json"), []byte(`{"a":1}`))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "hew: "+path+":",
		"the target must reach hew's own diagnostic as its label, not only ctxloom's surrounding wrap")
}

// --- recordConfigPatch: the conversion's recursion boundary, in isolation ---

func TestRecordJSONPatch_RecursionBoundary(t *testing.T) {
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
	// Drive hew's fluent authoring surface exactly as runConfigWrite does:
	// record onto an opened Doc, then read back the IR hew lowered. The
	// source mirrors `base` because the recorded reads resolve against it.
	doc, derr := hew.OpenBytes("/x/settings.json",
		[]byte(`{"existing_obj":{"sibling":"survives"},"scalar_key":"old"}`),
		hew.As(hew.FormatJSON))
	require.NoError(t, derr)
	require.Equal(t, 4, recordConfigPatch(doc, hew.RootPath(), base, patch),
		"one op per leaf: existing_obj recurses to one, plus brand_new_obj, scalar_key and to_delete")
	tl, terr := doc.Transforms()
	require.NoError(t, terr)
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
