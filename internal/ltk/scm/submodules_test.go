package scm

import (
	"reflect"
	"testing"

	"github.com/spf13/afero"
)

const gitmodules = `[submodule "libs/foo"]
	path = libs/foo
	url = https://example.com/foo.git
[submodule "vendor/bar"]
	path = vendor/bar
	branch = main
`

// SubmodulePaths reads the path keys, ignoring url/branch and other noise.
func TestSubmodulePaths(t *testing.T) {
	fs := afero.NewMemMapFs()
	if err := afero.WriteFile(fs, "/repo/.gitmodules", []byte(gitmodules), 0o644); err != nil {
		t.Fatal(err)
	}

	got := SubmodulePaths(fs, "/repo")
	want := []string{"libs/foo", "vendor/bar"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SubmodulePaths = %v, want %v", got, want)
	}
}

// The lookup walks up from a subdirectory to the repo root's .gitmodules.
func TestSubmodulePathsWalksUp(t *testing.T) {
	fs := afero.NewMemMapFs()
	if err := afero.WriteFile(fs, "/repo/.gitmodules", []byte("[submodule \"x\"]\n\tpath = x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := SubmodulePaths(fs, "/repo/a/b/c"); !reflect.DeepEqual(got, []string{"x"}) {
		t.Fatalf("walk-up = %v, want [x]", got)
	}
}

// The walk must not leave the repository: an inner repo nested under a parent
// that has a .gitmodules must NOT inherit the parent's submodule paths (they
// are relative to the parent and expand into spurious denials inside the inner
// repo). The first .git — file (gitfile pointer) or directory — is the boundary.
func TestSubmodulePathsStopAtRepositoryRoot(t *testing.T) {
	fs := afero.NewMemMapFs()
	if err := afero.WriteFile(fs, "/outer/.gitmodules", []byte("[submodule \"x\"]\n\tpath = x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := fs.MkdirAll("/outer/.git", 0o755); err != nil {
		t.Fatal(err)
	}

	t.Run("inner repo with a .git directory", func(t *testing.T) {
		if err := fs.MkdirAll("/outer/inner/.git", 0o755); err != nil {
			t.Fatal(err)
		}
		if got := SubmodulePaths(fs, "/outer/inner/src"); got != nil {
			t.Fatalf("parent repo's submodules leaked into the inner repo: %v", got)
		}
	})

	t.Run("inner repo with a gitfile-style .git file", func(t *testing.T) {
		if err := afero.WriteFile(fs, "/outer/gitfile/.git", []byte("gitdir: /outer/.git/modules/gitfile\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := SubmodulePaths(fs, "/outer/gitfile"); got != nil {
			t.Fatalf("parent repo's submodules leaked past a gitfile boundary: %v", got)
		}
	})

	// The repo's own root keeps working: .gitmodules is read before the .git
	// check stops the walk.
	t.Run("the repository's own .gitmodules still resolves", func(t *testing.T) {
		if got := SubmodulePaths(fs, "/outer/a/b"); !reflect.DeepEqual(got, []string{"x"}) {
			t.Fatalf("repo root .gitmodules = %v, want [x]", got)
		}
	})
}

// No .gitmodules anywhere → nil (the rule then matches nothing).
func TestSubmodulePathsNone(t *testing.T) {
	fs := afero.NewMemMapFs()
	if got := SubmodulePaths(fs, "/repo"); got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}
