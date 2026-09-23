package config

import (
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// WarningKind classifies a warning collected during config load, so startup
// choke owners can gate on the class (fail-loudly) instead of string-matching
// warning text. EVERY kind declared below is fatal-class in strict mode — a
// present-but-broken config or a lossy migration must abort startup — while an
// ABSENT config file stays fine and produces no warning at all. The count is
// deliberately not spelled out here: it was written as "four", a fifth kind
// was added, and the prose silently became false. StrictnessClass is the
// enumeration that matters, and it is total by construction.
type WarningKind string

const (
	// WarnKindRead: the config file exists but could not be read (EACCES, a
	// directory in its place, transient I/O).
	WarnKindRead WarningKind = "read"
	// WarnKindParse: the config file's YAML failed to unmarshal.
	WarnKindParse WarningKind = "parse"
	// WarnKindValidate: the config parsed but failed schema validation.
	WarnKindValidate WarningKind = "validate"
	// WarnKindUnknownKey: the config carries a key ctxloom does not know — a
	// typo, or a key RETIRED by a schema generation the migrator has already
	// passed (the classic: `profiles.defaults` copied out of a stale doc into a
	// current-version config). Split out of WarnKindValidate so the key can be
	// named, the near-miss suggested, and the retired key's replacement offered,
	// instead of the raw jsonschema pointer reaching the user. Silently dropping
	// it is the worst outcome: the setting looks applied and is not.
	WarnKindUnknownKey WarningKind = "unknown-key"
	// WarnKindMigrationLossy: the in-memory schema upgrade had to drop a
	// user-set value (e.g. a compaction model with no label to attach it to).
	WarnKindMigrationLossy WarningKind = "migration-lossy"
	// WarnKindLayerScope: a config LAYER carries a key whose value cannot be a
	// fact about that layer — a machine path in the committed, multi-author
	// project file; a project-scoped privilege grant filled in from a user's
	// home config (which does not lose the merge, it fills a gap the project
	// left — the escalation this exists to close); a value from the ambient
	// environment, which every child process this one spawns inherits. See
	// internal/core/config/layerscope. The value is DROPPED, exactly like
	// WarnKindUnknownKey and for the identical reason: a setting that looks
	// applied and is not is the worse outcome.
	WarnKindLayerScope WarningKind = "layer-scope"
	// WarnKindEnginelessAgent: an `agents:` entry declares no llm and no
	// profiles — nothing that could resolve an engine. That is not a degraded
	// agent, it is not an agent: `run --agent` would fall through to the
	// default binding or fail deep in launch rather than at the declaration.
	// The entry is DROPPED from the layer before the merge, for the same
	// reason WarnKindUnknownKey drops its key: an agent that lists as bound
	// to nothing is the worse outcome. Dropping it at the LAYER is also what
	// keeps a home-only `{}` out of the project file, where the next
	// project-layer save would otherwise re-serialise it from the merged view.
	WarnKindEnginelessAgent WarningKind = "engineless-agent"
)

// Kind buckets a warning kind for the fail-loudly gate. Every kind maps to
// a fatal class — that is what "fatal-class in strict mode" means — and the
// mapping lives here, beside the kinds themselves, so every consumer of a
// loaded config records the same class for the same degradation instead of
// each inventing its own table.
func (k WarningKind) Kind() report.Kind {
	if k == WarnKindMigrationLossy {
		return report.KindMigration
	}
	return report.KindConfig
}

// FixIt names the edit or command that clears a warning of this kind. The
// finding's message already carries the config path and the error detail, so
// this only has to say what to do about it.
func (k WarningKind) FixIt() string {
	switch k {
	case WarnKindRead:
		return "make the config file readable, or remove it"
	case WarnKindMigrationLossy:
		return "re-add the dropped setting in its new home (ctxloom manage config edit)"
	case WarnKindUnknownKey:
		// The message already names the key and (when known) its replacement, so
		// the fix-it only has to say where to make the edit.
		return "remove or rename the key in config.yaml (ctxloom manage config edit)"
	case WarnKindLayerScope:
		// The message already carries the specific edit (layerscope.Violation's
		// own FixIt, inlined by Message) — this is only the short pointer the
		// startup gate's listing shows alongside it.
		return "see the finding above for the exact key and where it belongs instead"
	case WarnKindEnginelessAgent:
		return "bind the agent to an llm or to profiles (ctxloom agent edit <name> --llm <label> | --profiles <p,...>), or remove it (ctxloom agent remove <name>)"
	default: // parse / validate
		return "fix the config file (ctxloom manage config edit)"
	}
}

// Warning is one non-fatal load-time diagnostic: the degradation text plus the
// kind the startup gate keys on.
type Warning struct {
	Kind WarningKind
	Text string
}

// Finding is the warning as the fail-loudly finding a sink renders and
// ledgers, so a present-but-broken config cannot open a session that silently
// runs on empty context. It is a Once finding: a long-lived server re-consults
// a loaded config from every session, and recording unconditionally would
// turn one broken file into N copies of one finding inside a single refusal.
// The ledger still re-fires it in the next window, so an unfixed config
// refuses the next session too.
func (w Warning) Finding() report.Finding {
	return report.FailOncef(w.Kind.Kind(), w.Kind.FixIt(), "%s", w.Text)
}

// ReportWarnings hands every warning a load produced to sink, as Findings.
// Every startup path that consults a loaded config (`ctxloom run`, `ctxloom
// mcp`, the GetConfig-based command entrypoints) reports through here, so
// there is one loop and one rendering.
func ReportWarnings(sink report.Sink, warnings []Warning) {
	rep := report.To(sink)
	for _, w := range warnings {
		rep.Report(w.Finding())
	}
}
