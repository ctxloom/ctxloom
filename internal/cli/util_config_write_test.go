package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
