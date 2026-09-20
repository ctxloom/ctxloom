package paths

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

const testHarp = "ugly-icy-squid"

// TestHarpSessionHome_IsTheHomeMemberOfTheSessionDir pins the instance root
// as a CHILD of the session dir rather than anything in the project tree:
// everything one session's engines need sits under one directory, so reaping
// the session is one RemoveAll and cannot half-delete an instance.
func TestHarpSessionHome_IsTheHomeMemberOfTheSessionDir(t *testing.T) {
	testsupport.Isolate(t)
	dir, err := HarpDir(testHarp)
	if err != nil {
		t.Fatalf("HarpDir() error = %v", err)
	}
	home, err := HarpSessionHome(testHarp)
	if err != nil {
		t.Fatalf("HarpSessionHome() error = %v", err)
	}
	if want := filepath.Join(dir, SessionHomeDirName); home != want {
		t.Errorf("HarpSessionHome() = %q, want %q", home, want)
	}
	if !strings.HasSuffix(home, filepath.Join(AppDirName, SessionsDir, testHarp, "home")) {
		t.Errorf("HarpSessionHome() = %q, want the literal shape ~/.ctxloom/sessions/<harp>/home", home)
	}
}

// TestHarpSessionHome_IsKeyedByHarp is the per-session property stated as a
// path fact: two sessions must not resolve to one directory. A home keyed by
// the project instead of the harp is exactly the durable per-project home
// this model retired, and it would hand session B whatever session A's agent
// wrote.
func TestHarpSessionHome_IsKeyedByHarp(t *testing.T) {
	testsupport.Isolate(t)
	a, err := HarpSessionHome("ugly-icy-squid")
	if err != nil {
		t.Fatalf("HarpSessionHome(A) error = %v", err)
	}
	b, err := HarpSessionHome("brave-warm-otter")
	if err != nil {
		t.Fatalf("HarpSessionHome(B) error = %v", err)
	}
	if a == b {
		t.Errorf("two sessions resolve to ONE instance home (%q); the instance must be keyed by harp", a)
	}
}

// TestHarpSessionHome_RejectsTraversalAndEmpty is HarpDir's validation reason:
// a harp is a user-renameable string (`ctxloom session edit <old> --name
// ../..`) that becomes a single path COMPONENT. Escaping it would let a
// rename reach MkdirAll outside the sessions root; an empty harp has no
// instance at all — there is deliberately no session-less fallback, since a
// shared path would recreate the retired durable home.
func TestHarpSessionHome_RejectsTraversalAndEmpty(t *testing.T) {
	testsupport.Isolate(t)
	for _, harp := range []string{"", "../..", "a/b", "..", ".", "/abs"} {
		t.Run(harp, func(t *testing.T) {
			got, err := HarpSessionHome(harp)
			if err == nil {
				t.Errorf("HarpSessionHome(%q) = %q, want a validation error", harp, got)
			}
			if got != "" {
				t.Errorf("HarpSessionHome(%q) returned %q alongside its error; a rejected harp must name no path at all", harp, got)
			}
		})
	}
}

// TestLayout_HasNoHarpKeyedRows pins the deliberate ABSENCE of a Layout row
// for any per-session path. Layout() enumerates paths whose absence doctor
// REPORTS (doctorCheckLocalTierState); a session's members live under the
// home-rooted sessions store row and are created at instance time, so a row
// would report a loss that is not one, and could not name a harp that does
// not exist yet. state/ holds only its fixed project-local residents.
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
