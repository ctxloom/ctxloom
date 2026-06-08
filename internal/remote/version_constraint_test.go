package remote

import (
	"context"
	"errors"
	"testing"
)

func TestClassifyConstraint(t *testing.T) {
	cases := []struct {
		expr string
		want ConstraintKind
	}{
		// Empty → track default branch.
		{"", ConstraintDefault},

		// Semver versions and ranges → matched against tags.
		{"1.2.3", ConstraintSemver},
		{"v1.2.3", ConstraintSemver},
		{"^1.2", ConstraintSemver},
		{"~1.2.0", ConstraintSemver},
		{">=1.2.0, <2.0.0", ConstraintSemver},
		{"1.2.x", ConstraintSemver},
		{"v1", ConstraintSemver},
		{"v1.2", ConstraintSemver},

		// Branches and non-semver tags → resolved verbatim.
		{"main", ConstraintDirect},
		{"release-1.x", ConstraintDirect},
		{"feature/foo", ConstraintDirect},
		{"nightly", ConstraintDirect},

		// Bare object names (SHAs) → direct, even when all-digit. This is the
		// guard: a 7+ hex string must NOT be read as a semver version.
		{"1234567", ConstraintDirect},
		{"abc1234", ConstraintDirect},
		{"deadbeef", ConstraintDirect},
		{"0123456789abcdef0123456789abcdef01234567", ConstraintDirect},
	}
	for _, c := range cases {
		if got := ClassifyConstraint(c.expr); got != c.want {
			t.Errorf("ClassifyConstraint(%q) = %v, want %v", c.expr, got, c.want)
		}
	}
}

// fakeRepoVersions is an in-memory RepoVersions for resolver tests. ResolveRef
// returns "sha-<ref>" unless overridden, and records every ref it was asked to
// resolve so tests can assert the resolver resolved the expected name verbatim.
type fakeRepoVersions struct {
	tags          []string
	defaultBranch string
	resolve       map[string]string // ref → SHA override
	tagsErr       error
	branchErr     error
	resolveErr    map[string]error // ref → error override

	resolvedRefs []string
}

func (f *fakeRepoVersions) ListTags(context.Context) ([]string, error) {
	if f.tagsErr != nil {
		return nil, f.tagsErr
	}
	return f.tags, nil
}

func (f *fakeRepoVersions) DefaultBranch(context.Context) (string, error) {
	if f.branchErr != nil {
		return "", f.branchErr
	}
	if f.defaultBranch == "" {
		return "main", nil
	}
	return f.defaultBranch, nil
}

func (f *fakeRepoVersions) ResolveRef(_ context.Context, ref string) (string, error) {
	f.resolvedRefs = append(f.resolvedRefs, ref)
	if err, ok := f.resolveErr[ref]; ok {
		return "", err
	}
	if sha, ok := f.resolve[ref]; ok {
		return sha, nil
	}
	return "sha-" + ref, nil
}

func TestResolveConstraint_DefaultBranch(t *testing.T) {
	rv := &fakeRepoVersions{defaultBranch: "trunk"}
	got, err := ResolveConstraint(context.Background(), "", rv)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.SHA != "sha-trunk" {
		t.Errorf("SHA = %q, want %q", got.SHA, "sha-trunk")
	}
	if got.Version != "" {
		t.Errorf("Version = %q, want empty for default-branch resolution", got.Version)
	}
	if want := []string{"trunk"}; len(rv.resolvedRefs) != 1 || rv.resolvedRefs[0] != want[0] {
		t.Errorf("resolved refs = %v, want %v", rv.resolvedRefs, want)
	}
}

func TestResolveConstraint_SemverRangePicksHighest(t *testing.T) {
	rv := &fakeRepoVersions{tags: []string{"v1.0.0", "v1.2.0", "v1.3.0", "v2.0.0"}}
	got, err := ResolveConstraint(context.Background(), "^1.2", rv)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Version != "v1.3.0" {
		t.Errorf("Version = %q, want v1.3.0 (highest in ^1.2, excluding v2.0.0)", got.Version)
	}
	if got.SHA != "sha-v1.3.0" {
		t.Errorf("SHA = %q, want sha-v1.3.0", got.SHA)
	}
}

func TestResolveConstraint_SemverExactTag(t *testing.T) {
	rv := &fakeRepoVersions{tags: []string{"v1.1.0", "v1.2.0", "v1.3.0"}}
	got, err := ResolveConstraint(context.Background(), "v1.2.0", rv)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Version != "v1.2.0" || got.SHA != "sha-v1.2.0" {
		t.Errorf("got {SHA:%q Version:%q}, want exact v1.2.0", got.SHA, got.Version)
	}
}

func TestResolveConstraint_SemverIgnoresNonSemverTags(t *testing.T) {
	rv := &fakeRepoVersions{tags: []string{"nightly", "latest", "v1.2.0", "release", "v1.3.0"}}
	got, err := ResolveConstraint(context.Background(), "^1", rv)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Version != "v1.3.0" {
		t.Errorf("Version = %q, want v1.3.0 (non-semver tags ignored)", got.Version)
	}
}

func TestResolveConstraint_SemverPrefersPrefixedOriginal(t *testing.T) {
	// Mixed 'v'-prefixed and bare tags: the chosen tag's authored form must be
	// the ref the resolver asks for, so the back-resolution hits a real tag.
	rv := &fakeRepoVersions{tags: []string{"1.2.0", "v1.3.0"}}
	got, err := ResolveConstraint(context.Background(), ">=1.0.0", rv)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Version != "v1.3.0" {
		t.Errorf("Version = %q, want v1.3.0", got.Version)
	}
	if len(rv.resolvedRefs) != 1 || rv.resolvedRefs[0] != "v1.3.0" {
		t.Errorf("resolved refs = %v, want [v1.3.0] (authored form)", rv.resolvedRefs)
	}
}

func TestResolveConstraint_SemverNoMatch(t *testing.T) {
	rv := &fakeRepoVersions{tags: []string{"v1.0.0", "v1.5.0"}}
	_, err := ResolveConstraint(context.Background(), "^2", rv)
	if err == nil {
		t.Fatal("expected error when no tag satisfies the constraint")
	}
}

func TestResolveConstraint_DirectBranch(t *testing.T) {
	rv := &fakeRepoVersions{tags: []string{"v1.0.0"}}
	got, err := ResolveConstraint(context.Background(), "release-1.x", rv)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.SHA != "sha-release-1.x" || got.Version != "" {
		t.Errorf("got {SHA:%q Version:%q}, want verbatim ref resolution", got.SHA, got.Version)
	}
	if len(rv.resolvedRefs) != 1 || rv.resolvedRefs[0] != "release-1.x" {
		t.Errorf("resolved refs = %v, want [release-1.x]", rv.resolvedRefs)
	}
}

func TestResolveConstraint_DirectSHA(t *testing.T) {
	rv := &fakeRepoVersions{resolve: map[string]string{"abc1234": "abc1234full"}}
	got, err := ResolveConstraint(context.Background(), "abc1234", rv)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.SHA != "abc1234full" {
		t.Errorf("SHA = %q, want abc1234full", got.SHA)
	}
}

func TestResolveConstraint_ListTagsError(t *testing.T) {
	sentinel := errors.New("boom")
	rv := &fakeRepoVersions{tagsErr: sentinel}
	_, err := ResolveConstraint(context.Background(), "^1.0", rv)
	if !errors.Is(err, sentinel) {
		t.Errorf("error = %v, want wrapped %v", err, sentinel)
	}
}

func TestResolveConstraint_ResolveRefError(t *testing.T) {
	sentinel := errors.New("no such ref")
	rv := &fakeRepoVersions{resolveErr: map[string]error{"main": sentinel}}
	_, err := ResolveConstraint(context.Background(), "", rv)
	if !errors.Is(err, sentinel) {
		t.Errorf("error = %v, want wrapped %v", err, sentinel)
	}
}
