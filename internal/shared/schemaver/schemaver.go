// Package schemaver is the one implementation of persisted-format versioning:
// every versioned file kind declares its format generation as an integer under
// Key, reads older generations by migrating them in memory, refuses a
// generation newer than the binary understands, and persists a migration only
// when asked to (see WriteBack and the --write-upgrades switch).
//
// The format generation is independent of any binary's release version, and of
// any version a file's AUTHOR declares (a bundle's semver `version`): it is the
// one number migrations key off.
//
// Every shape change to a persisted format — a rename, a removal, a change of
// meaning — bumps the generation with a Step, even a step that edits nothing.
// An older binary does not know the new shape: unbumped, it reads a renamed
// key as a missing one and silently falls back to the default instead of
// refusing a file newer than it understands (ErrNewer). The bump is what makes
// it refuse.
//
// schemaver owns the version gate and nothing else. The parse, the steps and
// the encode are internal/shared/upgrade's; a Kind's steps are ordinary
// upgrade.Upgraders. Context-dependent normalization (anything that needs more
// than the document's own bytes to decide) is not a schema step and stays with
// the caller.
package schemaver

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/spf13/afero"
	"github.com/spf13/pflag"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/shared/upgrade"
	"github.com/ctxloom/ctxloom/internal/shared/yamlx"
)

// Key is the top-level key every versioned file kind declares its format
// generation under.
const Key = "schema_version"

// Kind is one versioned file kind.
//
// Current is DERIVED (Oldest + len(Steps)) rather than declared, so a version
// bump without the step that migrates to it cannot be written.
type Kind struct {
	// Name identifies the kind in messages, e.g. "ltk config".
	Name string
	// LegacyKey, when set, is an older spelling of Key that Upgrade renames to
	// Key BEFORE reading the version. It is per-kind opt-in because the same
	// spelling can mean something else elsewhere: a bundle's `version` is its
	// author's semver, never a format generation.
	LegacyKey string
	// Oldest is the lowest generation still migratable; below it Upgrade
	// refuses with ErrTooOld.
	Oldest int
	// Steps[i] migrates generation Oldest+i to Oldest+i+1. A step runs because
	// the document's generation says it must, not because it detects work to
	// do, so — unlike a Pipeline stage — it need not be idempotent, and one
	// that edits nothing (a marker generation) still advances the version.
	Steps []upgrade.Upgrader
}

// IntroduceKey is generation 1 of a kind that was unversioned before it
// declared Key: Oldest 0, IntroduceKey as the first step. It edits nothing,
// because a file with no version at all means exactly what a generation-1 file
// means; Upgrade's stamp is the whole migration.
var IntroduceKey upgrade.Upgrader = introduceKey{}

type introduceKey struct{}

func (introduceKey) Name() string                    { return "introduce " + Key }
func (introduceKey) Apply(*yaml.Node) (changed bool) { return false }

// Current is the generation this binary reads and writes.
func (k Kind) Current() int { return k.Oldest + len(k.Steps) }

// Result is a document brought to the current generation in memory.
type Result struct {
	// Data is the document at To. When nothing changed it IS the input slice.
	Data []byte
	// From is the generation the input declared; To is the one Data declares.
	From, To int
	// Applied names every change made, in order: the legacy-key rename, then
	// each step run. Empty means Data is the input, byte for byte, and there
	// is nothing to write back.
	Applied []string
}

// Version failures. Each reaches the caller wrapped in a *VersionError naming
// the kind and the numbers; test with errors.Is/As, never by message text.
var (
	ErrNewer      = errors.New("written by a newer format than this binary reads; upgrade the binary")
	ErrTooOld     = errors.New("older than the oldest format this binary can migrate")
	ErrUnreadable = errors.New("format version cannot be read")

	errNotMapping = errors.New("the document is not a mapping")
	errNotInteger = errors.New(Key + " is not an integer")
	errBothKeys   = errors.New("both " + Key + " and its legacy spelling are present")
	errDuplicate  = errors.New("a mapping repeats a key")
)

// VersionError is a refused document. Err is (or wraps) ErrNewer, ErrTooOld
// or ErrUnreadable.
type VersionError struct {
	Kind                   string
	Found, Current, Oldest int
	Err                    error
}

func (e *VersionError) Error() string {
	switch {
	case errors.Is(e.Err, ErrNewer):
		return fmt.Sprintf("%s: %s %d: %v (this binary reads up to %d)", e.Kind, Key, e.Found, e.Err, e.Current)
	case errors.Is(e.Err, ErrTooOld):
		return fmt.Sprintf("%s: %s %d: %v (the oldest is %d)", e.Kind, Key, e.Found, e.Err, e.Oldest)
	default:
		return fmt.Sprintf("%s: %v", e.Kind, e.Err)
	}
}

func (e *VersionError) Unwrap() error { return e.Err }

func (k Kind) refuse(found int, err error) *VersionError {
	return &VersionError{Kind: k.Name, Found: found, Current: k.Current(), Oldest: k.Oldest, Err: err}
}

func (k Kind) unreadable(cause error) *VersionError {
	return k.refuse(0, fmt.Errorf("%w: %w", ErrUnreadable, cause))
}

// Upgrade brings data to k.Current() in memory. It reads the RAW bytes, so it
// runs before any strict decode or schema validation the caller applies to
// the result:
//
//  1. rename LegacyKey to Key, when the kind opts in;
//  2. read the generation — an empty or comment-only document is generation
//     0; a non-integer version, a non-mapping document or more than one
//     document is ErrUnreadable; above Current is ErrNewer, below Oldest is
//     ErrTooOld;
//  3. run the steps from that generation up;
//  4. stamp Current, when anything changed.
//
// A document already current passes through untouched: Result.Data is the
// input slice and Applied is empty.
func (k Kind) Upgrade(data []byte) (Result, error) {
	doc, commentOnly, err := k.parse(data)
	if err != nil {
		return Result{}, err
	}
	root := doc.Content[0]
	found, applied, err := k.gate(root)
	if err != nil {
		return Result{}, err
	}
	for _, step := range k.Steps[found-k.Oldest:] {
		step.Apply(root)
		applied = append(applied, step.Name())
	}
	if len(applied) == 0 {
		return Result{Data: data, From: found, To: found}, nil
	}
	out, err := k.encode(&doc, data, commentOnly)
	if err != nil {
		return Result{}, err
	}
	return Result{Data: out, From: found, To: k.Current(), Applied: applied}, nil
}

// parse returns data's single document with a mapping root. An empty,
// comment-only or null document comes back as an empty mapping; commentOnly
// reports that yaml.v3 kept no node for it at all.
func (k Kind) parse(data []byte) (doc yaml.Node, commentOnly bool, err error) {
	doc, err = upgrade.DecodeSingle(data)
	commentOnly = errors.Is(err, io.EOF)
	switch {
	case commentOnly || (err == nil && isNullDocument(&doc)):
		empty := yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
		return empty, commentOnly, nil
	case err != nil:
		return doc, false, k.unreadable(err)
	case doc.Content[0].Kind != yaml.MappingNode:
		return doc, false, k.unreadable(errNotMapping)
	}
	return doc, false, nil
}

// gate renames the legacy key, then reads and checks the declared
// generation. applied carries the rename when one happened.
func (k Kind) gate(root *yaml.Node) (found int, applied []string, err error) {
	if k.LegacyKey != "" {
		renamed, err := renameKey(root, k.LegacyKey)
		if err != nil {
			return 0, nil, k.unreadable(err)
		}
		if renamed {
			applied = append(applied, renameStepName(k.LegacyKey))
		}
	}
	found, ok := upgrade.Version(root, Key)
	switch {
	case !ok:
		return 0, nil, k.unreadable(errNotInteger)
	case found > k.Current():
		return 0, nil, k.refuse(found, ErrNewer)
	case found < k.Oldest:
		return 0, nil, k.refuse(found, ErrTooOld)
	}
	return found, applied, nil
}

// encode stamps and serializes a changed document.
func (k Kind) encode(doc *yaml.Node, data []byte, commentOnly bool) ([]byte, error) {
	root := doc.Content[0]
	if upgrade.HasDuplicateKey(root) {
		return nil, k.unreadable(errDuplicate)
	}
	k.Stamp(root)
	out, err := upgrade.Encode(doc)
	if err != nil {
		return nil, k.unreadable(err)
	}
	if commentOnly && len(strings.TrimSpace(string(data))) > 0 {
		// yaml.v3 keeps no node for a comment-only stream, so the comments —
		// the file's whole content — are carried over as bytes.
		out = append([]byte(strings.TrimRight(string(data), "\n")+"\n"), out...)
	}
	return out, nil
}

// isNullDocument reports a document whose only content is null (`---`, `~`):
// as empty as an empty file.
func isNullDocument(doc *yaml.Node) bool {
	return len(doc.Content) == 1 && doc.Content[0].Kind == yaml.ScalarNode && doc.Content[0].Tag == "!!null"
}

// renameKey renames legacy to Key in place, keeping its position and
// comments. Both spellings present is refused: there is no safe way to pick.
func renameKey(root *yaml.Node, legacy string) (renamed bool, err error) {
	if yamlx.MapValue(root, legacy) == nil {
		return false, nil
	}
	if yamlx.MapValue(root, Key) != nil {
		return false, errBothKeys
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == legacy {
			root.Content[i].Value = Key
			break
		}
	}
	return true, nil
}

func renameStepName(legacy string) string { return "rename " + legacy + " to " + Key }

// Stamp sets Key to k.Current() on a root mapping: in place when present,
// otherwise as the FIRST key, taking over the document's leading comment so a
// file header stays at the top. Writers stamp what they write.
func (k Kind) Stamp(root *yaml.Node) {
	if v := yamlx.MapValue(root, Key); v != nil {
		// Edited, not replaced, so a comment on the line survives.
		*v = yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(k.Current()),
			HeadComment: v.HeadComment, LineComment: v.LineComment, FootComment: v.FootComment}
		return
	}
	key := yamlx.ScalarNode(Key)
	value := yamlx.ScalarNode(strconv.Itoa(k.Current()))
	value.Tag = "!!int"
	if len(root.Content) > 0 {
		key.HeadComment, root.Content[0].HeadComment = root.Content[0].HeadComment, ""
	}
	root.Content = append([]*yaml.Node{key, value}, root.Content...)
}

// BackupSuffix is appended to a file's path for the copy WriteBack keeps of
// the pre-upgrade bytes.
const BackupSuffix = ".bak"

// Backup is the caller's choice, per file kind, of whether WriteBack keeps the
// pre-upgrade bytes beside the file.
type Backup bool

const (
	// KeepBackup copies the file to path+BackupSuffix first. For a file
	// nothing else versions (a config), the backup is the only way back.
	KeepBackup Backup = true
	// NoBackup leaves nothing beside the file. For version-controlled project
	// content (a bundle tree, the lockfile) git holds the prior bytes, and a
	// .bak would be one more file in a committed tree — inside a bundle, one a
	// later signing would ship.
	NoBackup Backup = false
)

// WriteBack persists an upgraded document: with KeepBackup the current file
// is first copied to path+BackupSuffix, then r.Data atomically replaces it,
// both keeping the file's permission bits.
func WriteBack(fs afero.Fs, path string, r Result, backup Backup) error {
	info, err := fs.Stat(path)
	if err != nil {
		return fmt.Errorf("write back %s: %w", path, err)
	}
	perm := info.Mode().Perm()
	if backup == KeepBackup {
		old, err := afero.ReadFile(fs, path)
		if err != nil {
			return fmt.Errorf("write back %s: %w", path, err)
		}
		// An empty original is a legitimate generation-0 file; its backup is
		// empty too.
		if err := safefs.WriteFile(fs, path+BackupSuffix, old, perm, safefs.AllowEmpty()); err != nil {
			return fmt.Errorf("write back %s: back up: %w", path, err)
		}
	}
	if err := safefs.WriteFile(fs, path, r.Data, perm); err != nil {
		return fmt.Errorf("write back %s: %w", path, err)
	}
	return nil
}

// WriteUpgradesFlag is the persistent flag every binary that loads versioned
// files carries: with it, a load that migrated a file persists the migration
// (WriteBack); without it the file on disk is never changed.
const WriteUpgradesFlag = "write-upgrades"

// writeUpgrades is process-wide because load sites sit layers below any
// command — in loaders with no cobra.Command to ask — and the answer is one
// per invocation. Atomic because those loads may run on goroutines the flag
// parse never sees.
var writeUpgrades atomic.Bool

// BindWriteUpgrades registers WriteUpgradesFlag on fs, writing straight into
// the process-wide switch WriteUpgrades reads. Binding RESETS the switch: a
// command tree is bound once per invocation, so a tree built after one that
// set it (tests drive a root repeatedly in one process) starts off.
func BindWriteUpgrades(fs *pflag.FlagSet) {
	writeUpgrades.Store(false)
	fs.Var(writeUpgradesValue{}, WriteUpgradesFlag,
		"Persist in-memory upgrades of older-format files (a config's old file is kept as <file>"+BackupSuffix+"; version-controlled project content keeps none, git holds it)")
	fs.Lookup(WriteUpgradesFlag).NoOptDefVal = "true"
}

// WriteUpgrades reports whether this invocation asked for migrations to be
// written back.
func WriteUpgrades() bool { return writeUpgrades.Load() }

type writeUpgradesValue struct{}

func (writeUpgradesValue) String() string { return strconv.FormatBool(writeUpgrades.Load()) }
func (writeUpgradesValue) Type() string   { return "bool" }
func (writeUpgradesValue) Set(s string) error {
	on, err := strconv.ParseBool(s)
	if err != nil {
		return err
	}
	writeUpgrades.Store(on)
	return nil
}
