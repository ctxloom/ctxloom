package paths

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestStatePath_JoinsAppPathAndStateDir(t *testing.T) {
	got := StatePath("/proj/.ctxloom")
	want := filepath.Join("/proj/.ctxloom", "state")
	if got != want {
		t.Errorf("StatePath() = %q, want %q", got, want)
	}
}

func TestDirtyTreeCommitAckPath_UnderState(t *testing.T) {
	got := DirtyTreeCommitAckPath("/proj/.ctxloom")
	want := filepath.Join("/proj/.ctxloom", "state", "dirty_tree_commit_ack.yaml")
	if got != want {
		t.Errorf("DirtyTreeCommitAckPath() = %q, want %q", got, want)
	}
}

func TestTier_String(t *testing.T) {
	tests := []struct {
		tier Tier
		want string
	}{
		{TierCommitted, "committed"},
		{TierDerived, "derived"},
		{TierLocal, "local"},
		{Tier(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.tier.String(); got != tt.want {
			t.Errorf("Tier(%d).String() = %q, want %q", tt.tier, got, tt.want)
		}
	}
}

// TestLayout_EveryEntryHasARelPath guards against a copy-paste Entry left
// with a zero-value Rel, which would make Layout() silently skip a real path
// (Rel == "" resolves to appPath itself, joining nothing).
func TestLayout_EveryEntryHasARelPath(t *testing.T) {
	for _, e := range Layout() {
		if e.Rel == "" {
			t.Errorf("Layout() entry with tier %s has an empty Rel", e.Tier)
		}
	}
}

// TestLayout_RelPathsAreUnique guards against two Entry values naming the
// same path under two different tiers, which would make "what tier is this
// path" ambiguous -- exactly the classification confusion Layout exists to
// resolve. The key is (Root, Rel), not Rel alone: a RootProject entry and a
// RootHome entry legitimately share a Rel (".ctxloom/sessions" under the
// project root and again under home) because Root makes them two different
// physical paths, the same way two directories on a filesystem can share a
// leaf name.
func TestLayout_RelPathsAreUnique(t *testing.T) {
	type key struct {
		root RootKind
		rel  string
	}
	seen := map[key]Tier{}
	for _, e := range Layout() {
		k := key{e.Root, e.Rel}
		if prior, ok := seen[k]; ok {
			t.Errorf("Layout() lists %q under root %s twice, as both %s and %s", e.Rel, e.Root, prior, e.Tier)
		}
		seen[k] = e.Tier
	}
}

// TestEntry_ZeroValueRootIsProject pins Entry's own documented contract: an
// Entry declared with no Root field behaves exactly as it did before RootKind
// existed. Mutation: swap RootKind's iota order so RootHome is the zero
// value, and this goes red because ResolveRoot for a bare {Rel: ...} literal
// would then return home instead of the project root.
func TestEntry_ZeroValueRootIsProject(t *testing.T) {
	e := Entry{Rel: "x"}
	if e.Root != RootProject {
		t.Fatalf("zero-value Entry.Root = %v, want RootProject", e.Root)
	}
	got := e.ResolveRoot("/proj/.ctxloom", "/home/alice")
	want := "/proj"
	if got != want {
		t.Errorf("zero-value Entry.ResolveRoot(appDir, home) = %q, want %q (the project root, unaffected by home)", got, want)
	}
}

// TestEntry_ZeroValuePresenceIsMustExist pins Presence's zero value the same
// way: an Entry declared before Presence existed keeps warning on absence.
func TestEntry_ZeroValuePresenceIsMustExist(t *testing.T) {
	e := Entry{Rel: "x"}
	if e.Presence != PresenceMustExist {
		t.Fatalf("zero-value Entry.Presence = %v, want PresenceMustExist", e.Presence)
	}
}

// TestLayout_HomeRowsResolveUnderHomeNeverProject is the RootHome resolution
// mutation-kill target: every RootHome entry must resolve under the home
// argument, never the project's. Mutation: have ResolveRoot ignore Root and
// always return filepath.Dir(appDir) -- every one of these goes red because
// the resolved path stops containing the home directory at all.
func TestLayout_HomeRowsResolveUnderHomeNeverProject(t *testing.T) {
	const appDir = "/proj/.ctxloom"
	const home = "/home/alice"
	var sawHomeRow bool
	for _, e := range Layout() {
		if e.Root != RootHome {
			continue
		}
		sawHomeRow = true
		got := e.ResolveRoot(appDir, home)
		if got != home {
			t.Errorf("%q (RootHome) resolves onto %q, want the home root %q", e.Rel, got, home)
		}
		full := filepath.Join(got, e.Rel)
		if !strings.HasPrefix(full, home+string(filepath.Separator)) {
			t.Errorf("%q (RootHome) joined path %q does not live under home %q", e.Rel, full, home)
		}
		if strings.HasPrefix(full, "/proj") {
			t.Errorf("%q (RootHome) joined path %q resolved under the PROJECT root, not home", e.Rel, full)
		}
	}
	if !sawHomeRow {
		t.Fatal("Layout() has no RootHome entry at all -- this test would be vacuous")
	}
}

// TestLayout_HomeRowsNameStoreRootsOnly keeps the new RootHome rows honest
// the same way TestLayout_HasNoHarpKeyedRows keeps the project rows honest:
// a home row must name a fixed store root (a single leaf, or "cache/<leaf>"
// for the one cache-shaped store), never a harp- or project-key-keyed
// instance path underneath one.
func TestLayout_HomeRowsNameStoreRootsOnly(t *testing.T) {
	allowedHomeRels := map[string]bool{
		filepath.Join(AppDirName, SessionsDir):                      true,
		filepath.Join(AppDirName, ApprovalsDirName):                 true,
		filepath.Join(AppDirName, AllowedSignersFileName):           true,
		filepath.Join(AppDirName, DistrustedSignersFileName):        true,
		filepath.Join(AppDirName, CacheDir, TriggersDir):            true,
		filepath.Join(AppDirName, CoordDirName):                     true,
		filepath.Join(AppDirName, CompanionConsentFileName+".yaml"): true,
		filepath.Join(AppDirName, HomeLocksDirName):                 true,
	}
	for _, e := range Layout() {
		if e.Root != RootHome {
			continue
		}
		if !allowedHomeRels[e.Rel] {
			t.Errorf("Layout() home row %q is not a known store root -- if this is a deliberate new store, add it to this test's allowlist; if it is an instance path (harp- or project-key-keyed), it must not have a row at all", e.Rel)
		}
	}
}

// TestLayout_OnlyTierLocalOmitsRebuild pins Entry's own documented contract:
// Rebuild == "" iff Tier == TierLocal.
func TestLayout_OnlyTierLocalOmitsRebuild(t *testing.T) {
	for _, e := range Layout() {
		if e.Tier == TierLocal {
			if e.Rebuild != "" {
				t.Errorf("%q is TierLocal but declares a Rebuild command (%q); TierLocal means nothing rebuilds it", e.Rel, e.Rebuild)
			}
			if e.Lost == "" {
				t.Errorf("%q is TierLocal but has no Lost text -- doctor's report would name the path with nothing to say about it", e.Rel)
			}
			continue
		}
		if e.Tier == TierDerived && e.Rebuild == "" {
			t.Errorf("%q is TierDerived but names no Rebuild command", e.Rel)
		}
	}
}

// TestLayout_RebuildNamesACtxloomCommand pins Rebuild's own doc — "names the
// COMMAND that reconstructs this path" — as something checkable. A Rebuild
// string is the only instruction a user gets after deleting a derived path, and
// prose ("regenerated automatically") reads fine right up to the moment someone
// needs to actually do it.
func TestLayout_RebuildNamesACtxloomCommand(t *testing.T) {
	for _, e := range Layout() {
		if e.Rebuild == "" {
			continue
		}
		if !strings.HasPrefix(e.Rebuild, "ctxloom ") {
			t.Errorf("%q declares Rebuild %q, which does not start with a runnable `ctxloom ` command", e.Rel, e.Rebuild)
		}
	}
}

// TestLayout_ListsTheAssembledContextCache closes the one hole in Layout's own
// claim to enumerate "every path this tree's own writers produce": the
// content-addressed context files agent.WriteContextFile has always written
// under cache/context had no row at all, so nothing classified them and doctor
// could not name them.
//
// TierDerived, and it stays in cache/ on purpose: the file is content-ADDRESSED
// — a function of the fragment set, not of the session — so two sessions
// assembling the same context legitimately share one file, which is what a
// cache is.
func TestLayout_ListsTheAssembledContextCache(t *testing.T) {
	want := filepath.Join(AppDirName, CacheDir, ContextCacheDir)
	for _, e := range Layout() {
		if e.Rel != want {
			continue
		}
		if e.Tier != TierDerived {
			t.Errorf("%q has tier %s, want %s: a run reassembles it from the fragment set", want, e.Tier, TierDerived)
		}
		if e.Rebuild == "" {
			t.Errorf("%q names no Rebuild command", want)
		}
		return
	}
	t.Errorf("Layout() does not list %q at all, though agent.WriteContextFile writes there on every launch", want)
}

// TestLayout_StateDirIsTierLocal pins the one entry this whole task's third
// tier exists for.
func TestLayout_StateDirIsTierLocal(t *testing.T) {
	want := filepath.Join(AppDirName, StateDir)
	for _, e := range Layout() {
		if e.Rel == want {
			if e.Tier != TierLocal {
				t.Errorf("%q has tier %s, want %s", want, e.Tier, TierLocal)
			}
			return
		}
	}
	t.Errorf("Layout() does not list %q at all", want)
}
