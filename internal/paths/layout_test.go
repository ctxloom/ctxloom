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
// resolve.
func TestLayout_RelPathsAreUnique(t *testing.T) {
	seen := map[string]Tier{}
	for _, e := range Layout() {
		if prior, ok := seen[e.Rel]; ok {
			t.Errorf("Layout() lists %q twice, as both %s and %s", e.Rel, prior, e.Tier)
		}
		seen[e.Rel] = e.Tier
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
