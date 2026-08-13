package sessions

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestSessionStoreContract runs one lifecycle scenario against every Store
// adapter, so the in-memory MemStore and the filesystem *Manager are verified
// to behave identically through the ADR-0026 port (harp minting, first-bind-
// wins, project filtering, reconcile-by-predicate).
func TestSessionStoreContract(t *testing.T) {
	adapters := []struct {
		name string
		make func(t *testing.T) Store
	}{
		{"MemStore", func(t *testing.T) Store { return NewMemStore() }},
		{"Manager", func(t *testing.T) Store {
			m, err := Open(filepath.Join(t.TempDir(), "index.yaml"))
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			return m
		}},
	}

	for _, a := range adapters {
		t.Run(a.name, func(t *testing.T) {
			s := a.make(t)

			e1, err := s.AssignHarp("/proj", "claude")
			if err != nil {
				t.Fatalf("AssignHarp: %v", err)
			}
			e2, err := s.AssignHarp("/proj", "claude")
			if err != nil {
				t.Fatalf("AssignHarp: %v", err)
			}
			if e1.HarpName == e2.HarpName {
				t.Fatal("AssignHarp returned a duplicate harp")
			}

			// Find present / absent.
			if got, _ := s.Find(e1.HarpName); got == nil || got.HarpName != e1.HarpName {
				t.Fatalf("Find(%q) = %+v", e1.HarpName, got)
			}
			if got, _ := s.Find("absent"); got != nil {
				t.Fatal("Find of a missing harp should be nil")
			}

			// Rebind rule, identical for both adapters: a new id WITH a
			// transcript path is engine rotation and re-points; an id-only
			// bind never displaces a live binding.
			if err := s.BindSession(e1.HarpName, "sid-1", "/t1"); err != nil {
				t.Fatalf("BindSession: %v", err)
			}
			if err := s.BindSession(e1.HarpName, "sid-2", "/t2"); err != nil {
				t.Fatalf("BindSession (second): %v", err)
			}
			if got, _ := s.Find(e1.HarpName); got.SessionID != "sid-2" || got.TranscriptPath != "/t2" {
				t.Fatalf("rotation did not re-point: SessionID = %q, TranscriptPath = %q", got.SessionID, got.TranscriptPath)
			}
			if err := s.BindSession(e1.HarpName, "sid-3", ""); err != nil {
				t.Fatalf("BindSession (id-only): %v", err)
			}
			if got, _ := s.Find(e1.HarpName); got.SessionID != "sid-2" {
				t.Fatalf("id-only bind displaced a live binding: SessionID = %q", got.SessionID)
			}

			// Project filtering (membership, not order — timestamps may tie).
			list, err := s.ListForProject("/proj")
			if err != nil {
				t.Fatalf("ListForProject: %v", err)
			}
			if len(list) != 2 {
				t.Fatalf("ListForProject(/proj) returned %d, want 2", len(list))
			}
			if other, _ := s.ListForProject("/elsewhere"); len(other) != 0 {
				t.Fatalf("ListForProject(/elsewhere) returned %d, want 0", len(other))
			}

			// MarkEnded.
			if err := s.MarkEnded(e1.HarpName, time.Now()); err != nil {
				t.Fatalf("MarkEnded: %v", err)
			}
			if got, _ := s.Find(e1.HarpName); got.EndedAt == nil {
				t.Fatal("EndedAt not stamped")
			}

			// Rename: collision rejected, then success.
			if err := s.Rename(e2.HarpName, e1.HarpName); err == nil {
				t.Fatal("Rename onto an existing harp should fail")
			}
			if err := s.Rename(e2.HarpName, "renamed"); err != nil {
				t.Fatalf("Rename: %v", err)
			}
			if got, _ := s.Find("renamed"); got == nil {
				t.Fatal("renamed harp not found")
			}

			// Reconcile drops the predicate-dead entry.
			survivors, err := s.Reconcile(func(e Entry) bool { return e.HarpName == e1.HarpName })
			if err != nil {
				t.Fatalf("Reconcile: %v", err)
			}
			if len(survivors) != 1 || survivors[0].HarpName != "renamed" {
				t.Fatalf("Reconcile survivors = %+v, want [renamed]", survivors)
			}

			// Forget.
			if err := s.Forget("renamed"); err != nil {
				t.Fatalf("Forget: %v", err)
			}
			if got, _ := s.Find("renamed"); got != nil {
				t.Fatal("harp still present after Forget")
			}
		})
	}
}

// TestSessionStoreContract_FindPopulatesCanonicalTranscript pins the
// S4 fix: Find must enrich its returned copy with
// CanonicalTranscriptPath exactly like ListForProject already does
// (fillCanonicalTranscript), for BOTH Store adapters — every S4 consumer that
// resolves a single harp (compactor, sessionfeed, mcp memory tools) goes
// through Find, not ListForProject, so a harp with a captured transcript must
// be visible there too.
func TestSessionStoreContract_FindPopulatesCanonicalTranscript(t *testing.T) {
	adapters := []struct {
		name string
		make func(t *testing.T) Store
	}{
		{"MemStore", func(t *testing.T) Store { return NewMemStore() }},
		{"Manager", func(t *testing.T) Store {
			m, err := Open(filepath.Join(t.TempDir(), "index.yaml"))
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			return m
		}},
	}

	for _, a := range adapters {
		t.Run(a.name, func(t *testing.T) {
			testsupport.Isolate(t) // paths.HarpCanonicalTranscriptPath is HOME-rooted

			s := a.make(t)
			e, err := s.AssignHarp("/proj", "codex")
			if err != nil {
				t.Fatalf("AssignHarp: %v", err)
			}

			// No canonical transcript yet: Find must leave the field empty, not
			// synthesize a path that doesn't exist on disk.
			if got, _ := s.Find(e.HarpName); got.CanonicalTranscriptPath != "" {
				t.Fatalf("CanonicalTranscriptPath = %q before any transcript was written, want empty", got.CanonicalTranscriptPath)
			}

			canonicalPath, err := paths.HarpCanonicalTranscriptPath(e.HarpName)
			if err != nil {
				t.Fatalf("HarpCanonicalTranscriptPath: %v", err)
			}
			if err := os.MkdirAll(filepath.Dir(canonicalPath), 0o755); err != nil {
				t.Fatalf("MkdirAll: %v", err)
			}
			if err := os.WriteFile(canonicalPath, []byte(`{"v":1}`+"\n"), 0o644); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}

			got, err := s.Find(e.HarpName)
			if err != nil {
				t.Fatalf("Find: %v", err)
			}
			if got.CanonicalTranscriptPath != canonicalPath {
				t.Fatalf("CanonicalTranscriptPath = %q, want %q", got.CanonicalTranscriptPath, canonicalPath)
			}
		})
	}
}

// TestSessionStoreContract_RotationLineage holds BOTH adapters to the same
// rotation-history rule: a displacing rebind appends the old binding to
// Rotations, and FindBySessionID resolves a rotated-away id through it.
// MemStore is the fake every other package's tests run against, so a fake
// that silently drops lineage a real Manager preserves is exactly how that
// defect stays invisible everywhere else.
func TestSessionStoreContract_RotationLineage(t *testing.T) {
	adapters := []struct {
		name string
		make func(t *testing.T) Store
	}{
		{"MemStore", func(t *testing.T) Store { return NewMemStore() }},
		{"Manager", func(t *testing.T) Store {
			m, err := Open(filepath.Join(t.TempDir(), "index.yaml"))
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			return m
		}},
	}

	for _, a := range adapters {
		t.Run(a.name, func(t *testing.T) {
			s := a.make(t)
			e, err := s.AssignHarp("/proj", "claude-code")
			if err != nil {
				t.Fatalf("AssignHarp: %v", err)
			}

			if err := s.BindSession(e.HarpName, "pre-clear", "/pre.jsonl"); err != nil {
				t.Fatalf("BindSession: %v", err)
			}
			if err := s.BindSession(e.HarpName, "post-clear", "/post.jsonl"); err != nil {
				t.Fatalf("BindSession (rotation): %v", err)
			}

			got, err := s.Find(e.HarpName)
			if err != nil || got == nil {
				t.Fatalf("Find: %v, %v", got, err)
			}
			if len(got.Rotations) != 1 || got.Rotations[0].SessionID != "pre-clear" || got.Rotations[0].TranscriptPath != "/pre.jsonl" {
				t.Fatalf("%s: Rotations = %+v, want [{pre-clear /pre.jsonl}]", a.name, got.Rotations)
			}

			byOld, err := s.FindBySessionID("pre-clear")
			if err != nil {
				t.Fatalf("FindBySessionID(pre-clear): %v", err)
			}
			if byOld == nil || byOld.HarpName != e.HarpName {
				t.Fatalf("%s: FindBySessionID(pre-clear) = %+v, want harp %q", a.name, byOld, e.HarpName)
			}

			byCurrent, err := s.FindBySessionID("post-clear")
			if err != nil || byCurrent == nil || byCurrent.HarpName != e.HarpName {
				t.Fatalf("%s: FindBySessionID(post-clear) = %+v, %v", a.name, byCurrent, err)
			}

			byUnknown, err := s.FindBySessionID("never-bound")
			if err != nil || byUnknown != nil {
				t.Fatalf("%s: FindBySessionID(never-bound) = %+v, %v, want nil, nil", a.name, byUnknown, err)
			}
		})
	}
}

// TestSessionStoreContract_RenameRefusesUnsafeNames holds BOTH adapters to
// the same safe-rename rule. It lives in the contract suite deliberately: MemStore is
// the fake every other package's tests run against, so a fake that accepts a
// name the real Manager refuses is exactly how a validation defect stays
// invisible. Asserted on store state, not error text.
func TestSessionStoreContract_RenameRefusesUnsafeNames(t *testing.T) {
	adapters := []struct {
		name string
		make func(t *testing.T) Store
	}{
		{"MemStore", func(t *testing.T) Store { return NewMemStore() }},
		{"Manager", func(t *testing.T) Store {
			m, err := Open(filepath.Join(t.TempDir(), "index.yaml"))
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			return m
		}},
	}
	for _, a := range adapters {
		t.Run(a.name, func(t *testing.T) {
			for _, bad := range []string{"", "..", "../../etc/passwd", "a/b", `a\b`, " lead", "nul\x00name"} {
				s := a.make(t)
				e, err := s.AssignHarp("/proj", "claude")
				if err != nil {
					t.Fatalf("AssignHarp: %v", err)
				}
				if err := s.Rename(e.HarpName, bad); err == nil {
					t.Errorf("Rename to %q was accepted; want refusal", bad)
				}
				got, err := s.Find(e.HarpName)
				if err != nil || got == nil {
					t.Errorf("after refused rename to %q: Find(%q) = %v, %v; want the original entry", bad, e.HarpName, got, err)
				}
			}
		})
	}
}
