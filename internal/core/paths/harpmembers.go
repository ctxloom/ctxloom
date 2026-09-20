package paths

// MemberTier classifies a session member by what it is to the session: its
// identity, something derived from it, machine data, something a human or
// engine authored, or scratch.
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
func (m HarpMember) Rel() string { return "" }

// HarpMembers is the table: the ONE classification every walker, reaper and
// mount list derives from.
var HarpMembers = []HarpMember{}

// ClassifyMember is the ONE predicate every walker uses: rel (relative to the
// session dir, slash-separated) is the member it names or lives under.
func ClassifyMember(rel string) (HarpMember, bool) { return HarpMember{}, false }

// IdentityMember is the row whose presence makes a directory a session.
func IdentityMember() HarpMember { return HarpMember{} }

// MountedLocations is the location directories of the Mounted rows, each
// once — what a containerized run's session-state mounts carry.
func MountedLocations() []string { return nil }
