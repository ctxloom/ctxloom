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
// schemaver owns the version gate and the Step contract. The parse and the
// encode are internal/shared/upgrade's; a kind's steps live under
// internal/migrations (see its package doc). Context-dependent normalization (anything that needs more
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

// Step migrates a document from generation To()-1 to To(). It runs because
// the document's generation says it must, so — unlike an upgrade.Pipeline
// stage — it need not be idempotent and reports nothing; a step that edits
// nothing (a marker generation) still advances the version.
type Step interface {
	To() int
	Name() string
	Apply(root *yaml.Node)
}

// Kind is one versioned file kind. Its fields are unexported: a Kind exists
// only through Define, so a broken chain cannot be declared.
type Kind struct {
	name    string
	current int
	steps   []Step // ascending, contiguous, last To() == current
}

// Define declares a kind at generation current, migratable from
// current-len(steps). Current is DECLARED, never derived from the steps: the
// generation is the format the binary writes, and retiring the oldest step
// must raise Oldest without lowering it — a lowered Current would refuse
// every file already stamped as newer.
//
// It PANICS, a programming error in a shipped declaration, unless current >= 1
// and steps[i].To() == current-len(steps)+1+i for every i. Deleting the oldest
// steps therefore raises Oldest and needs no other edit; deleting a middle or
// newest one fails at package init, in every test of the owner.
func Define(name string, current int, steps ...Step) Kind {
	if current < 1 {
		panic(fmt.Sprintf("schemaver: kind %q declares generation %d; the lowest is 1", name, current))
	}
	if len(steps) > current {
		panic(fmt.Sprintf("schemaver: kind %q declares %d steps below generation %d", name, len(steps), current))
	}
	oldest := current - len(steps)
	for i, s := range steps {
		if want := oldest + 1 + i; s.To() != want {
			panic(fmt.Sprintf("schemaver: kind %q step %q migrates to %d, but its position in the chain is %d", name, s.Name(), s.To(), want))
		}
	}
	return Kind{name: name, current: current, steps: steps}
}

// Name identifies the kind in messages, e.g. "ltk config".
func (k Kind) Name() string { return k.name }

// Current is the generation this binary reads and writes.
func (k Kind) Current() int { return k.current }

// Oldest is the lowest generation still migratable; below it Upgrade refuses
// with ErrTooOld.
func (k Kind) Oldest() int { return k.current - len(k.steps) }

// StepsAbove is the chain that migrates a document at generation gen up to
// Current, oldest first: every step at Oldest, none at Current. The slice
// shares the kind's chain, so a caller replays it and never writes to it.
//
// It PANICS when gen is outside [Oldest, Current]: no chain reaches Current
// from there, and a caller asking has skipped the generation gate (Upgrade
// refuses those generations with ErrTooOld or ErrNewer before it asks).
func (k Kind) StepsAbove(gen int) []Step {
	if gen < k.Oldest() || gen > k.current {
		panic(fmt.Sprintf("schemaver: kind %q has no steps above generation %d; it migrates from %d to %d", k.name, gen, k.Oldest(), k.current))
	}
	return k.steps[gen-k.Oldest():]
}

// Result is a document brought to the current generation in memory.
type Result struct {
	// Data is the document at To. When nothing changed it IS the input slice.
	Data []byte
	// From is the generation the input declared; To is the one Data declares.
	From, To int
	// Applied names every step run, in order. Empty means Data is the input, byte for byte, and there
	// is nothing to write back.
	Applied []string
}

// Version failures. Each reaches the caller wrapped in a *VersionError naming
// the kind and the numbers; test with errors.Is/As, never by message text.
var (
	ErrNewer      = errors.New("written by a newer format than this binary reads; upgrade the binary")
	ErrTooOld     = errors.New("older than the oldest format this binary can migrate")
	ErrUnreadable = errors.New("format version cannot be read")

	errNotInteger = errors.New(Key + " is not an integer")

	// errMalformed marks a document that is not a well-formed YAML mapping.
	// It never leaves Upgrade, which passes such a document through.
	errMalformed = errors.New("not a well-formed YAML mapping")
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
	return &VersionError{Kind: k.name, Found: found, Current: k.current, Oldest: k.Oldest(), Err: err}
}

func (k Kind) unreadable(cause error) *VersionError {
	return k.refuse(0, fmt.Errorf("%w: %w", ErrUnreadable, cause))
}

// Upgrade brings data to k.Current() in memory. It reads the RAW bytes, so it
// runs before any strict decode or schema validation the caller applies to
// the result:
//
//  1. read the generation — a document with no Key (an empty or comment-only
//     one included) is generation 0; a non-integer version or more than one
//     document is ErrUnreadable; above Current is ErrNewer, below Oldest is
//     ErrTooOld;
//  2. run the steps from that generation up;
//  3. stamp Current, when any step ran.
//
// A document already current passes through untouched: Result.Data is the
// input slice and Applied is empty. So does one that is not a well-formed
// YAML mapping — a syntax error, a non-mapping root, a repeated key — with
// From and To zero: it has no generation to judge, and refusing it here would
// report a file that does not parse as a version fault, pointing at the wrong
// thing. The kind's own decode, which follows, reports it as the parse failure
// it is.
func (k Kind) Upgrade(data []byte) (Result, error) {
	r, _, err := k.migrate(data)
	return r, err
}

// Decode is Upgrade followed by the kind's decode of the result into out, from
// ONE parse: the value is decoded from the migrated tree, never from bytes
// parsed a second time. A document that is not a well-formed mapping is
// decoded from its bytes, so out's decode reports the parse failure it is.
func (k Kind) Decode(data []byte, out any) (Result, error) {
	r, doc, err := k.migrate(data)
	if err != nil {
		return Result{}, err
	}
	if doc == nil {
		return r, yaml.Unmarshal(data, out)
	}
	return r, doc.Decode(out)
}

// migrate is Upgrade, also returning the migrated tree; the tree is nil for a
// document that is not a well-formed mapping.
func (k Kind) migrate(data []byte) (Result, *yaml.Node, error) {
	doc, commentOnly, err := k.parse(data)
	if errors.Is(err, errMalformed) {
		return Result{Data: data}, nil, nil
	}
	if err != nil {
		return Result{}, nil, err
	}
	root := doc.Content[0]
	found, err := k.gate(root)
	if err != nil {
		return Result{}, nil, err
	}
	var applied []string
	for _, step := range k.StepsAbove(found) {
		step.Apply(root)
		applied = append(applied, step.Name())
	}
	if len(applied) == 0 {
		return Result{Data: data, From: found, To: found}, root, nil
	}
	out, err := k.encode(&doc, data, commentOnly)
	if err != nil {
		return Result{}, nil, err
	}
	return Result{Data: out, From: found, To: k.Current(), Applied: applied}, root, nil
}

// parse returns data's single document with a mapping root. An empty,
// comment-only or null document comes back as an empty mapping; commentOnly
// reports that yaml.v3 kept no node for it at all. A document that is not a
// well-formed mapping is errMalformed.
//
// More than one document is ErrUnreadable even when a later one is what fails
// to parse: the first parsed, so which document declares the generation is
// the fault, and a caller's single-document decode would read the first and
// never see the rest.
func (k Kind) parse(data []byte) (doc yaml.Node, commentOnly bool, err error) {
	doc, err = upgrade.DecodeSingle(data)
	commentOnly = errors.Is(err, io.EOF)
	firstParsed := doc.Kind == yaml.DocumentNode
	switch {
	case commentOnly || (err == nil && isNullDocument(&doc)):
		empty := yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
		return empty, commentOnly, nil
	case err != nil && firstParsed:
		return doc, false, k.unreadable(err)
	case err != nil,
		doc.Content[0].Kind != yaml.MappingNode,
		// A repeated key is refused by every struct/map decode, but a node
		// decode accepts it and the node helpers act on the FIRST match: a
		// migration would silently keep one entry and drop the other.
		upgrade.HasDuplicateKey(doc.Content[0]):
		return doc, false, errMalformed
	}
	return doc, false, nil
}

// gate reads and checks the declared generation.
func (k Kind) gate(root *yaml.Node) (found int, err error) {
	found, ok := upgrade.Version(root, Key)
	switch {
	case !ok:
		return 0, k.unreadable(errNotInteger)
	case found > k.current:
		return 0, k.refuse(found, ErrNewer)
	case found < k.Oldest():
		return 0, k.refuse(found, ErrTooOld)
	}
	return found, nil
}

// encode stamps and serializes a changed document.
func (k Kind) encode(doc *yaml.Node, data []byte, commentOnly bool) ([]byte, error) {
	root := doc.Content[0]
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

// Stamp sets Key to k.Current() on a root mapping: in place when present,
// otherwise as the FIRST key, taking over the document's leading comment so a
// file header stays at the top. Writers stamp what they write.
func (k Kind) Stamp(root *yaml.Node) {
	if v := yamlx.MapValue(root, Key); v != nil {
		// Edited, not replaced, so a comment on the line survives.
		*v = yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(k.current),
			HeadComment: v.HeadComment, LineComment: v.LineComment, FootComment: v.FootComment}
		return
	}
	key := yamlx.ScalarNode(Key)
	value := yamlx.ScalarNode(strconv.Itoa(k.current))
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
