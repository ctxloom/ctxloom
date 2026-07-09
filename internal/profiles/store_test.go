package profiles

import (
	"testing"

	"github.com/spf13/afero"
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

			if err := s.Save(&Profile{Name: "alpha"}); err != nil {
				t.Fatalf("Save alpha: %v", err)
			}
			if err := s.Save(&Profile{Name: "beta"}); err != nil {
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
