package remote

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestInferSelectorKind(t *testing.T) {
	cases := []struct {
		expr string
		want SelectorKind
	}{
		// Empty → track the default branch.
		{"", SelectorBranch},

		// Semver versions and ranges → matched against tags.
		{"1.2.3", SelectorVersion},
		{"v1.2.3", SelectorVersion},
		{"^1.2", SelectorVersion},
		{"~1.2.0", SelectorVersion},
		{">=1.2.0, <2.0.0", SelectorVersion},
		{"1.2.x", SelectorVersion},
		{"v1", SelectorVersion},
		{"v1.2", SelectorVersion},

		// Bare object names (SHAs) → sha, even when all-digit. This is the guard:
		// a 7+ hex string must NOT be read as a semver version.
		{"1234567", SelectorSHA},
		{"abc1234", SelectorSHA},
		{"deadbeef", SelectorSHA},
		{"0123456789abcdef0123456789abcdef01234567", SelectorSHA},

		// Bare names of no inferable shape → ambiguous ("") — tag vs branch is
		// decided against the repo at resolve time (tag wins).
		{"main", ""},
		{"release-1.x", ""},
		{"feature/foo", ""},
		{"nightly", ""},
	}
	for _, c := range cases {
		if got := inferSelectorKind(c.expr); got != c.want {
			t.Errorf("inferSelectorKind(%q) = %q, want %q", c.expr, got, c.want)
		}
	}
}

func TestSelectorKind_IsPin(t *testing.T) {
	pins := []SelectorKind{SelectorSHA, SelectorTag}
	moving := []SelectorKind{SelectorVersion, SelectorBranch, ""}
	for _, k := range pins {
		if !k.IsPin() {
			t.Errorf("%q.IsPin() = false, want true (a pin never goes outdated)", k)
		}
	}
	for _, k := range moving {
		if k.IsPin() {
			t.Errorf("%q.IsPin() = true, want false (re-resolves)", k)
		}
	}
}

func TestLockEntry_SelectorKind_DerivesForLegacyEntries(t *testing.T) {
	cases := []struct {
		e    LockEntry
		want SelectorKind
	}{
		{LockEntry{Kind: SelectorTag}, SelectorTag},            // explicit wins
		{LockEntry{Version: "v1.2.0"}, SelectorVersion},        // a recorded tag ⇒ version
		{LockEntry{RequestedVersion: "^1.2"}, SelectorVersion}, // semver shape
		{LockEntry{RequestedVersion: "abc1234"}, SelectorSHA},  // hex shape
		{LockEntry{RequestedVersion: ""}, SelectorBranch},      // empty ⇒ default branch
		{LockEntry{RequestedVersion: "main"}, SelectorBranch},  // bare name derives to branch
	}
	for _, c := range cases {
		if got := c.e.SelectorKind(); got != c.want {
			t.Errorf("SelectorKind(%+v) = %q, want %q", c.e, got, c.want)
		}
	}
}

func TestParseSelector(t *testing.T) {
	cases := []struct {
		expr     string
		wantKind SelectorKind
		wantBare string
	}{
		{"sha:abc1234", SelectorSHA, "abc1234"},
		{"tag:main", SelectorTag, "main"},
		{"version:^1", SelectorVersion, "^1"},
		{"branch:1.0", SelectorBranch, "1.0"}, // forces branch over the version shape
		{"^1.2", "", "^1.2"},                  // no prefix → inference
		{"main", "", "main"},
	}
	for _, c := range cases {
		k, b := parseSelector(c.expr)
		if k != c.wantKind || b != c.wantBare {
			t.Errorf("parseSelector(%q) = (%q, %q), want (%q, %q)", c.expr, k, b, c.wantKind, c.wantBare)
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
	if got.Kind != SelectorBranch {
		t.Errorf("Kind = %q, want %q (empty ⇒ default branch)", got.Kind, SelectorBranch)
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

func TestResolveConstraint_BareNameBranch(t *testing.T) {
	// A bare name that is NOT one of the repo's tags is tracked as a branch.
	rv := &fakeRepoVersions{tags: []string{"v1.0.0"}}
	got, err := ResolveConstraint(context.Background(), "release-1.x", rv)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.SHA != "sha-release-1.x" || got.Version != "" || got.Kind != SelectorBranch {
		t.Errorf("got {SHA:%q Version:%q Kind:%q}, want a branch resolution", got.SHA, got.Version, got.Kind)
	}
	if len(rv.resolvedRefs) != 1 || rv.resolvedRefs[0] != "release-1.x" {
		t.Errorf("resolved refs = %v, want [release-1.x]", rv.resolvedRefs)
	}
}

func TestResolveConstraint_BareNameTagWins(t *testing.T) {
	// A bare name that IS one of the repo's tags is pinned as a tag (tag wins).
	rv := &fakeRepoVersions{tags: []string{"stable", "v1.0.0"}}
	got, err := ResolveConstraint(context.Background(), "stable", rv)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Kind != SelectorTag || got.Version != "stable" || got.SHA != "sha-stable" {
		t.Errorf("got {SHA:%q Version:%q Kind:%q}, want a tag pin of stable", got.SHA, got.Version, got.Kind)
	}
}

func TestResolveConstraint_ExplicitPrefixesOverrideInference(t *testing.T) {
	// tag:main pins the tag even though a same-named branch exists; branch:1.0
	// tracks a branch named "1.0" that would otherwise infer as a version.
	rv := &fakeRepoVersions{tags: []string{"main"}}
	got, err := ResolveConstraint(context.Background(), "tag:main", rv)
	if err != nil {
		t.Fatalf("tag:main: %v", err)
	}
	if got.Kind != SelectorTag || got.Version != "main" {
		t.Errorf("tag:main → {Version:%q Kind:%q}, want a tag pin", got.Version, got.Kind)
	}

	rv2 := &fakeRepoVersions{tags: []string{"v1.0.0"}}
	got2, err := ResolveConstraint(context.Background(), "branch:1.0", rv2)
	if err != nil {
		t.Fatalf("branch:1.0: %v", err)
	}
	if got2.Kind != SelectorBranch || got2.SHA != "sha-1.0" {
		t.Errorf("branch:1.0 → {SHA:%q Kind:%q}, want a branch track of 1.0", got2.SHA, got2.Kind)
	}
}

func TestResolveConstraint_SHA(t *testing.T) {
	rv := &fakeRepoVersions{resolve: map[string]string{"abc1234": "abc1234full"}}
	got, err := ResolveConstraint(context.Background(), "abc1234", rv)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.SHA != "abc1234full" || got.Kind != SelectorSHA {
		t.Errorf("got {SHA:%q Kind:%q}, want abc1234full/sha", got.SHA, got.Kind)
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

// TestResolveConstraint_ForcedVersionRejectsUnparseableExpr pins that
// resolveSemver's constraint-parse failure is NOT purely
// defensive. On the INFERRED path inferSelectorKind already proved the
// expression parses as semver, so the branch cannot fire — but the FORCED
// "version:<expr>" path skips inference entirely, so an unparseable
// expression reaches semver.NewConstraint with nothing having validated it.
// That must be a returned error naming the offending expression, never a
// panic and never a silent fall-through to some other selector kind.
func TestResolveConstraint_ForcedVersionRejectsUnparseableExpr(t *testing.T) {
	for _, expr := range []string{"version:not-a-version", "version:^^1", "version:main"} {
		t.Run(expr, func(t *testing.T) {
			rv := &fakeRepoVersions{tags: []string{"v1.0.0", "v1.2.0"}}
			got, err := ResolveConstraint(context.Background(), expr, rv)
			if err == nil {
				t.Fatalf("ResolveConstraint(%q) = %+v, want an error: the forced version: path has no prior validation", expr, got)
			}
			if !strings.Contains(err.Error(), "invalid version constraint") {
				t.Errorf("error = %q, want it to name the invalid constraint", err)
			}
			if len(rv.resolvedRefs) != 0 {
				t.Errorf("resolved refs = %v, want none: an unparseable constraint must not reach the repo", rv.resolvedRefs)
			}
		})
	}
}

// TestResolveConstraint_NameTagFirst_ListTagsErrorPropagates pins that a
// bare, prefix-less name of no inferable shape (e.g. "main") is classified by
// asking the repo whether it names a tag (resolveNameTagFirst → isRepoTag). If
// that ListTags call fails transiently, the failure must be reported, NOT
// silently read as "not a tag" — which used to fall through to resolveBranch
// and quietly convert what may have been an intended tag PIN into branch
// TRACKING, persisted to lock.yaml as such.
func TestResolveConstraint_NameTagFirst_ListTagsErrorPropagates(t *testing.T) {
	sentinel := errors.New("listing tags: connection reset")
	rv := &fakeRepoVersions{tagsErr: sentinel}
	got, err := ResolveConstraint(context.Background(), "main", rv)
	if err == nil {
		t.Fatalf("ResolveConstraint(%q) = %+v, want an error: a failed tag listing must not silently become branch tracking", "main", got)
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("error = %v, want wrapped %v", err, sentinel)
	}
	if len(rv.resolvedRefs) != 0 {
		t.Errorf("resolved refs = %v, want none: must not fall through to resolveBranch after a listing failure", rv.resolvedRefs)
	}
}
