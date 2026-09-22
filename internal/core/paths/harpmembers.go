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
	InPersist
	InSegments
	InEphemeral
	InHome
)

// Dir is the session-dir-relative directory the location names ("" at the
// top).
func (l MemberLocation) Dir() string {
	switch l {
	case InPersist:
		return PersistDirName
	case InSegments:
		return SegmentsDirName
	case InEphemeral:
		return EphemeralDirName
	case InHome:
		return SessionEngineHomesDirName
	default:
		return ""
	}
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
	// Mounted says a containerized run of this harp must reach the member;
	// the container's session-state mounts are DERIVED from this column
	// (MountedLocations), so moving a member (the spool) without setting it
	// is a red table test, not a silent unmount.
	Mounted bool
}

// Rel is the member's path relative to the session dir, slash-separated.
func (m HarpMember) Rel() string { return path.Join(m.Location.Dir(), m.Name) }

// HarpMembers is the table: the ONE classification every walker, reaper and
// mount list derives from. Each row is a member some writer produces under
// ~/.ctxloom/sessions/<harp>/; a name that is a pattern rather than a fixed
// leaf (an engine transcript link, a plan file) has no row and classifies to
// the directory it lives in.
//
// The essence and the next step sit at the top of the dir, beside the
// sidecar: that is where their writers put them and where the listing reads
// them (sessions.fillFromEssence), and the table records the tree as it IS.
var HarpMembers = []HarpMember{
	{Name: SessionSidecarFileName, Tier: MemberIdentity, Location: AtTop, Lifetime: Persist},
	{Name: SessionKeepMarkerFileName, Tier: MemberIdentity, Location: AtTop, Lifetime: Persist},
	{Name: EssenceFileName, Tier: MemberDerived, Location: AtTop, Lifetime: Persist},
	{Name: NextStepFileName, Tier: MemberAuthored, Location: AtTop, Lifetime: Persist},
	// The session engine homes dir holds each engine's config-home INSTANCE:
	// created at session-creation time from managed writers, engine
	// scaffolding and a one-way copy of host material, so it is rebuilt
	// rather than kept.
	{Name: SessionEngineHomesDirName, Tier: MemberMachine, Location: AtTop, Lifetime: Ephemeral},
	{Name: PersistDirName, Tier: MemberAuthored, Location: AtTop, Lifetime: Persist},
	{Name: EphemeralDirName, Tier: MemberDisposable, Location: AtTop, Lifetime: Ephemeral},
	{Name: SegmentsDirName, Tier: MemberDerived, Location: AtTop, Lifetime: Persist},
	{Name: CanonicalTranscriptFileName, Tier: MemberAuthored, Location: InPersist, Lifetime: Persist},
	{Name: TranscriptStoreDirName, Tier: MemberMachine, Location: InPersist, Lifetime: Persist},
	// Container mail rides the session-state mount: the spool is the one
	// member a containerized run MUST reach, and this column is what puts
	// its location directory in the mount list.
	{Name: SpoolDirName, Tier: MemberMachine, Location: InPersist, Lifetime: Persist, Mounted: true},
	{Name: PackageDirName, Tier: MemberMachine, Location: InPersist, Lifetime: Persist},
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

// MountedLocations is the location directories of the Mounted rows, each
// once, in table order — what a containerized run's session-state mounts
// carry. A member is reached by mounting the directory it lives in.
func MountedLocations() []string {
	var dirs []string
	seen := map[string]bool{}
	for _, m := range HarpMembers {
		if !m.Mounted {
			continue
		}
		dir := m.Location.Dir()
		if dir == "" {
			dir = m.Name
		}
		if !seen[dir] {
			seen[dir] = true
			dirs = append(dirs, dir)
		}
	}
	return dirs
}
