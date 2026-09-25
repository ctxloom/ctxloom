// Package report is the typed diagnostic a core component RETURNS or hands
// to a Sink it was given, instead of writing to the process's stderr itself.
// Rendering — the "<prog>: warning:" line, the structured envelope, the
// fatal-findings ledger — is the caller's business: the composition root
// builds one Sink and passes it in, so two components in one process can
// report to two different places and a test can read findings as values.
//
// The package is a toolbox leaf: it imports nothing of ours, so every ring
// may hold a Finding.
package report

import (
	"fmt"
	"sync"
)

// Kind classifies a finding. The empty Kind is an advisory warning: it is
// rendered and forgotten. Any other Kind names a fail-loudly class; the
// renderer records it so a startup gate can refuse to proceed on it.
type Kind string

// The fail-loudly classes. Each names the subsystem whose choke reported the
// finding; the startup gate groups and prints them by Kind, so the abort
// listing reads as a diagnosis ("[config] ...", "[sync] ...") rather than an
// undifferentiated wall of text.
const (
	// KindConfig is a present-but-broken config file (unreadable / parse /
	// schema-invalid). An absent config is fine and never a finding.
	KindConfig Kind = "config"

	// KindMigration is a lossy in-memory schema migration (a setting the
	// upgrade pipeline had to drop).
	KindMigration Kind = "migration"

	// KindSync is a lockfile-pinned item that is neither in the local cache
	// nor fetchable. A refresh failure with a complete cache stays a plain
	// warning in both modes and never reaches this class.
	KindSync Kind = "sync"

	// KindRef is an unresolvable configured reference: a default profile, a
	// profile parent, or a profile-pushed fragment that fails to load.
	KindRef Kind = "ref"

	// KindApply is a hook/MCP/settings apply failure or a context
	// regeneration failure (partial apply is no longer success in strict).
	KindApply Kind = "apply"

	// KindBundle is a load/parse failure of a lockfile-active or local
	// bundle. Builtin (in-binary) bundle failures stay warnings.
	KindBundle Kind = "bundle"

	// KindTrust is a corrupt/unreadable trust store (the deny-all posture).
	KindTrust Kind = "trust-store"

	// KindIsolation is an EXPLICITLY-requested container runtime that cannot be
	// satisfied AS REQUESTED: no reachable runtime, an unrecognized runtime axis
	// value (a typo that would silently land on the host), an external plugin
	// binary that cannot be containerized, the agent image absent/unbuildable, a
	// stale image whose refresh build failed, a configured base image
	// (isolation_base_containerfile) that failed to build, shared-fs probe
	// failed, or no resolvable auth — so the run would otherwise fall back to the
	// UNSANDBOXED host, or run a STALE/substituted image instead of the one
	// requested. Only an explicit request — an agent's `runtime:` trait, the
	// project `runtime:` default, or `--runtime container` — reaches this class;
	// the ambient host default degrades silently and never lands here.
	KindIsolation Kind = "isolation"

	// KindTask is an EXPLICITLY-requested task-store mutation that could not
	// be applied — `ctxloom run --seed-task <harp>` against a corrupt,
	// unreadable, or non-matching project task log, or a taskloom write
	// carrying a tag its tag-schema refuses (the write-side gate in
	// internal/shared/tasks/operations). Only an explicit request
	// reaches this class, mirroring KindIsolation: ambient task bookkeeping
	// that nobody asked for stays a plain warning. The point is that a user
	// who named a task must not be told the launch succeeded while the task
	// silently stayed untouched.
	KindTask Kind = "task"

	// KindOwner is a second session-owning process claiming a project
	// another live `ctxloom` already owns (coord.ErrStateOwned). A project
	// has ONE coordinator, hosted by ONE session; the loser of the owner
	// claim is refused by name rather than degraded to a rival coordinator
	// on ephemeral state. Degradable: --degraded launches the second session
	// WITHOUT agent delegation, never as a second owner.
	KindOwner Kind = "owner"
)

// Finding is one diagnostic. Text is the whole human message; Remedy is the
// one-line fix a fail-loudly finding carries (empty for advisories).
type Finding struct {
	Kind   Kind
	Text   string
	Remedy string

	// Once asks the renderer to emit at most one finding with this Text per
	// process, for sites that fire per item in a loop.
	Once bool
	// NonDegradable keeps a fail-loudly finding actionable under --degraded,
	// where ordinary ones are waived. It is declared per FINDING, never per
	// Kind, because the kind-wide reading is refuted by the code: an
	// ownership mismatch is KindIsolation and its sanctioned degraded outcome
	// is a HOST fallback that launches, while a container that died at the
	// daemon is the same kind and must not launch at all. The discriminator
	// is the doctrine's own — does LAUNCHING cause the harm? — which is a
	// property of the specific fault, not of its category. The zero value is
	// degradable: non-degradability is opt-in.
	NonDegradable bool
	// Quiet records a fail-loudly finding without rendering it — for a site
	// whose text is already delivered to the user by another channel.
	Quiet bool
}

// Fatal reports whether the finding belongs to a fail-loudly class.
func (f Finding) Fatal() bool { return f.Kind != "" }

// Warnf is an advisory finding.
func Warnf(format string, args ...any) Finding {
	return Finding{Text: fmt.Sprintf(format, args...)}
}

// WarnOncef is an advisory finding rendered at most once per Text.
func WarnOncef(format string, args ...any) Finding {
	return Finding{Text: fmt.Sprintf(format, args...), Once: true}
}

// Failf is a fail-loudly finding of the given Kind carrying its remedy.
func Failf(kind Kind, remedy, format string, args ...any) Finding {
	return Finding{Kind: kind, Remedy: remedy, Text: detailOr(kind, fmt.Sprintf(format, args...))}
}

// FailOncef is Failf rendered and recorded at most once per Text.
func FailOncef(kind Kind, remedy, format string, args ...any) Finding {
	f := Failf(kind, remedy, format, args...)
	f.Once = true
	return f
}

// FailAlwaysf is Failf that --degraded does not waive.
func FailAlwaysf(kind Kind, remedy, format string, args ...any) Finding {
	f := Failf(kind, remedy, format, args...)
	f.NonDegradable = true
	return f
}

// Recordf is Failf recorded but not rendered.
func Recordf(kind Kind, remedy, format string, args ...any) Finding {
	f := Failf(kind, remedy, format, args...)
	f.Quiet = true
	return f
}

// RecordOncef is Recordf recorded at most once per Text.
func RecordOncef(kind Kind, remedy, format string, args ...any) Finding {
	f := Recordf(kind, remedy, format, args...)
	f.Once = true
	return f
}

// detailOr refuses an empty message: a choke that reports nothing has still
// failed, and a blank line would hide that.
func detailOr(kind Kind, msg string) string {
	if msg == "" {
		return fmt.Sprintf("unspecified %s failure: the choke reported no detail", kind)
	}
	return msg
}

// Sink receives findings as they arise. A long-lived component holds one;
// a synchronous function may instead return Findings.
type Sink interface {
	Report(Finding)
}

// SinkFunc adapts a function to Sink.
type SinkFunc func(Finding)

// Report calls f.
func (f SinkFunc) Report(x Finding) { f(x) }

// Discard drops every finding.
var Discard Sink = SinkFunc(func(Finding) {})

// Findings is the value a synchronous function accumulates and returns. Its
// pointer is a Sink, so a function that takes a Sink can be handed one.
type Findings []Finding

// Report appends f.
func (fs *Findings) Report(f Finding) { *fs = append(*fs, f) }

// Append appends more.
func (fs *Findings) Append(more ...Finding) { *fs = append(*fs, more...) }

// Fatal returns the fail-loudly subset.
func (fs Findings) Fatal() Findings {
	var out Findings
	for _, f := range fs {
		if f.Fatal() {
			out = append(out, f)
		}
	}
	return out
}

// Reporter is the value a component holds to report through. Its zero value
// discards, so a struct built without a Sink (a test double, a headless
// caller) reports nowhere rather than panicking.
type Reporter struct {
	Sink Sink
}

// To returns a Reporter over s.
func To(s Sink) Reporter { return Reporter{Sink: s} }

// Report forwards f to the Sink, if any.
func (r Reporter) Report(f Finding) {
	if r.Sink != nil {
		r.Sink.Report(f)
	}
}

// Warnf reports Warnf.
func (r Reporter) Warnf(format string, args ...any) { r.Report(Warnf(format, args...)) }

// WarnOncef reports WarnOncef.
func (r Reporter) WarnOncef(format string, args ...any) { r.Report(WarnOncef(format, args...)) }

// Failf reports Failf.
func (r Reporter) Failf(kind Kind, remedy, format string, args ...any) {
	r.Report(Failf(kind, remedy, format, args...))
}

// FailOncef reports FailOncef.
func (r Reporter) FailOncef(kind Kind, remedy, format string, args ...any) {
	r.Report(FailOncef(kind, remedy, format, args...))
}

// FailAlwaysf reports FailAlwaysf.
func (r Reporter) FailAlwaysf(kind Kind, remedy, format string, args ...any) {
	r.Report(FailAlwaysf(kind, remedy, format, args...))
}

// Recordf reports Recordf.
func (r Reporter) Recordf(kind Kind, remedy, format string, args ...any) {
	r.Report(Recordf(kind, remedy, format, args...))
}

// RecordOncef reports RecordOncef.
func (r Reporter) RecordOncef(kind Kind, remedy, format string, args ...any) {
	r.Report(RecordOncef(kind, remedy, format, args...))
}

// Collector is a Sink safe for concurrent reporters that keeps every finding
// for a later reader — a test, or a component that renders at a fold point.
type Collector struct {
	mu    sync.Mutex
	found Findings
}

// Report keeps f.
func (c *Collector) Report(f Finding) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.found = append(c.found, f)
}

// All returns a copy of everything reported so far.
func (c *Collector) All() Findings {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append(Findings(nil), c.found...)
}

// Drain returns everything reported so far and forgets it.
func (c *Collector) Drain() Findings {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.found
	c.found = nil
	return out
}

// Texts returns the Text of each finding, for assertions.
func (fs Findings) Texts() []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.Text)
	}
	return out
}
