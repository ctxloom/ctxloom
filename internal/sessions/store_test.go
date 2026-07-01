package sessions

import (
	"path/filepath"
	"testing"
	"time"
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

			// First-bind-wins.
			if err := s.BindSession(e1.HarpName, "sid-1", "/t1"); err != nil {
				t.Fatalf("BindSession: %v", err)
			}
			if err := s.BindSession(e1.HarpName, "sid-2", "/t2"); err != nil {
				t.Fatalf("BindSession (second): %v", err)
			}
			if got, _ := s.Find(e1.HarpName); got.SessionID != "sid-1" {
				t.Fatalf("first-bind-wins violated: SessionID = %q", got.SessionID)
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
