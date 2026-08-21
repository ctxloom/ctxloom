// Package schemaver is the single spelling of ctxloom's on-disk schema
// version across every file kind (config, bundles, profiles, the session
// index, ...). Every kind declares its version under the same key, migrates
// through the same upgrade.Pipeline machinery, and reads its declared version
// through the same three-way convention. Before this package each kind
// answered "what version is this file" its own way, and the two existing
// answers disagreed on what a missing key means (see Declared below) — that
// divergence is the bug this package exists to close, one call site at a
// time as each kind is migrated onto it.
//
// schemaver builds on internal/shared/upgrade (the Pipeline/Upgrader engine)
// rather than replacing it: Kind is a thin, named wrapper around a Pipeline
// plus the version it targets, and RenameUpgrade is one more Upgrader a
// kind's own Pipeline includes explicitly, in the position its doc comment
// requires. This package does not wire itself into any existing file kind;
// that is later, per-kind work.
package schemaver

import (
	"bytes"
	"errors"
	"io"

	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/shared/upgrade"
)

// Key is the only schema-version spelling ctxloom writes going forward.
const Key = "schema_version"

// LegacyKey is the pre-rename spelling. ctxloom still READS it — via
// RenameUpgrade — so an old on-disk file migrates instead of being refused
// for merely spelling its version the old way.
const LegacyKey = "version"

// Declared reports the schema version a raw document declares, preferring Key
// and falling back to LegacyKey when Key is absent.
//
// This follows upgrade.Version's convention, not config.declaredConfigVersion's
// — the two disagree on what a MISSING key means, and only one of them can be
// schemaver's answer:
//
//   - upgrade.Version reports a missing key as (0, true): a document with no
//     version key at all is the genuine pre-versioning generation, and every
//     migration should run over it.
//   - config.declaredConfigVersion reports a missing key as (0, false): "this
//     file does not say it is current", collapsing "never versioned" and
//     "versioned but unreadable" into one fact because that config's caller
//     only ever asked one question ("is this current or not") and had no use
//     for the distinction.
//
// schemaver keeps upgrade.Version's three-way distinction, because collapsing
// it is exactly the mistake upgrade.Version's own doc comment warns against:
// a document whose version key is PRESENT but unreadable (`schema_version:
// banana`, a float, a nested mapping) is not a pre-versioning document — it
// is probably corrupt — and treating it as generation 0 would re-run every
// migration over it and stamp the current version on the way out, replacing
// the parse error the caller would otherwise have surfaced with a clean load
// of rewritten bytes.
//
//   - neither Key nor LegacyKey present:            (0, true)  — generation 0
//   - Key (or, absent that, LegacyKey) present and
//     a plain integer scalar:                       (version, true)
//   - Key (or LegacyKey) present but not a plain
//     integer scalar:                                (0, false) — unreadable
//
// A document that fails to parse as a single YAML mapping (malformed,
// multi-document, or otherwise not a !!map at the top level) is reported as
// present-but-unreadable, (0, false): schemaver cannot tell "no version key"
// from "no readable document" in that case, and unreadable is the safer of
// the two to report, for the same reason given above.
func Declared(data []byte) (version int, ok bool) {
	root, valid := documentRoot(data)
	if !valid {
		return 0, false
	}
	if upgrade.MapValue(root, Key) != nil {
		return upgrade.Version(root, Key)
	}
	if upgrade.MapValue(root, LegacyKey) != nil {
		return upgrade.Version(root, LegacyKey)
	}
	return 0, true
}

// documentRoot parses data as a single YAML document and returns its root
// mapping node. ok is false for anything Declared cannot safely read a
// version out of: malformed YAML, an empty document, more than one document
// in the stream, or a top-level node that is not a mapping.
//
// The multi-document check mirrors upgrade.singleDocument: a plain
// yaml.Unmarshal into a yaml.Node silently decodes only the FIRST document of
// a stream and reports no error, which would make Declared answer from a
// document the file's second half might disagree with.
func documentRoot(data []byte) (root *yaml.Node, ok bool) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		return nil, false
	}
	var next yaml.Node
	if err := dec.Decode(&next); !errors.Is(err, io.EOF) {
		return nil, false
	}
	if len(doc.Content) != 1 {
		return nil, false
	}
	root = doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, false
	}
	return root, true
}

// renameToSchemaVersion is the RenameUpgrade Upgrader. See RenameUpgrade for
// its contract.
type renameToSchemaVersion struct{}

// Name identifies the upgrade in logs and the rewrite prompt.
func (renameToSchemaVersion) Name() string { return "rename version to schema_version" }

// Apply moves LegacyKey to Key. It is idempotent (a document with no
// LegacyKey is untouched) and never clobbers: a document carrying BOTH keys
// is left completely untouched and reported unchanged, because there is no
// safe way to guess which of two present values is authoritative.
func (renameToSchemaVersion) Apply(root *yaml.Node) (changed bool) {
	legacy := upgrade.MapValue(root, LegacyKey)
	if legacy == nil {
		return false
	}
	if upgrade.MapValue(root, Key) != nil {
		// Both keys present. Not ours to resolve — leave the document alone.
		return false
	}
	upgrade.MapDelete(root, LegacyKey)
	upgrade.MapSet(root, Key, legacy)
	return true
}

// RenameUpgrade returns the Upgrader that moves the legacy `version:` key to
// `schema_version:`, unconditionally and without touching the value. It is
// deliberately NOT version-gated: a rename has no version to gate on until
// after it runs.
//
// Ordering invariant — this is the whole point of this function existing
// separately from any version-gated step: RenameUpgrade MUST run before any
// Upgrader that inspects Key via upgrade.Version/Declared. Every file kind's
// own Pipeline is responsible for placing it first. If a version check runs
// before the rename, a document that is otherwise fully current but still
// spells its version `version:` reads as having NO declared version — Key is
// absent — and a caller whose refusal path treats "no declared version" as
// "too old to load" tells the user to back up and re-init over what is
// really just an unrenamed key, not a stale schema.
func RenameUpgrade() upgrade.Upgrader { return renameToSchemaVersion{} }

// Kind is one versioned file kind (config, a bundle, a profile, the session
// index, ...): a name for logs, the schema version current code writes, and
// the ordered Upgrader pipeline that brings an older document up to it.
// Upgrades may be empty — that is the expected steady state for a kind with
// no migrations registered yet, not a special case, and Migrate must work
// correctly over it.
type Kind struct {
	Name     string
	Current  int
	Upgrades upgrade.Pipeline
}

// Migrate runs k.Upgrades over data and returns whatever upgrade.Pipeline.Run
// returns: the re-encoded bytes and the names of the stages that fired, or
// the original bytes verbatim and a nil/empty applied when nothing fired or
// the input could not be safely re-encoded (malformed, non-mapping,
// multi-document, or duplicate-key — see Pipeline.Run).
//
// Migrate does not add RenameUpgrade on k's behalf. That is deliberate: which
// Upgraders run, and in what order, is k.Upgrades' own declared pipeline, and
// RenameUpgrade's ordering invariant (see its doc comment) is something each
// kind's Upgrades must satisfy for itself by listing RenameUpgrade first, the
// same way it lists every other Upgrader. Migrate hiding that would take a
// policy decision — "the rename always runs" — that belongs to each file
// kind's own on-disk format, not to this shared plumbing.
func (k Kind) Migrate(data []byte) (out []byte, applied []string) {
	// RenameUpgrade runs FIRST, always, and is not the caller's to remember.
	// The rename must precede every version-gated stage, or a document still
	// spelling its version LegacyKey reads as undeclared and a version check
	// refuses a file that is actually current.
	//
	// It is prepended here rather than left to each kind's own Upgrades list
	// because a kind with NO migrations registered is the expected steady
	// state, not an edge case. Leaving the rename to the caller makes it
	// absent exactly where nothing else would catch its absence: an empty
	// pipeline would pass a legacy-keyed document through untouched and
	// report success.
	//
	// A document already at Key is unaffected: RenameUpgrade reports no
	// change, so Run returns the original bytes verbatim without
	// reserializing.
	return append(upgrade.Pipeline{RenameUpgrade()}, k.Upgrades...).Run(data)
}
