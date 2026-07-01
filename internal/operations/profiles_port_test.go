package operations

import (
	"context"
	"testing"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/profiles"
)

// TestProfileOperations_StorageAgnostic_MemStore proves the ADR-0026 profiles
// port: the authoring operations depend on profiles.Store, so a filesystem-free
// in-memory adapter drives list/create/delete end to end. The profiles never
// touch disk — only the injected MemStore — confirming operations are
// storage-agnostic.
func TestProfileOperations_StorageAgnostic_MemStore(t *testing.T) {
	store := profiles.NewMemStore()
	store.Seed(&profiles.Profile{Name: "alpha"}, &profiles.Profile{Name: "beta"})

	cfg := &config.Config{AppPaths: []string{"/app"}}
	cfg.SetFS(afero.NewMemMapFs())

	// Read path lists straight from the injected store.
	list, err := ListProfiles(context.Background(), cfg, ListProfilesRequest{Loader: store})
	if err != nil {
		t.Fatalf("ListProfiles: %v", err)
	}
	if list.Count != 2 {
		t.Fatalf("ListProfiles count = %d, want 2", list.Count)
	}

	// Write path: a create lands in the injected store, not on disk.
	if _, err := CreateProfile(context.Background(), cfg, CreateProfileRequest{Name: "gamma", Loader: store}); err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}
	if !store.Exists("gamma") {
		t.Fatal("created profile did not land in the injected store")
	}

	// Delete path routes through the same port.
	if _, err := DeleteProfile(context.Background(), cfg, DeleteProfileRequest{Name: "alpha", Loader: store}); err != nil {
		t.Fatalf("DeleteProfile: %v", err)
	}
	if store.Exists("alpha") {
		t.Fatal("deleted profile still present in the store")
	}
}
