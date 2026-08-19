package remote

import (
	"strings"
	"testing"
)

func TestResolveRef_CanonicalPassthrough(t *testing.T) {
	// A scheme-qualified ref is self-contained and ignores the source.
	got, err := ResolveRef("https://github.com/o/r@bundles/x", "https://other/repo", ItemTypeBundle)
	if err != nil {
		t.Fatalf("ResolveRef: %v", err)
	}
	if got.URL != "https://github.com/o/r" || got.Path != "x" || got.ItemType != ItemTypeBundle {
		t.Fatalf("canonical not preserved: %+v", got)
	}
}

func TestResolveRef_LocalPassthrough(t *testing.T) {
	got, err := ResolveRef("ctxloom:local@bundles/x", "https://other/repo", ItemTypeBundle)
	if err != nil {
		t.Fatalf("ResolveRef: %v", err)
	}
	if !got.IsLocal || got.Path != "x" {
		t.Fatalf("local ref not preserved: %+v", got)
	}
}

func TestResolveRef_ShortAgainstURL(t *testing.T) {
	got, err := ResolveRef("demo", "https://github.com/o/r", ItemTypeBundle)
	if err != nil {
		t.Fatalf("ResolveRef: %v", err)
	}
	if want := "ctxloom+git://github.com/o/r//bundles/demo"; got.CanonicalString() != want {
		t.Fatalf("CanonicalString = %q, want %q", got.CanonicalString(), want)
	}
}

func TestResolveRef_ShortWithVersion(t *testing.T) {
	got, err := ResolveRef("demo@v1", "https://github.com/o/r", ItemTypeBundle)
	if err != nil {
		t.Fatalf("ResolveRef: %v", err)
	}
	if got.Path != "demo" || got.ContentVersion != "v1" {
		t.Fatalf("path/version not parsed: path=%q version=%q", got.Path, got.ContentVersion)
	}
}

func TestResolveRef_ShortNestedPath(t *testing.T) {
	got, err := ResolveRef("lang/go", "file:///tmp/repo.git", ItemTypeBundle)
	if err != nil {
		t.Fatalf("ResolveRef: %v", err)
	}
	if got.Path != "lang/go" || got.URL != "file:///tmp/repo.git" {
		t.Fatalf("nested path not resolved: %+v", got)
	}
}

func TestResolveRef_ShortAgainstLocalSource(t *testing.T) {
	got, err := ResolveRef("demo", LocalSource, ItemTypeBundle)
	if err != nil {
		t.Fatalf("ResolveRef: %v", err)
	}
	if !got.IsLocal || got.Path != "demo" || got.ItemType != ItemTypeBundle {
		t.Fatalf("short ref against LocalSource not local: %+v", got)
	}
}

// TestResolveRef_MalformedCanonicalNotReclassified pins that ResolveRef
// used to reclassify ANY ParseReference failure as "this must be a short
// same-repo ref" and silently re-expand it against sourceURL — so a malformed
// scheme-qualified ref never surfaced its real error, it just produced a
// nonsense Reference instead.
func TestResolveRef_MalformedCanonicalNotReclassified(t *testing.T) {
	_, err := ResolveRef("https://github.com/o/r@fragments/x", "https://src", ItemTypeBundle)
	if err == nil {
		t.Fatal("expected the real parse error for a malformed canonical ref, got nil")
	}
	if !strings.Contains(err.Error(), "unknown item type") {
		t.Fatalf("expected ParseReference's real error to surface, got: %v", err)
	}
}

func TestResolveRef_ShortWithoutSource(t *testing.T) {
	if _, err := ResolveRef("demo", "", ItemTypeBundle); err == nil {
		t.Fatal("expected error resolving a short ref with no source")
	}
}

func TestResolveRefString(t *testing.T) {
	url := "https://github.com/o/r"
	cases := []struct {
		name, ref, source, hash string
		kind                    ItemType
		want                    string
	}{
		{"short bundle no hash", "demo", url, "", ItemTypeBundle, "https://github.com/o/r@bundles/demo"},
		{"short bundle inherits source hash", "demo", url, "abc123", ItemTypeBundle, "https://github.com/o/r@bundles/demo@abc123"},
		{"short with own version ignores source hash", "demo@v1", url, "abc123", ItemTypeBundle, "https://github.com/o/r@bundles/demo@v1"},
		{"short with item path inherits hash", "demo#fragments/x", url, "abc123", ItemTypeBundle, "https://github.com/o/r@bundles/demo@abc123#fragments/x"},
		{"canonical verbatim", "https://other/repo@bundles/y", url, "abc123", ItemTypeBundle, "https://other/repo@bundles/y"},
		{"local verbatim", "ctxloom:local@bundles/y", url, "abc123", ItemTypeBundle, "ctxloom:local@bundles/y"},
		{"no source falls back", "demo", "", "abc123", ItemTypeBundle, "demo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolveRefString(tc.ref, tc.source, tc.hash, tc.kind); got != tc.want {
				t.Fatalf("ResolveRefString(%q,%q,%q) = %q, want %q", tc.ref, tc.source, tc.hash, got, tc.want)
			}
		})
	}
}
