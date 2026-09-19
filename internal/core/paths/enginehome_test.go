package paths

import (
	"path/filepath"
	"strings"
	"testing"
)

const testHarp = "ugly-icy-squid"

// TestSessionStatePath_JoinsUnderTheStateTier pins the ONE location a session's
// project-side state lives: <appPath>/state/<harp>. It is under state/ and not
// cache/ because it holds a copied credential — gitignored AND unrebuildable —
// which is what makes the single .ctxloom/state/ ignore rule cover it.
func TestSessionStatePath_JoinsUnderTheStateTier(t *testing.T) {
	got, err := SessionStatePath("/proj/.ctxloom", testHarp)
	if err != nil {
		t.Fatalf("SessionStatePath() error = %v", err)
	}
	if want := filepath.Join(StatePath("/proj/.ctxloom"), testHarp); got != want {
		t.Errorf("SessionStatePath() = %q, want %q", got, want)
	}
}

// TestSessionHomePath_IsSessionStatePathPlusHome pins the instance root as a
// CHILD of the session's state root rather than a sibling: everything one
// session's engines need sits under one directory, so reaping the session is
// one RemoveAll and cannot half-delete an instance.
func TestSessionHomePath_IsSessionStatePathPlusHome(t *testing.T) {
	state, err := SessionStatePath("/proj/.ctxloom", testHarp)
	if err != nil {
		t.Fatalf("SessionStatePath() error = %v", err)
	}
	home, err := SessionHomePath("/proj/.ctxloom", testHarp)
	if err != nil {
		t.Fatalf("SessionHomePath() error = %v", err)
	}
	if want := filepath.Join(state, SessionHomeDirName); home != want {
		t.Errorf("SessionHomePath() = %q, want %q", home, want)
	}
	if want := filepath.Join("/proj/.ctxloom", "state", testHarp, "home"); home != want {
		t.Errorf("SessionHomePath() = %q, want the literal shape %q", home, want)
	}
}

// TestSessionPaths_AreKeyedByHarp is the per-session property stated as a path
// fact: two sessions in ONE project must not resolve to one directory. A home
// keyed by the project instead of the harp is exactly the durable per-project
// home this model retired, and it would hand session B whatever session A's
// agent wrote.
func TestSessionPaths_AreKeyedByHarp(t *testing.T) {
	a, err := SessionHomePath("/proj/.ctxloom", "ugly-icy-squid")
	if err != nil {
		t.Fatalf("SessionHomePath(A) error = %v", err)
	}
	b, err := SessionHomePath("/proj/.ctxloom", "brave-warm-otter")
	if err != nil {
		t.Fatalf("SessionHomePath(B) error = %v", err)
	}
	if a == b {
		t.Errorf("two sessions resolve to ONE instance home (%q); the instance must be keyed by harp", a)
	}
}

// TestSessionStatePath_RejectsTraversal is HarpDir's validation reason applied
// to the project side: a harp is a user-renameable string (`ctxloom session
// edit <old> --name ../..`) that becomes a single path COMPONENT, and this is
// the chokepoint every session-scoped project path is built from. Escaping it
// would let a rename reach MkdirAll outside the checkout's .ctxloom tree.
func TestSessionStatePath_RejectsTraversal(t *testing.T) {
	for _, harp := range []string{"../..", "a/b", "..", ".", "/abs"} {
		t.Run(harp, func(t *testing.T) {
			got, err := SessionStatePath("/proj/.ctxloom", harp)
			if err == nil {
				t.Errorf("SessionStatePath(%q) = %q, want a validation error", harp, got)
			}
			if got != "" {
				t.Errorf("SessionStatePath(%q) returned %q alongside its error; a rejected harp must name no path at all", harp, got)
			}
		})
	}
}

// TestSessionStatePath_RejectsEmptyHarp states the empty-harp policy where it
// is enforced: no session, no instance. There is deliberately no session-less
// fallback — a shared project path would recreate the retired durable home.
func TestSessionStatePath_RejectsEmptyHarp(t *testing.T) {
	if got, err := SessionStatePath("/proj/.ctxloom", ""); err == nil {
		t.Errorf("SessionStatePath(\"\") = %q, want an error — there is no session-less instance", got)
	}
	if got, err := SessionHomePath("/proj/.ctxloom", ""); err == nil {
		t.Errorf("SessionHomePath(\"\") = %q, want an error — there is no session-less instance", got)
	}
}

// TestLayout_HasNoHarpKeyedRows pins the deliberate ABSENCE of a Layout row for
// state/<harp>. Layout() enumerates paths whose absence doctor REPORTS
// (doctorCheckLocalTierState), and a per-session directory's absence is the
// normal case — it is created at instance time and reaped at session end — so a
// row would report a loss that is not one, and could not name a harp that does
// not exist yet.
//
// tests/arch's TestArch_LayoutHasNoHarpKeyedRows is the same claim under the
// arch gate; this copy rides the default suite, where a change to Layout() is
// actually made.
func TestLayout_HasNoHarpKeyedRows(t *testing.T) {
	sep := string(filepath.Separator)
	statePrefix := filepath.Join(AppDirName, StateDir) + sep

	for _, e := range Layout() {
		if strings.Contains(e.Rel, sep+SessionHomeDirName) {
			t.Errorf("Layout row %q names an engine config-home instance; instances are per-session and disposable, so they get no row", e.Rel)
		}
		if e.Rel == filepath.Join(AppDirName, StateDir, "engines") {
			t.Errorf("Layout row %q is the retired durable per-project engine home", e.Rel)
		}
		if !strings.HasPrefix(e.Rel, statePrefix) {
			continue
		}
		head := strings.SplitN(strings.TrimPrefix(e.Rel, statePrefix), sep, 2)[0]
		switch head {
		case TrustFileName, LocksDir:
		default:
			t.Errorf("Layout row %q sits under state/%s, which is neither a known fixed resident nor allowed to be a per-session key", e.Rel, head)
		}
	}
}
