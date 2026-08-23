package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"time"

	"path/filepath"

	hew "github.com/benjaminabbitt/hew/go"

	"github.com/ctxloom/ctxloom/internal/confpatch"
	// hew moved its format bindings to ext/<format> (O48), and registration is
	// import-for-effect: a format this file does not import is a format this
	// build cannot apply. Both are blank imports because everything reaches
	// them through hew.Lookup rather than by calling the package directly —
	// which is what lets one code path serve every format.
	_ "github.com/benjaminabbitt/hew/go/ext/json"
	_ "github.com/benjaminabbitt/hew/go/ext/toml"
	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/afero"
	"github.com/spf13/cobra"
	yamlv3 "gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/paths"
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

// hewFormatFor maps config-write's filetype to hew's FormatID, and REFUSES a
// filetype hew cannot apply rather than silently falling back to some other
// write path. A fallback here would be the silent no-op in its most damaging
// form: the file changes, but by a mechanism that leaves no §9.7 record, so
// the evidence needed to undo it never exists.
func hewFormatFor(ft string) (hew.FormatID, error) {
	switch ft {
	case configWriteFiletypeJSON:
		return hew.FormatJSON, nil
	case configWriteFiletypeTOML:
		return hew.FormatTOML, nil
	default:
		return "", fmt.Errorf("config-write: no hew binding for filetype %q", ft)
	}
}

// emptyTargetFor is the empty document of a format, used as the pre-image when
// the target file does not exist yet. It is format-specific and deliberately
// not one shared literal: "{}" is an empty JSON object, but in TOML it is a
// parse error — an empty TOML document is zero bytes.
func emptyTargetFor(format hew.FormatID) []byte {
	if format == hew.FormatJSON {
		return []byte("{}")
	}
	return nil
}

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
	// Record is the path of the hew §9.7 application record this apply wrote
	// (~/.ctxloom/records/…). Every format config-write applies produces one —
	// JSON and TOML alike — because the record is the only durable evidence of
	// what changed in a file ctxloom does not own. See
	// buildAndWriteApplicationRecord.
	Record string `json:"record,omitempty"`
}

// runConfigWrite performs the guarded merge-write end to end. Extracted from
// RunE (and taking fs explicitly) so it is testable without cobra or the real
// filesystem.
//
// The read-modify-write span against file — readExisting through
// verifyConfigWrite — runs under agent.WithFileLock: config-write is, by its
// own doc, "the ONE tested command that performs the dangerous filesystem
// mechanics of editing a THIRD-PARTY config file correctly", and until this
// fix (D7 remainder, N3 in the fs-consolidation closing verification) it was
// the one settings-family RMW that wasn't actually locked, racing any
// concurrent write to the same target from ctxloom's own SettingsWriter
// family. Everything upstream (validating --file, resolving the filetype,
// reading and decoding the stdin patch) touches neither file nor its lock, so
// it stays outside the critical section — this is one process, one
// invocation, start to finish, so there is no cross-process-boundary gap for
// the lock to need to span.
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

	lockErr := agent.WithFileLock(fs, file, func() error {
		base, rawBefore, existed, err := readExisting(fs, file, ft)
		result.Created = !existed
		if err != nil {
			return err
		}

		var out []byte
		var tl hew.TransformList
		format, ferr := hewFormatFor(ft)
		if ferr != nil {
			return ferr
		}
		// target is the pre-image hew resolves and edits against: rawBefore
		// as read, or this format's EMPTY DOCUMENT for a file that does not
		// exist yet. Empty is format-specific and cannot be one shared
		// literal — "{}" is an empty JSON object but a parse error in TOML,
		// where empty is zero bytes. It is also the record's "before" image
		// (§9.7): the bytes the applier actually walked, not a fiction.
		target := rawBefore
		if !existed {
			target = emptyTargetFor(format)
		}
		// ONE path for every format hew can apply — JSON and TOML alike. hew
		// is a byte-preserving structural applier (§6.3): it edits only the
		// byte ranges a transform touches and copies every other byte of
		// target verbatim, unlike the re-serialize-the-whole-file merge this
		// replaces, which alphabetized keys and reindented on every write.
		//
		// The fluent document API (§A.0) is the authoring surface: edits are
		// RECORDED against an opened Doc and hew lowers them to the §9.2 IR
		// itself, rather than this file hand-assembling a TransformList.
		// Doc.Transforms() yields that same ordinary IR, which is what the
		// §9.7 record below resolves.
		doc, derr := hew.OpenBytes(file, target, hew.As(format))
		if derr != nil {
			return fmt.Errorf("config-write: open %s for hew: %w", file, derr)
		}
		// A patch whose every leaf recurses away (e.g. {"a":{}} where base
		// already holds an object at "a") records NOTHING. The authoring
		// surface treats an un-operated Doc as a caller bug and Transforms()
		// errors on it, so the no-op is caught HERE — writing target back
		// unchanged, which is what an empty transform list used to do.
		if recordConfigPatch(doc, hew.RootPath(), base, patch) == 0 {
			out = target
		} else {
			if tl, err = doc.Transforms(); err != nil {
				return fmt.Errorf("config-write: lower %s's patch to hew transforms: %w", file, err)
			}
			if out, err = doc.Bytes(); err != nil {
				return fmt.Errorf("config-write: hew apply to %s: %w", file, err)
			}
		}
		if err := writeConfigFile(fs, file, out); err != nil {
			return err
		}
		result.Merged = sortedKeys(patch)

		if err := verifyConfigWrite(fs, file, ft, patch); err != nil {
			return err
		}
		result.Verified = true

		// Every format hew applies gets a §9.7 record, TOML included — the
		// record is the only durable evidence of what was changed in a file
		// ctxloom does not own, so a format that writes without one is
		// exactly the recovery gap this path exists to close.
		recordPath, rerr := buildAndWriteApplicationRecord(fs, file, format, tl, patchBytes, target, out)
		if rerr != nil {
			return fmt.Errorf("config-write: %s was written and verified, but its hew §9.7 application record could not be written (retrying config-write is safe — every transform here is OnConflict:replace or an optional remove, so re-applying the same patch is a no-op): %w", file, rerr)
		}
		result.Record = recordPath
		return nil
	})
	if lockErr != nil {
		return result, lockErr
	}

	return result, nil
}

// readExisting implements rule 4 for a target that may or may
// not exist: parse what is there (rule 4 — an unparseable file STOPS the
// command and is never overwritten),
// since a file that could not be read is a file with nothing to preserve. A
// missing target yields an empty base and existed=false.
//
// raw is the file's exact on-disk bytes (nil when !existed) — the JSON path
// needs them as hewjson.Apply's byte-preserving pre-image, in addition to
// the decoded base map every filetype still uses to plan the merge/patch
// conversion.
func readExisting(fs afero.Fs, file, ft string) (base map[string]any, raw []byte, existed bool, err error) {
	base = map[string]any{}

	existed, err = afero.Exists(fs, file)
	if err != nil {
		return base, nil, false, fmt.Errorf("config-write: stat %s: %w", file, err)
	}
	if !existed {
		return base, nil, false, nil
	}

	data, err := afero.ReadFile(fs, file)
	if err != nil {
		return base, nil, true, fmt.Errorf("config-write: read %s: %w", file, err)
	}
	base, err = decodeConfigFile(data, ft)
	if err != nil {
		return map[string]any{}, nil, true, fmt.Errorf("config-write: %s is not valid %s — refusing to overwrite a file this command couldn't parse (fix or remove it by hand, then retry): %w", file, ft, err)
	}
	return base, data, true, nil
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

// sortedKeys returns m's keys in a stable order, so a report names them the
// same way on every run. Generic in the value type because the two callers
// carry different ones — a merged config patch, and a reconcile's
// refs-by-repository — and want the identical ordering guarantee.
func sortedKeys[V any](m map[string]V) []string {
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

// encodeConfigFile and deepMergeConfigMaps used to live here: the TOML path
// merged decoded maps and re-serialized the whole file. Both are gone now that
// every format goes through hew's byte-preserving applier — re-serializing was
// what reordered keys and reindented files ctxloom does not own, and it left no
// §9.7 record behind. recordConfigPatch below is their replacement.

// recordConfigPatch walks patch (an RFC 7386 JSON-merge-patch-shaped object,
// decodeConfigPatch's output) and RECORDS one operation per leaf it reaches
// onto doc, hew's fluent authoring surface (§A.0). hew lowers those recorded
// steps to the §9.2 IR itself — this file no longer assembles a
// TransformList by hand. It is the JSON path's equivalent of
// deepMergeConfigMaps, expressed as edits instead of a merged map; the
// recursion rule below is what keeps the two semantically aligned (rule 3's
// "preserve every foreign key").
//
//   - A null patch value is a delete: OpRemove, Optional (RFC 7386's own
//     "null means absent" semantics — removing an already-absent key is a
//     no-op, not an error). This is a DELIBERATE behavior upgrade over the
//     deep merge this replaces: the old deepMergeConfigMaps had no delete
//     semantics at all — `base[k] = pv` for a JSON null simply wrote the
//     literal value null into the file. A caller that was relying on
//     "patch a key to null" leaving a literal null in the target will now
//     see the key removed instead.
//   - An object patch value recurses ONLY while base already holds an
//     object at the same path: that is what turns a whole-subtree hew `add`
//     into deepMergeConfigMaps' "merge key by key, preserving every foreign
//     sibling" — see the TestRunConfigWrite_JSONMerge_PreservesForeignKeysAndAddsNew
//     case, where "agent_servers" recurses (base already has an object
//     there, "other-client" must survive) but "ctxloom: dev" underneath it
//     does not (base has no such key yet, so it becomes one atomic add).
//     Recursion has to stop the moment base doesn't already hold a
//     like-shaped object, because hew's `add` can create exactly one
//     missing path segment at a time (ext/json planInsert requires the
//     PARENT to already resolve) — it cannot auto-vivify a multi-level
//     path the way deepMergeConfigMaps' map assignment could.
//   - Anything else (a scalar, an array, or an object base has no match
//     for) becomes one OpAdd with OnConflict: replace — hew's upsert:
//     replaces the node if it exists, creates it if it doesn't, so the same
//     transform shape covers both of deepMergeConfigMaps' "key exists" and
//     "key is new" branches.
//
// It returns how many operations it recorded, which the caller needs because
// a patch can legitimately record NONE (every leaf recursed away) and the
// authoring surface rejects an un-operated Doc.
//
// Addressing goes through AtPath, not At's pattern string: a config key is
// arbitrary user data and may contain the characters a pattern parses (dots,
// quotes, brackets). Building the Path segment-wise cannot misread one.
func recordConfigPatch(doc *hew.Doc, prefix hew.Path, base, patch map[string]any) int {
	recorded := 0
	for _, k := range sortedKeys(patch) { // stable order: a deterministic transform list and record
		pv := patch[k]
		path := prefix.Append(hew.Segment{Kind: hew.SegKey, Name: k})
		if pv == nil {
			// Optional is RFC 7386's "null means absent": removing an
			// already-absent key is a no-op, not an error. It qualifies THIS
			// op because each key gets its own Sel.
			doc.AtPath(path).Remove().Optional()
			recorded++
			continue
		}
		if pMap, ok := pv.(map[string]any); ok {
			if bMap, ok2 := base[k].(map[string]any); ok2 {
				recorded += recordConfigPatch(doc, path, bMap, pMap)
				continue
			}
		}
		// Set is OpAdd + OnConflict:replace — hew's upsert, covering both
		// "key exists" and "key is new". The value is handed over as a plain
		// `any`: the Doc encodes it and, per §10.4, parks any failure on the
		// document for Transforms() to report, so this no longer needs
		// ValueOf and its unreachable panic.
		doc.AtPath(path).Set(pv)
		recorded++
	}
	return recorded
}

// applicationRecord is hew's §9.7 application record, in Go structs
// marshaled straight through gopkg.in/yaml.v3: struct FIELD DECLARATION
// ORDER is what v3 emits (unlike a map, whose key order it would not
// preserve), so the field order below IS the on-disk key order — no
// hand-built yaml.Node tree is needed for this.
type applicationRecord struct {
	Record    int            `yaml:"hew-record"`
	AppliedAt string         `yaml:"applied_at"`
	Patch     recordPatch    `yaml:"patch"`
	Targets   []recordTarget `yaml:"targets"`
}

type recordPatch struct {
	Source string `yaml:"source"`
	Digest string `yaml:"digest"`
}

type recordTarget struct {
	Target     string               `yaml:"target"`
	Format     string               `yaml:"format"`
	Before     string               `yaml:"before"`
	After      string               `yaml:"after"`
	Committed  bool                 `yaml:"committed"`
	Transforms []confpatch.RecordOp `yaml:"transforms"`
	// Inverse is the op list that turns the AFTER image back into the BEFORE
	// image — what a caller applies to undo this application.
	//
	// It is stored rather than derived later because deriving it later needs
	// the before-image, and the record deliberately keeps only a DIGEST of
	// that: these records describe FOREIGN files, and the files config-write
	// edits include MCP server configs whose env blocks carry API keys, so a
	// verbatim copy would put secrets in an unencrypted directory. An inverse
	// op list carries only what an op actually replaced.
	//
	// Reversing an application that CREATED the file yields removes, leaving
	// an empty document rather than an absent one. That is honest — the record
	// states what it can undo — and the caller that wants the file gone can
	// see Before was the empty document.
	Inverse []confpatch.RecordOp `yaml:"inverse,omitempty"`
}

// resolveAppliedTransforms projects tl onto the pre-image, producing the
// resolved list §9.7 requires — concrete indices, key-matches collapsed.
//
// This used to resolve one transform at a time so it could catch and drop an
// optional remove whose key was never there: that is a zero-edit no-op to the
// applier, but Resolve rejected it as HEW013. hew.Resolve honors Optional
// itself now (OP-06), skipping a missing optional address, so the tolerance
// lives once in hew rather than in every caller that resolves what it applied.
func resolveAppliedTransforms(tl hew.TransformList, doc hew.Document, target string) ([]hew.ResolvedOp, error) {
	ops, err := hew.Resolve(tl, doc)
	if err != nil {
		return nil, fmt.Errorf("resolve the applied transforms against %s: %w", target, err)
	}
	return ops, nil
}

// buildAndWriteApplicationRecord resolves tl against before (the exact bytes
// hewjson.Apply just walked — see hew.Resolve's doc: "the pre-image is the
// right document to resolve against") and writes one hew §9.7 application
// record to ~/.ctxloom/records/, home-rooted for the same reason
// paths.HomePathFor's lock sidecars are: file is FOREIGN, ctxloom does
// not own it, and so must never leave its own state beside it.
//
// What this achieves, precisely, and what it does not: hew's Resolve
// (§9.2) is implemented and exported today, so the record's "transforms"
// are genuinely the resolved list §9.7 specifies, with real before/after
// digests — not a placeholder. What is NOT built, because hew does not
// build it yet, is anything that CONSUMES a record: no `hew revert`, no
// ledger integration (§9.7's own "Future work" names both as undesigned
// P5 questions). This closes distinct-bullpen's "config-write has no
// recovery path for a foreign file" only up to "the evidence needed for a
// human or a future tool to recover exists on disk" — not up to "ctxloom
// can undo it."
func buildAndWriteApplicationRecord(fs afero.Fs, target string, format hew.FormatID, tl hew.TransformList, patchBytes, before, after []byte) (string, error) {
	// The reader comes from the REGISTRY, not from a named ext package: one
	// code path serves every format, and which formats it can serve is decided
	// by this file's blank imports rather than by a switch. A binding with no
	// Document is a HALF binding — it can apply but not read — and a §9.7
	// record needs the read side, so refuse rather than write a record that
	// states nothing.
	binding, ok := hew.Lookup(format)
	if !ok || binding.Document == nil {
		return "", fmt.Errorf("hew has no document reader for %q, so the applied transforms cannot be resolved into a §9.7 record", format)
	}
	// target is hew's diagnostics LABEL here, not a path it opens — Document
	// does no I/O. Passing the real path means a parse failure names the file
	// in hew's own error as well as in the wrap below.
	doc, err := binding.Document(target, before)
	if err != nil {
		return "", fmt.Errorf("parse %s's pre-image to resolve the applied transforms: %w", target, err)
	}
	ops, err := resolveAppliedTransforms(tl, doc, target)
	if err != nil {
		return "", err
	}

	inverse, err := inverseOps(binding, format, target, after, before)
	if err != nil {
		return "", err
	}

	at := time.Now().UTC()
	rec := applicationRecord{
		Record:    1,
		AppliedAt: at.Format(time.RFC3339),
		Patch:     recordPatch{Source: "-", Digest: sha256Digest(patchBytes)},
		Targets: []recordTarget{{
			Target:     target,
			Format:     string(format),
			Before:     sha256Digest(before),
			After:      sha256Digest(after),
			Committed:  true,
			Transforms: confpatch.ResolvedOpsToRecord(ops),
			Inverse:    confpatch.ResolvedOpsToRecord(inverse),
		}},
	}

	out, err := yamlv3.Marshal(rec)
	if err != nil {
		return "", fmt.Errorf("marshal application record: %w", err)
	}

	recordsDir, err := paths.HomeRecordsDir()
	if err != nil {
		return "", err
	}
	if err := fs.MkdirAll(recordsDir, 0755); err != nil {
		return "", fmt.Errorf("create %s: %w", recordsDir, err)
	}
	recordPath, err := freeRecordPath(fs, recordsDir, target, at)
	if err != nil {
		return "", err
	}
	if err := agent.AtomicWriteFile(fs, recordPath, out, filepath.Base(recordPath)); err != nil {
		return "", fmt.Errorf("write %s: %w", recordPath, err)
	}
	return recordPath, nil
}

// inverseOps is the op list that turns after back into before — what a caller
// applies to undo this application.
//
// hew.Invert owns the derivation, including the direction. This used to
// assemble it here by calling hew's differ with the arguments swapped, which
// worked but put a silently-reversible decision in a consumer: Diff(before,
// after) and Diff(after, before) are both well-formed and only one undoes
// anything. hew decides it once now.
//
// Resolved against the AFTER image because that is the document an undo would
// be applied to: the pointers have to name positions in the file as it stands
// now, not as it stood before the write. That obligation is the caller's —
// Invert returns the abstract list, as DiffTrees does.
func inverseOps(b hew.Binding, format hew.FormatID, target string, after, before []byte) ([]hew.ResolvedOp, error) {
	if b.Document == nil {
		return nil, fmt.Errorf("hew cannot read %q, so this application records no way to undo itself", format)
	}
	tl, err := hew.Invert(format, before, after, hew.DiffOptions{Target: target})
	if err != nil {
		return nil, fmt.Errorf("derive the inverse of the application to %s: %w", target, err)
	}
	doc, err := b.Document(target, after)
	if err != nil {
		return nil, fmt.Errorf("parse %s's post-image to resolve the inverse: %w", target, err)
	}
	return hew.Resolve(tl, doc)
}

// freeRecordPath is the record path that does not already exist, disambiguating
// with a counter when it does.
//
// AtomicWriteFile OVERWRITES, so the timestamp alone was the whole defence and
// it was not one: at second resolution two applies against the same target in
// the same second produced the same name and the second silently destroyed the
// first — the exact evidence the §9.7 record exists to preserve, gone on a
// success path with a success message. Nanoseconds make that vanishingly
// unlikely; this loop makes it IMPOSSIBLE, which is the difference between an
// invariant and a hope.
//
// It disambiguates rather than refusing because refusing also loses a record:
// the point is that no apply goes unrecorded, not that a particular filename
// is available.
func freeRecordPath(fs afero.Fs, dir, target string, at time.Time) (string, error) {
	base := applicationRecordFilename(target, at)
	path := filepath.Join(dir, base)
	for n := 2; ; n++ {
		exists, err := afero.Exists(fs, path)
		if err != nil {
			return "", fmt.Errorf("check %s: %w", path, err)
		}
		if !exists {
			return path, nil
		}
		path = filepath.Join(dir, strings.TrimSuffix(base, recordFileSuffix)+fmt.Sprintf("-%d", n)+recordFileSuffix)
	}
}

// applicationRecordFilename flattens target the same way
// paths.HomePathFor's flattenLockName flattens a protected path into one
// filename component (forward-slash it, then "/" -> "__"); the two are
// independent copies of the same convention, not a shared function, for the
// reason HomeLocksDirName's doc gives for a package outside internal/paths
// keeping its own copy — crossing a
// package boundary to share three lines of string manipulation is not worth
// the coupling. Suffixed with a sortable UTC timestamp because a record is
// an audit trail entry, not a mutable sidecar: two applies against the same
// target must not overwrite each other's record.
//
// NANOSECONDS, not seconds. The format is still lexically sortable, and the
// clock is a parameter so the collision case is reachable from a test — with
// time.Now() inlined here, two applies could only be made to collide by
// running them inside the same second, which is a race a test cannot state.
// freeRecordPath is what actually enforces the no-overwrite invariant.
func applicationRecordFilename(target string, at time.Time) string {
	flat := strings.ReplaceAll(filepath.ToSlash(target), "/", "__")
	return flat + "__" + at.UTC().Format("20060102T150405.000000000Z") + recordFileSuffix
}

// recordFileSuffix is the record's extension, named once because
// freeRecordPath has to split a filename on it to insert its counter.
const recordFileSuffix = ".hew-record.yaml"

func sha256Digest(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// containsConfigPatch implements rule 5's payload verification: it confirms
// every key/value in patch is present in data, recursing into nested objects.
// Numeric values are normalized before comparison since a JSON-parsed patch
// (always float64) and a TOML-round-tripped file (may decode integers as
// int64) would otherwise compare unequal for the same logical number.
func containsConfigPatch(data, patch map[string]any) bool {
	for k, pv := range patch {
		if pv == nil {
			// A null patch value is a delete on the JSON/hew path
			// (recordConfigPatch OpRemove) — verification for that key must
			// be "now absent", the mirror image of every other key's
			// "now present with this value", not a literal-null lookup.
			if _, stillThere := data[k]; stillThere {
				return false
			}
			continue
		}
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
	if r.Record != "" {
		w.Printf("  application record: %s\n", r.Record)
	}
	return w.Err()
}
