package paths

import (
	"path"
	"strings"
)

// MemberTier classifies a session member by what it is to the session: its
// identity, something derived from it, machine data, something a human or
// engine authored, or scratch. Tiers exist for purge classification and
// doctor reporting; the reaper honours Lifetime alone.
type MemberTier int

const (
	MemberIdentity MemberTier = iota + 1
	MemberDerived
	MemberMachine
	MemberAuthored
	MemberDisposable
)

// MemberLocation is the directory of the session dir a member lives in.
type MemberLocation int

const (
	AtTop MemberLocation = iota + 1
	InTranscripts
)

// Dir is the session-dir-relative directory the location names ("" at the
// top).
func (l MemberLocation) Dir() string {
	if l == InTranscripts {
		return TranscriptsDirName
	}
	return ""
}

// Lifetime is the ONLY axis the reaper honours.
type Lifetime int

const (
	Persist Lifetime = iota + 1
	Ephemeral
)

// HarpMember is one row of the session-directory table.
type HarpMember struct {
	Name     string
	Tier     MemberTier
	Location MemberLocation
	Lifetime Lifetime
	// Mounted says a containerized run of this harp must reach the member at
	// its own relative path under the container's ~/.ctxloom/sessions/<harp>;
	// the container's session-state mounts are DERIVED from this column
	// (MountedMembers), so moving a member without setting it is a red table
	// test, not a silent unmount.
	Mounted bool
	// File says the member is a single file rather than a directory: a bind
	// source must exist as the right KIND before a runtime is asked to mount
	// it, or the runtime creates a directory in a file's place.
	File bool
}

// Rel is the member's path relative to the session dir, slash-separated.
func (m HarpMember) Rel() string { return path.Join(m.Location.Dir(), m.Name) }

// HarpMembers is the table: the ONE classification every walker, reaper and
// mount list derives from. Each row is a member some writer produces under
// ~/.ctxloom/sessions/<harp>/; a name that is a pattern rather than a fixed
// leaf (a worktree checkout, a run's scratch root) has no row and classifies
// to the directory it lives in.
//
// The session dir holds MACHINE state only. What a human reads — the essence,
// the next step, plans, segment essences, published reports — lives in the
// session's output dir (sessions.Entry.OutputDir), which no reaper touches.
var HarpMembers = []HarpMember{
	{Name: SessionSidecarFileName, Tier: MemberIdentity, Location: AtTop, Lifetime: Persist, File: true},
	{Name: SessionKeepMarkerFileName, Tier: MemberIdentity, Location: AtTop, Lifetime: Persist, File: true},
	{Name: DiagnosticsLogFileName, Tier: MemberMachine, Location: AtTop, Lifetime: Persist, File: true},
	// The engine's statusline hook appends the series from inside the
	// container, so a containerized run must reach it.
	{Name: ContextMetricsFileName, Tier: MemberMachine, Location: AtTop, Lifetime: Persist, Mounted: true, File: true},
	// Container mail: the spool is a member a containerized run MUST reach.
	{Name: SpoolDirName, Tier: MemberMachine, Location: AtTop, Lifetime: Persist, Mounted: true},
	// The runner redeems a claim-checked launch package from here, in the
	// container when it runs in one.
	{Name: PackageDirName, Tier: MemberMachine, Location: AtTop, Lifetime: Persist, Mounted: true},
	// Native history is reached by a container too, but not at its own
	// relative path: it mounts beside the engine homes so their relative
	// link resolves (isolation's nativeMount), which is why it is not a
	// Mounted row.
	{Name: NativeDirName, Tier: MemberMachine, Location: AtTop, Lifetime: Persist},
	// The runner records the canonical transcript, in the container when it
	// runs in one.
	{Name: TranscriptsDirName, Tier: MemberMachine, Location: AtTop, Lifetime: Persist, Mounted: true},
	{Name: CanonicalTranscriptFileName, Tier: MemberAuthored, Location: InTranscripts, Lifetime: Persist, File: true},
	{Name: SegmentsDirName, Tier: MemberDerived, Location: InTranscripts, Lifetime: Persist},
	// The session engine homes dir holds each engine's config-home INSTANCE:
	// created from managed writers, engine scaffolding and a one-way copy of
	// host material, so it is rebuilt rather than kept. Its native history is
	// NOT in it (NativeDirName).
	{Name: SessionEngineHomesDirName, Tier: MemberMachine, Location: AtTop, Lifetime: Ephemeral},
	// Worktree checkouts can hold the only copy of an agent's work: an
	// Ephemeral row the reaper TRIAGES (sessions.Triage) rather than takes.
	{Name: WorkDirName, Tier: MemberAuthored, Location: AtTop, Lifetime: Ephemeral},
	{Name: ScratchDirName, Tier: MemberDisposable, Location: AtTop, Lifetime: Ephemeral},
}

// ClassifyMember is the ONE predicate every walker uses: rel (relative to the
// session dir, slash-separated) is the member it names or the deepest member
// it lives under — a plan file under persist/ is persist's, a message under
// persist/spool is the spool's. The session dir itself, and a top-level name
// no row carries, classify to nothing.
func ClassifyMember(rel string) (HarpMember, bool) {
	rel = strings.Trim(path.Clean("/"+rel), "/")
	if rel == "" {
		return HarpMember{}, false
	}
	var best HarpMember
	found := false
	for _, m := range HarpMembers {
		r := m.Rel()
		if rel != r && !strings.HasPrefix(rel, r+"/") {
			continue
		}
		if !found || len(r) > len(best.Rel()) {
			best, found = m, true
		}
	}
	return best, found
}

// IdentityMember is the row whose presence makes a directory a session: the
// sidecar, at the top of the dir.
func IdentityMember() HarpMember {
	for _, m := range HarpMembers {
		if m.Tier == MemberIdentity && m.Location == AtTop {
			return m
		}
	}
	panic("paths.HarpMembers has no identity row at the top of the session dir")
}

// MountedMembers is the Mounted rows, in table order: what a containerized
// run's session-state mounts carry, each at its own relative path.
func MountedMembers() []HarpMember {
	var out []HarpMember
	for _, m := range HarpMembers {
		if m.Mounted {
			out = append(out, m)
		}
	}
	return out
}
