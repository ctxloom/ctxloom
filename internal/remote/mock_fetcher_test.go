package remote

import (
	"context"
	"errors"
	"testing"
)

func TestNewMockFetcher(t *testing.T) {
	mock := NewMockFetcher()

	if mock.Files == nil {
		t.Error("Files map should be initialized")
	}
	if mock.Dirs == nil {
		t.Error("Dirs map should be initialized")
	}
	if mock.Refs == nil {
		t.Error("Refs map should be initialized")
	}
	if mock.ValidRepos == nil {
		t.Error("ValidRepos map should be initialized")
	}
	if mock.DefaultBranch != "main" {
		t.Errorf("DefaultBranch = %q, want %q", mock.DefaultBranch, "main")
	}
	if mock.ForgeType != ForgeGitHub {
		t.Errorf("ForgeType = %v, want %v", mock.ForgeType, ForgeGitHub)
	}
}

func TestMockFetcher_WithFile(t *testing.T) {
	mock := NewMockFetcher().WithFile("test/file.yaml", []byte("content"))

	if content, ok := mock.Files["test/file.yaml"]; !ok {
		t.Error("file should be in map")
	} else if string(content) != "content" {
		t.Errorf("content = %q, want %q", content, "content")
	}
}

func TestMockFetcher_WithDir(t *testing.T) {
	entries := []DirEntry{{Name: "file.yaml", IsDir: false}}
	mock := NewMockFetcher().WithDir("test/dir", entries)

	if dirs, ok := mock.Dirs["test/dir"]; !ok {
		t.Error("dir should be in map")
	} else if len(dirs) != 1 {
		t.Errorf("len(dirs) = %d, want 1", len(dirs))
	}
}

func TestMockFetcher_WithRef(t *testing.T) {
	mock := NewMockFetcher().WithRef("v1.0.0", "abc123def456")

	if sha, ok := mock.Refs["v1.0.0"]; !ok {
		t.Error("ref should be in map")
	} else if sha != "abc123def456" {
		t.Errorf("sha = %q, want %q", sha, "abc123def456")
	}
}

func TestMockFetcher_WithRepos(t *testing.T) {
	repos := []RepoInfo{{Name: "test-repo", Owner: "alice"}}
	mock := NewMockFetcher().WithRepos(repos)

	if len(mock.Repos) != 1 {
		t.Errorf("len(Repos) = %d, want 1", len(mock.Repos))
	}
}

func TestMockFetcher_WithValidRepo(t *testing.T) {
	mock := NewMockFetcher().WithValidRepo("alice", "ctxloom")

	if !mock.ValidRepos["alice/ctxloom"] {
		t.Error("repo should be valid")
	}
}

func TestMockFetcher_WithForge(t *testing.T) {
	mock := NewMockFetcher().WithForge(ForgeGitLab)

	if mock.ForgeType != ForgeGitLab {
		t.Errorf("ForgeType = %v, want %v", mock.ForgeType, ForgeGitLab)
	}
}

func TestMockFetcher_FetchFile(t *testing.T) {
	ctx := context.Background()

	t.Run("file exists", func(t *testing.T) {
		mock := NewMockFetcher().WithFile("test.yaml", []byte("test content"))
		content, err := mock.FetchFile(ctx, "owner", "repo", "test.yaml", "main")

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(content) != "test content" {
			t.Errorf("content = %q, want %q", content, "test content")
		}
		if len(mock.FetchFileCalls) != 1 {
			t.Errorf("len(FetchFileCalls) = %d, want 1", len(mock.FetchFileCalls))
		}
	})

	t.Run("file not found", func(t *testing.T) {
		mock := NewMockFetcher()
		_, err := mock.FetchFile(ctx, "owner", "repo", "missing.yaml", "main")

		if err == nil {
			t.Error("expected error for missing file")
		}
	})

	t.Run("error injection", func(t *testing.T) {
		mock := NewMockFetcher()
		mock.FetchFileErr = errors.New("injected error")

		_, err := mock.FetchFile(ctx, "owner", "repo", "test.yaml", "main")
		if err == nil || err.Error() != "injected error" {
			t.Error("expected injected error")
		}
	})
}

func TestMockFetcher_ListDir_Unit(t *testing.T) {
	ctx := context.Background()

	t.Run("dir exists", func(t *testing.T) {
		entries := []DirEntry{{Name: "file.yaml", IsDir: false}}
		mock := NewMockFetcher().WithDir("bundles", entries)

		result, err := mock.ListDir(ctx, "owner", "repo", "bundles", "main")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 1 {
			t.Errorf("len(result) = %d, want 1", len(result))
		}
		if len(mock.ListDirCalls) != 1 {
			t.Errorf("len(ListDirCalls) = %d, want 1", len(mock.ListDirCalls))
		}
	})

	t.Run("dir not found", func(t *testing.T) {
		mock := NewMockFetcher()
		_, err := mock.ListDir(ctx, "owner", "repo", "missing", "main")

		if err == nil {
			t.Error("expected error for missing dir")
		}
	})

	t.Run("error injection", func(t *testing.T) {
		mock := NewMockFetcher()
		mock.ListDirErr = errors.New("injected error")

		_, err := mock.ListDir(ctx, "owner", "repo", "bundles", "main")
		if err == nil || err.Error() != "injected error" {
			t.Error("expected injected error")
		}
	})
}

func TestMockFetcher_ResolveRef(t *testing.T) {
	ctx := context.Background()

	t.Run("ref exists", func(t *testing.T) {
		mock := NewMockFetcher().WithRef("v1.0.0", "abc123")
		sha, err := mock.ResolveRef(ctx, "owner", "repo", "v1.0.0")

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if sha != "abc123" {
			t.Errorf("sha = %q, want %q", sha, "abc123")
		}
		if len(mock.ResolveRefCalls) != 1 {
			t.Errorf("len(ResolveRefCalls) = %d, want 1", len(mock.ResolveRefCalls))
		}
	})

	t.Run("ref not in map uses default", func(t *testing.T) {
		mock := NewMockFetcher()
		sha, err := mock.ResolveRef(ctx, "owner", "repo", "main")

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if sha != "main000000" {
			t.Errorf("sha = %q, want %q", sha, "main000000")
		}
	})

	t.Run("error injection", func(t *testing.T) {
		mock := NewMockFetcher()
		mock.ResolveRefErr = errors.New("injected error")

		_, err := mock.ResolveRef(ctx, "owner", "repo", "v1.0.0")
		if err == nil || err.Error() != "injected error" {
			t.Error("expected injected error")
		}
	})
}

func TestMockFetcher_SearchRepos_Unit(t *testing.T) {
	ctx := context.Background()

	t.Run("returns repos", func(t *testing.T) {
		repos := []RepoInfo{
			{Name: "repo1", Owner: "alice"},
			{Name: "repo2", Owner: "bob"},
		}
		mock := NewMockFetcher().WithRepos(repos)

		result, err := mock.SearchRepos(ctx, "test", 10)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 2 {
			t.Errorf("len(result) = %d, want 2", len(result))
		}
		if len(mock.SearchReposCalls) != 1 {
			t.Errorf("len(SearchReposCalls) = %d, want 1", len(mock.SearchReposCalls))
		}
	})

	t.Run("respects limit", func(t *testing.T) {
		repos := []RepoInfo{
			{Name: "repo1"}, {Name: "repo2"}, {Name: "repo3"},
		}
		mock := NewMockFetcher().WithRepos(repos)

		result, err := mock.SearchRepos(ctx, "test", 2)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 2 {
			t.Errorf("len(result) = %d, want 2", len(result))
		}
	})

	t.Run("error injection", func(t *testing.T) {
		mock := NewMockFetcher()
		mock.SearchReposErr = errors.New("injected error")

		_, err := mock.SearchRepos(ctx, "test", 10)
		if err == nil || err.Error() != "injected error" {
			t.Error("expected injected error")
		}
	})
}

func TestMockFetcher_ValidateRepo(t *testing.T) {
	ctx := context.Background()

	t.Run("explicit valid", func(t *testing.T) {
		mock := NewMockFetcher().WithValidRepo("alice", "ctxloom")
		valid, err := mock.ValidateRepo(ctx, "alice", "ctxloom")

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !valid {
			t.Error("expected repo to be valid")
		}
		if len(mock.ValidateCalls) != 1 {
			t.Errorf("len(ValidateCalls) = %d, want 1", len(mock.ValidateCalls))
		}
	})

	t.Run("default is valid", func(t *testing.T) {
		mock := NewMockFetcher()
		valid, err := mock.ValidateRepo(ctx, "unknown", "repo")

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !valid {
			t.Error("expected default to be valid")
		}
	})

	t.Run("explicit invalid", func(t *testing.T) {
		mock := NewMockFetcher()
		mock.ValidRepos["bad/repo"] = false

		valid, err := mock.ValidateRepo(ctx, "bad", "repo")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if valid {
			t.Error("expected repo to be invalid")
		}
	})

	t.Run("error injection", func(t *testing.T) {
		mock := NewMockFetcher()
		mock.ValidateErr = errors.New("injected error")

		_, err := mock.ValidateRepo(ctx, "alice", "ctxloom")
		if err == nil || err.Error() != "injected error" {
			t.Error("expected injected error")
		}
	})
}

func TestMockFetcher_GetDefaultBranch(t *testing.T) {
	ctx := context.Background()

	t.Run("returns default", func(t *testing.T) {
		mock := NewMockFetcher()
		branch, err := mock.GetDefaultBranch(ctx, "owner", "repo")

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if branch != "main" {
			t.Errorf("branch = %q, want %q", branch, "main")
		}
	})

	t.Run("custom default", func(t *testing.T) {
		mock := NewMockFetcher()
		mock.DefaultBranch = "master"

		branch, err := mock.GetDefaultBranch(ctx, "owner", "repo")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if branch != "master" {
			t.Errorf("branch = %q, want %q", branch, "master")
		}
	})

	t.Run("error injection", func(t *testing.T) {
		mock := NewMockFetcher()
		mock.DefaultBrErr = errors.New("injected error")

		_, err := mock.GetDefaultBranch(ctx, "owner", "repo")
		if err == nil || err.Error() != "injected error" {
			t.Error("expected injected error")
		}
	})
}

func TestMockFetcher_Forge(t *testing.T) {
	mock := NewMockFetcher()
	if mock.Forge() != ForgeGitHub {
		t.Errorf("Forge() = %v, want %v", mock.Forge(), ForgeGitHub)
	}

	mock.WithForge(ForgeGitLab)
	if mock.Forge() != ForgeGitLab {
		t.Errorf("Forge() = %v, want %v", mock.Forge(), ForgeGitLab)
	}
}
