package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"

	"path/filepath"

	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/afero"
	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

// Content formats configWriteCmd understands. These are the target FILE's
// serialization, not the --format text/json OUTPUT flag (format.go) — an
// unrelated axis: config-write's own report renders as text or json exactly
// like every other command, independent of which format the config file it
// touched is written in.
const (
	configWriteFiletypeJSON = "json"
	configWriteFiletypeTOML = "toml"
)

var (
	configWriteFile     string
	configWriteFiletype string
)

// configWriteCmd is the frontend-neutral guarded-write primitive (init-as-skill
// plan, "(d) RESOLVED"): the ONE tested command that performs the dangerous
// filesystem mechanics of editing a THIRD-PARTY config file correctly, so an
// agent configuring an ACP client's fallback path (no config CLI of its own)
// never hand-rolls os.WriteFile into a user's ~/.config. It knows nothing
// about any client's schema — the caller supplies WHERE (--file, already
// resolved) and WHAT (a JSON patch on stdin); config-write supplies
// HOW-TO-WRITE-SAFELY: resolve+validate the path, back up before editing,
// parse-merge without ever truncating, fail loud on anything unparseable,
// and re-read+verify after writing.
var configWriteCmd = &cobra.Command{
	Use:   "config-write",
	Short: "Guarded merge-write into a third-party config file (internal — for agent use)",
	Long: `config-write merges a JSON patch into an existing config file (JSON or
TOML), following five rules every time:

  1. Resolve+validate the given path. --file must already be an absolute,
     fully-resolved path — this command never expands $HOME or "~" and never
     guesses a location; an env-overridden $HOME is the caller's problem to
     have already solved.
  2. If the file exists, back it up BEFORE any edit to
     NO BACKUP is taken (it once wrote "<file>.bak.<UTC-timestamp>" per call —
     overwritten by the next run).
  3. Parse the existing file (a missing file starts from an empty object) and
     deep-merge the stdin patch into it: objects merge key by key, preserving
     every foreign key the patch doesn't mention; non-object values are
     replaced. The file is never regenerated wholesale.
  4. An existing file that fails to parse STOPS the command with a clear
     error naming the file — it is never overwritten.
  5. After writing, the file is re-read and re-parsed, and the patched
     content is confirmed present (payload verification, not just a clean
     exit code). A verify failure fails loud and says the merge is already on disk
     from.

The patch is always a JSON object on stdin, whether --file is json or toml —
only the TARGET file's format varies; the shape you send never does.

--filetype is inferred from --file's extension (.json / .toml) when omitted.`,
	Hidden: true,
	Args:   cobra.NoArgs,
	RunE:   runConfigWriteCmd,
}

func runConfigWriteCmd(cmd *cobra.Command, args []string) error {
	result, err := runConfigWrite(afero.NewOsFs(), cmd, configWriteFile, configWriteFiletype)
	if err != nil {
		return err
	}
	return emit(cmd, result, func() error {
		return renderConfigWriteResult(cmd.OutOrStdout(), result)
	})
}

func init() {
	configWriteCmd.Flags().StringVar(&configWriteFile, "file", "", "absolute, already-resolved path to the target config file (required)")
	configWriteCmd.Flags().StringVar(&configWriteFiletype, "filetype", "", "target file's content format: json or toml (inferred from --file's extension if omitted)")
	_ = configWriteCmd.MarkFlagRequired("file")
	utilCmd.AddCommand(configWriteCmd)
}

// configWriteResult is config-write's machine-readable report: the payload an
// agent checks before trusting the write, not just the command's exit code
// (ctxloom's silent-no-op discipline — absence must be provable, not assumed).
type configWriteResult struct {
	File     string   `json:"file"`
	Filetype string   `json:"filetype"`
	Created  bool     `json:"created"`
	Merged   []string `json:"mergedKeys"`
	Verified bool     `json:"verified"`
}

// runConfigWrite performs the guarded merge-write end to end. Extracted from
// RunE (and taking fs explicitly) so it is testable without cobra or the real
// filesystem.
func runConfigWrite(fs afero.Fs, cmd *cobra.Command, file, filetype string) (configWriteResult, error) {
	var result configWriteResult

	if err := validateRealFilePath(file); err != nil {
		return result, err
	}
	ft, err := resolveConfigFiletype(file, filetype)
	if err != nil {
		return result, err
	}
	result.File = file
	result.Filetype = ft

	patchBytes, err := io.ReadAll(cmd.InOrStdin())
	if err != nil {
		return result, fmt.Errorf("config-write: read patch from stdin: %w", err)
	}
	patch, err := decodeConfigPatch(patchBytes)
	if err != nil {
		return result, err
	}

	base, existed, err := readExisting(fs, file, ft)
	result.Created = !existed
	if err != nil {
		return result, err
	}

	out, err := encodeConfigFile(deepMergeConfigMaps(base, patch), ft)
	if err != nil {
		return result, fmt.Errorf("config-write: encode %s: %w", file, err)
	}
	if err := writeConfigFile(fs, file, out); err != nil {
		return result, err
	}
	result.Merged = sortedKeys(patch)

	if err := verifyConfigWrite(fs, file, ft, patch); err != nil {
		return result, err
	}
	result.Verified = true

	return result, nil
}

// readExisting implements rule 4 for a target that may or may
// not exist: parse what is there (rule 4 — an unparseable file STOPS the
// command and is never overwritten),
// since a file that could not be read is a file with nothing to preserve. A
// missing target yields an empty base and existed=false.
// what failed next.
func readExisting(fs afero.Fs, file, ft string) (base map[string]any, existed bool, err error) {
	base = map[string]any{}

	existed, err = afero.Exists(fs, file)
	if err != nil {
		return base, false, fmt.Errorf("config-write: stat %s: %w", file, err)
	}
	if !existed {
		return base, false, nil
	}

	data, err := afero.ReadFile(fs, file)
	if err != nil {
		return base, true, fmt.Errorf("config-write: read %s: %w", file, err)
	}
	base, err = decodeConfigFile(data, ft)
	if err != nil {
		return map[string]any{}, true, fmt.Errorf("config-write: %s is not valid %s — refusing to overwrite a file this command couldn't parse (fix or remove it by hand, then retry): %w", file, ft, err)
	}
	return base, true, nil
}

// writeConfigFile creates the target's directory if needed and writes out
// atomically.
func writeConfigFile(fs afero.Fs, file string, out []byte) error {
	if dir := filepath.Dir(file); dir != "." {
		if err := fs.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("config-write: create directory for %s: %w", file, err)
		}
	}
	if err := agent.AtomicWriteFile(fs, file, out, filepath.Base(file)); err != nil {
		return fmt.Errorf("config-write: write %s: %w", file, err)
	}
	return nil
}

// verifyConfigWrite implements rule 5: re-read the file, re-parse it, and
// confirm the patched content is actually present — payload verification, not a
// clean exit code. Every failure names what the user can recover from
// (backupClause), because this is the one moment the message has to be
// actionable.
func verifyConfigWrite(fs afero.Fs, file, ft string, patch map[string]any) error {
	reread, err := afero.ReadFile(fs, file)
	if err != nil {
		return fmt.Errorf("config-write: re-read %s after writing (verify failed) — the merge is already on disk and no backup is kept; inspect %s by hand: %w", file, file, err)
	}
	verifyData, err := decodeConfigFile(reread, ft)
	if err != nil {
		return fmt.Errorf("config-write: %s is malformed after writing (verify failed) — the merge is already on disk and no backup is kept; inspect it by hand: %w", file, err)
	}
	if !containsConfigPatch(verifyData, patch) {
		return fmt.Errorf("config-write: %s does not contain the merged content after writing (verify failed) — inspect it by hand", file)
	}
	return nil
}

// sortedKeys returns m's top-level keys in a stable order, so the report names
// the merged keys the same way on every run.
func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// validateRealFilePath enforces rule 1: never trust an env-overridden $HOME,
// never guess a location. --file must already be the caller's fully-resolved
// absolute path; a relative path or an unexpanded ~/$ reference is refused —
// resolving those safely is the caller's job (it knows which $HOME/env is
// real for the client it's configuring), not this command's.
func validateRealFilePath(path string) error {
	if path == "" {
		return fmt.Errorf("config-write: --file is required")
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("config-write: --file must be an absolute, already-resolved path (got %q) — this command never expands $HOME or guesses a location; resolve it yourself before calling", path)
	}
	if strings.Contains(path, "~") || strings.Contains(path, "$") {
		return fmt.Errorf("config-write: --file must not contain an unexpanded ~ or $ reference (got %q) — resolve it yourself; this command never trusts an env-overridden $HOME", path)
	}
	return nil
}

// resolveConfigFiletype returns explicit if it names a supported format, else
// infers one from file's extension.
func resolveConfigFiletype(file, explicit string) (string, error) {
	if explicit != "" {
		switch explicit {
		case configWriteFiletypeJSON, configWriteFiletypeTOML:
			return explicit, nil
		default:
			return "", fmt.Errorf("config-write: unsupported --filetype %q (supported: %s, %s)", explicit, configWriteFiletypeJSON, configWriteFiletypeTOML)
		}
	}
	switch strings.ToLower(filepath.Ext(file)) {
	case ".json":
		return configWriteFiletypeJSON, nil
	case ".toml":
		return configWriteFiletypeTOML, nil
	default:
		return "", fmt.Errorf("config-write: cannot infer a filetype from %q; pass --filetype %s|%s", file, configWriteFiletypeJSON, configWriteFiletypeTOML)
	}
}

// decodeConfigPatch parses the stdin patch. An empty or all-whitespace body,
// or a patch that decodes to zero keys, is refused rather than silently
// succeeding as a no-op write — ctxloom's silent-no-op hazard applies to a
// primitive whose entire job is a write at least as much as anywhere else.
func decodeConfigPatch(raw []byte) (map[string]any, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, fmt.Errorf("config-write: empty patch on stdin — refusing a no-op write (pass a JSON object with at least one key)")
	}
	var patch map[string]any
	if err := json.Unmarshal(raw, &patch); err != nil {
		return nil, fmt.Errorf("config-write: patch on stdin is not a JSON object: %w", err)
	}
	if len(patch) == 0 {
		return nil, fmt.Errorf("config-write: patch is an empty JSON object — refusing a no-op write")
	}
	return patch, nil
}

// decodeConfigFile parses data in the given filetype into a generic map. An
// empty file decodes to an empty map (nothing to preserve, nothing malformed).
func decodeConfigFile(data []byte, ft string) (map[string]any, error) {
	m := map[string]any{}
	if len(bytes.TrimSpace(data)) == 0 {
		return m, nil
	}
	switch ft {
	case configWriteFiletypeJSON:
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, err
		}
	case configWriteFiletypeTOML:
		if err := toml.Unmarshal(data, &m); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported filetype %q", ft)
	}
	return m, nil
}

// encodeConfigFile serializes m in the given filetype.
func encodeConfigFile(m map[string]any, ft string) ([]byte, error) {
	switch ft {
	case configWriteFiletypeJSON:
		out, err := json.MarshalIndent(m, "", "  ")
		if err != nil {
			return nil, err
		}
		return append(out, '\n'), nil
	case configWriteFiletypeTOML:
		var buf bytes.Buffer
		if err := toml.NewEncoder(&buf).Encode(m); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	default:
		return nil, fmt.Errorf("unsupported filetype %q", ft)
	}
}

// deepMergeConfigMaps merges patch into base in place and returns it: nested
// objects merge key by key (recursively), and any other value in patch
// replaces base's value wholesale. Every base key patch doesn't mention is
// left untouched — this is rule 3's "never truncate."
func deepMergeConfigMaps(base, patch map[string]any) map[string]any {
	for k, pv := range patch {
		if bv, ok := base[k]; ok {
			if bMap, ok1 := bv.(map[string]any); ok1 {
				if pMap, ok2 := pv.(map[string]any); ok2 {
					base[k] = deepMergeConfigMaps(bMap, pMap)
					continue
				}
			}
		}
		base[k] = pv
	}
	return base
}

// containsConfigPatch implements rule 5's payload verification: it confirms
// every key/value in patch is present in data, recursing into nested objects.
// Numeric values are normalized before comparison since a JSON-parsed patch
// (always float64) and a TOML-round-tripped file (may decode integers as
// int64) would otherwise compare unequal for the same logical number.
func containsConfigPatch(data, patch map[string]any) bool {
	for k, pv := range patch {
		dv, ok := data[k]
		if !ok {
			return false
		}
		if pMap, ok2 := pv.(map[string]any); ok2 {
			dMap, ok3 := dv.(map[string]any)
			if !ok3 || !containsConfigPatch(dMap, pMap) {
				return false
			}
			continue
		}
		if !reflect.DeepEqual(normalizeConfigValue(pv), normalizeConfigValue(dv)) {
			return false
		}
	}
	return true
}

// normalizeConfigValue recursively coerces integer types to float64 so
// verification compares logical values, not a parser's incidental Go type.
func normalizeConfigValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, vv := range t {
			out[k] = normalizeConfigValue(vv)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, vv := range t {
			out[i] = normalizeConfigValue(vv)
		}
		return out
	case int:
		return float64(t)
	case int32:
		return float64(t)
	case int64:
		return float64(t)
	default:
		return v
	}
}

// renderConfigWriteResult is the human-readable twin of configWriteResult,
// used in text mode (--format json emits the struct directly via emit).
func renderConfigWriteResult(out io.Writer, r configWriteResult) error {
	w := iox.NewErrWriter(out)
	action := "updated"
	if r.Created {
		action = "created"
	}
	w.Printf("config-write: %s %s (%s)\n", action, r.File, r.Filetype)
	w.Printf("  merged keys: %s\n", strings.Join(r.Merged, ", "))
	w.Printf("  verified: %t\n", r.Verified)
	return w.Err()
}
