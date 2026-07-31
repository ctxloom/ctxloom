package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/afero"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	assert.NotEmpty(t, result.Backup)
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

	result, err := runConfigWrite(fs, cmd, path, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), path)
	assert.Contains(t, err.Error(), "refusing to overwrite")
	assert.Empty(t, result.Backup, "no backup is taken for a file we never touch")

	after, readErr := afero.ReadFile(fs, path)
	require.NoError(t, readErr)
	assert.True(t, bytes.Equal(original, after), "the original bytes must survive byte-for-byte; got: %s", after)
}

// --- Rule 2: timestamped backup BEFORE any edit ---

func TestRunConfigWrite_BackupCreatedBeforeEdit_MatchesOriginalBytes(t *testing.T) {
	fs := afero.NewMemMapFs()
	path := "/home/user/.config/zed/settings.json"
	original := []byte(`{"foreign_setting":"keep-me"}`)
	require.NoError(t, afero.WriteFile(fs, path, original, 0644))

	patch := `{"agent_servers":{"ctxloom: dev":{"command":"ctxloom"}}}`
	cmd, _ := configWriteTestCmd(patch)

	result, err := runConfigWrite(fs, cmd, path, "")
	require.NoError(t, err)
	require.NotEmpty(t, result.Backup)
	assert.True(t, strings.HasPrefix(result.Backup, path+".bak."), "backup name follows <file>.bak.<UTC-timestamp>: %s", result.Backup)

	backupBytes, err := afero.ReadFile(fs, result.Backup)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(original, backupBytes), "the backup holds the PRE-edit bytes, not the merged result")

	// The live file, meanwhile, now differs from the backup (the merge applied).
	live, err := afero.ReadFile(fs, path)
	require.NoError(t, err)
	assert.False(t, bytes.Equal(original, live))
}

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
	// atomic-write helper's internal backup read, and our own post-write
	// verify read) sees corrupt bytes instead.
	fs := &tamperedReadFs{Fs: base, path: path, corruptAt: 2, corruptVal: []byte("{not json at all")}

	patch := `{"agent_servers":{"ctxloom: dev":{"command":"ctxloom"}}}`
	cmd, _ := configWriteTestCmd(patch)

	result, err := runConfigWrite(fs, cmd, path, "")
	require.Error(t, err, "a verify step that never checks anything would return nil here")
	assert.Contains(t, err.Error(), "verify failed")
	assert.Contains(t, err.Error(), result.Backup, "the failure must point at the backup to restore from")
	assert.False(t, result.Verified)

	// The backup itself was written through the REAL (untampered) fs path
	// (a distinct file from `path`, so tamperedReadFs never intercepts it) —
	// prove it actually holds the pre-edit content, i.e. "point at the
	// backup" is a genuine recovery path, not just words in an error string.
	backupBytes, err := afero.ReadFile(base, result.Backup)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(original, backupBytes))
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

func TestRunConfigWrite_MissingFile_CreatesFreshNoBackup(t *testing.T) {
	fs := afero.NewMemMapFs()
	path := "/home/user/.config/zed/settings.json"
	patch := `{"agent_servers":{"ctxloom: dev":{"command":"ctxloom","args":["acp","--agent","dev"]}}}`
	cmd, _ := configWriteTestCmd(patch)

	result, err := runConfigWrite(fs, cmd, path, "")
	require.NoError(t, err)
	assert.True(t, result.Created)
	assert.Empty(t, result.Backup, "nothing existed before, so there is nothing to back up")
	assert.True(t, result.Verified)

	out, err := afero.ReadFile(fs, path)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(out, &got))
	servers := got["agent_servers"].(map[string]any)
	assert.Contains(t, servers, "ctxloom: dev")
}

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
	assert.NotEmpty(t, got.Backup)

	onDisk, err := os.ReadFile(path)
	require.NoError(t, err)
	var cfg map[string]any
	require.NoError(t, json.Unmarshal(onDisk, &cfg))
	assert.Equal(t, "keep", cfg["foreign"])
	servers := cfg["agent_servers"].(map[string]any)
	assert.Contains(t, servers, "ctxloom: dev")

	backupOnDisk, err := os.ReadFile(got.Backup)
	require.NoError(t, err)
	assert.JSONEq(t, `{"foreign":"keep"}`, string(backupOnDisk))
}

// freezeConfigWriteClock pins backupBeforeEdit's timestamp source so the
// same-second collision case is deterministic rather than a race with the
// wall clock: two consecutive calls in a real run almost always land in the
// same second, but "almost always" is exactly the shape of a pin that quietly
// stops proving anything.
func freezeConfigWriteClock(t *testing.T) {
	t.Helper()
	frozen := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	orig := configWriteNow
	configWriteNow = func() time.Time { return frozen }
	t.Cleanup(func() { configWriteNow = orig })
}

// config-write's rule 2 promises "a fresh backup every call — never
// overwritten by the next run", and that promise is the whole reason the
// command exists: it is the only copy of the user's pre-edit third-party
// config. The timestamp has ONE-SECOND resolution, so two edits inside the
// same second (an agent writing several keys, a retry after a failure) both
// resolve to one filename and the second call destroys the first backup —
// losing precisely the generation the user would want back.
func TestBackupBeforeEdit_SameSecondCallsDoNotOverwrite(t *testing.T) {
	freezeConfigWriteClock(t)
	fs := afero.NewMemMapFs()
	path := "/home/user/.config/zed/settings.json"

	first, err := backupBeforeEdit(fs, path, []byte(`{"gen":1}`))
	require.NoError(t, err)
	second, err := backupBeforeEdit(fs, path, []byte(`{"gen":2}`))
	require.NoError(t, err)

	assert.NotEqual(t, first, second, "a second backup in the same second must get its own name")

	got1, err := afero.ReadFile(fs, first)
	require.NoError(t, err)
	assert.Equal(t, `{"gen":1}`, string(got1), "the earlier generation must survive the later backup")
	got2, err := afero.ReadFile(fs, second)
	require.NoError(t, err)
	assert.Equal(t, `{"gen":2}`, string(got2))
}

// The same hazard across PROCESSES: a backup a previous run already left on
// disk under this second's name must not be reused either.
func TestBackupBeforeEdit_DoesNotReuseAnExistingBackupName(t *testing.T) {
	freezeConfigWriteClock(t)
	fs := afero.NewMemMapFs()
	path := "/home/user/.config/zed/settings.json"
	stale := path + ".bak.20260730T120000Z"
	require.NoError(t, afero.WriteFile(fs, stale, []byte("from an earlier run"), 0600))

	fresh, err := backupBeforeEdit(fs, path, []byte("now"))
	require.NoError(t, err)
	assert.NotEqual(t, stale, fresh)

	kept, err := afero.ReadFile(fs, stale)
	require.NoError(t, err)
	assert.Equal(t, "from an earlier run", string(kept))
}

// A verify failure is the moment the user most needs an actionable message,
// and for a file config-write CREATED there is no backup at all — Backup is
// empty by design (see TestRunConfigWrite_MissingFile_CreatesFreshNoBackup).
// Interpolating that empty string produced "... — original backed up to :
// invalid character ..." and "restore from backup " with nothing after it: a
// sentence that names a recovery path which does not exist, aimed at a user
// who is now holding a malformed config file.
//
// Note the pre-existing verify test asserts Contains(err, result.Backup),
// which is VACUOUSLY true when Backup is "" — that is exactly how this
// survived.
func TestRunConfigWrite_VerifyFailureOnCreatedFile_SaysThereIsNoBackup(t *testing.T) {
	base := afero.NewMemMapFs()
	path := "/home/user/.config/zed/settings.json"
	// Nothing exists beforehand, so there is no pre-write base read and no
	// backup read: the FIRST open of path is our own post-write verify read.
	fs := &tamperedReadFs{Fs: base, path: path, corruptAt: 1, corruptVal: []byte("{not json at all")}

	cmd, _ := configWriteTestCmd(`{"agent_servers":{"ctxloom: dev":{"command":"ctxloom"}}}`)
	result, err := runConfigWrite(fs, cmd, path, "")

	require.Error(t, err)
	assert.True(t, result.Created)
	assert.Empty(t, result.Backup)
	assert.False(t, result.Verified)
	assert.Contains(t, err.Error(), "verify failed")
	assert.NotRegexp(t, `backed up to\s*:`, err.Error(),
		"a created file has no backup; the message must not name an empty one")
	assert.NotRegexp(t, `restore from backup\s*$`, err.Error())
	assert.Contains(t, err.Error(), "no backup",
		"say plainly that there is nothing to restore from, rather than trailing off")
}

// The mirror case must keep working: when a backup WAS taken, the failure
// still names it, because that path is a real recovery instruction.
func TestRunConfigWrite_VerifyFailureOnExistingFile_StillNamesTheBackup(t *testing.T) {
	base := afero.NewMemMapFs()
	path := "/home/user/.config/zed/settings.json"
	require.NoError(t, afero.WriteFile(base, path, []byte(`{"foreign_setting":"keep-me"}`), 0644))
	fs := &tamperedReadFs{Fs: base, path: path, corruptAt: 2, corruptVal: []byte("{not json at all")}

	cmd, _ := configWriteTestCmd(`{"agent_servers":{"ctxloom: dev":{"command":"ctxloom"}}}`)
	result, err := runConfigWrite(fs, cmd, path, "")

	require.Error(t, err)
	require.NotEmpty(t, result.Backup)
	assert.Contains(t, err.Error(), result.Backup)
	assert.NotContains(t, err.Error(), "no backup")
}
