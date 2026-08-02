package profiles

import (
	"errors"
	"testing"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/errs"
)

// TestProfileStoreContract runs one CRUD scenario against every Store adapter,
// so the in-memory MemStore and the filesystem *Loader are verified to behave
// identically through the ADR-0026 port.
func TestProfileStoreContract(t *testing.T) {
	adapters := []struct {
		name string
		make func() Store
	}{
		{"MemStore", func() Store { return NewMemStore() }},
		{"Loader", func() Store { return NewLoader([]string{"/profiles"}, WithFS(afero.NewMemMapFs())) }},
	}

	for _, a := range adapters {
		t.Run(a.name, func(t *testing.T) {
			s := a.make()

			if s.Exists("alpha") {
				t.Fatal("empty store reports alpha exists")
			}
			if _, err := s.Load("alpha"); err == nil {
				t.Fatal("Load of a missing profile should error")
			}

			if err := s.Save(&Profile{Name: "alpha", Bundles: []string{"go-development"}}); err != nil {
				t.Fatalf("Save alpha: %v", err)
			}
			if err := s.Save(&Profile{Name: "beta", Bundles: []string{"go-development"}}); err != nil {
				t.Fatalf("Save beta: %v", err)
			}

			if !s.Exists("alpha") {
				t.Fatal("alpha should exist after Save")
			}
			got, err := s.Load("alpha")
			if err != nil || got == nil || got.Name != "alpha" {
				t.Fatalf("Load alpha = %+v, %v", got, err)
			}
			list, err := s.List()
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(list) != 2 {
				t.Fatalf("List returned %d profiles, want 2", len(list))
			}

			if err := s.Delete("alpha"); err != nil {
				t.Fatalf("Delete alpha: %v", err)
			}
			if s.Exists("alpha") {
				t.Fatal("alpha should be gone after Delete")
			}
		})
	}
}

// storeAdapters is the adapter table the port-parity tests below run against.
// Both Store implementations answer the same questions the same way, or the
// port is a lie: a caller written against profiles.Store must not have to know
// which adapter it holds.
func storeAdapters() []struct {
	name string
	make func() Store
} {
	return []struct {
		name string
		make func() Store
	}{
		{"MemStore", func() Store { return NewMemStore() }},
		{"Loader", func() Store { return NewLoader([]string{"/profiles"}, WithFS(afero.NewMemMapFs())) }},
	}
}

// TestProfileStoreParity_MissingProfileIsSentinel pins that every Store adapter
// reports an absent profile with errs.ErrProfileNotFound, so
// errors.Is(err, errs.ErrProfileNotFound) — the check callers actually use to
// tell "absent" from "broken" — answers correctly whichever adapter is
// installed. MemStore used to return a bare fmt.Errorf, so the sentinel check
// silently reported false for a genuinely missing profile.
func TestProfileStoreParity_MissingProfileIsSentinel(t *testing.T) {
	for _, a := range storeAdapters() {
		t.Run(a.name, func(t *testing.T) {
			s := a.make()

			_, err := s.Load("absent")
			if !errors.Is(err, errs.ErrProfileNotFound) {
				t.Fatalf("Load of a missing profile: err = %v, want errors.Is ErrProfileNotFound", err)
			}
			if err := s.Delete("absent"); !errors.Is(err, errs.ErrProfileNotFound) {
				t.Fatalf("Delete of a missing profile: err = %v, want errors.Is ErrProfileNotFound", err)
			}
		})
	}
}

// TestProfileStoreParity_RejectsUnsafeNames pins that every Store adapter agrees
// on what a valid profile name is. The two adapters used to disagree: the
// filesystem *Loader rejects '#' (reserved for bundle refs) and traversal names
// via validateProfileName, while MemStore accepted anything non-empty — so a
// name that a MemStore-backed test proved acceptable would be refused the moment
// the same code ran against the real store.
func TestProfileStoreParity_RejectsUnsafeNames(t *testing.T) {
	unsafe := []string{
		"a#profiles/b",
		"../escape",
		"/absolute",
		"",
	}
	for _, a := range storeAdapters() {
		t.Run(a.name, func(t *testing.T) {
			for _, name := range unsafe {
				s := a.make()
				if err := s.Save(&Profile{Name: name, Bundles: []string{"go-development"}}); err == nil {
					t.Fatalf("Save(%q) succeeded; want rejection", name)
				}
				if s.Exists(name) {
					t.Fatalf("Exists(%q) is true after a rejected Save", name)
				}
			}
		})
	}
}
