package content

import (
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/signing"
)

// TestSplitFrontMatter_DoesNotCorruptBodies pins the two corruptions this repo
// has actually been bitten by: a body containing its own "---" rule, and a body
// containing literal mustache pairs.
func TestSplitFrontMatter_DoesNotCorruptBodies(t *testing.T) {
	body := "Intro paragraph.\n\n---\n\nAfter the rule: {{ not_a_template }} and {{{ triple }}}.\n\n---\n"
	raw := "---\ntags:\n  - edge-case\n---\n" + body

	fm, gotBody, found := splitFrontMatter([]byte(raw))
	if !found {
		t.Fatal("front-matter not found")
	}
	if strings.TrimSpace(string(fm)) != "tags:\n  - edge-case" {
		t.Errorf("front-matter = %q", fm)
	}
	if gotBody != body {
		t.Errorf("body corrupted:\n got %q\nwant %q", gotBody, body)
	}
}

func TestSplitFrontMatter_AbsentAndUnterminated(t *testing.T) {
	for name, raw := range map[string]string{
		"no fence at all": "# Just a heading\n\nSome prose.\n",
		// An unterminated block must NOT be read as front-matter: doing so would
		// swallow the whole document into metadata and leave an empty body.
		"unterminated":                "---\ntags:\n  - a\n\nstill prose, no closing fence\n",
		"fence not on the first line": "\n---\ntags: [a]\n---\nbody\n",
	} {
		fm, body, found := splitFrontMatter([]byte(raw))
		if found {
			t.Errorf("%s: reported front-matter %q", name, fm)
		}
		if body != raw {
			t.Errorf("%s: body = %q, want the whole input", name, body)
		}
	}
}

// TestJoinFrontMatter_EmptyMetaWithFencedBody covers the silent corruption the
// obvious implementation has: a metadata-less document whose body OPENS with a
// rule, written with no front-matter block, reads back with its first paragraph
// parsed as metadata.
func TestJoinFrontMatter_EmptyMetaWithFencedBody(t *testing.T) {
	body := "---\ntitle: this is prose, not metadata\n---\nreal body\n"
	out, err := joinFrontMatter(mdMeta{}, body)
	if err != nil {
		t.Fatalf("joinFrontMatter: %v", err)
	}
	fm, gotBody, found := splitFrontMatter(out)
	if !found {
		t.Fatalf("no front-matter emitted, so the body was misread on re-read: %q", out)
	}
	if strings.TrimSpace(string(fm)) != "" {
		t.Errorf("front-matter = %q, want empty", fm)
	}
	if gotBody != body {
		t.Errorf("body = %q, want %q", gotBody, body)
	}
}

func TestJoinFrontMatter_OmitsBlockWhenThereIsNothingToRecord(t *testing.T) {
	out, err := joinFrontMatter(mdMeta{}, "plain prose\n")
	if err != nil {
		t.Fatalf("joinFrontMatter: %v", err)
	}
	if string(out) != "plain prose\n" {
		t.Errorf("out = %q, want the body verbatim", out)
	}
}

func TestJoinSplitFrontMatter_RoundTrip(t *testing.T) {
	meta := mdMeta{Description: "d", Tags: []string{"a", "b"}, NoDistill: true}
	body := "line one\n\n---\n\nline two {{ x }}\n"
	out, err := joinFrontMatter(meta, body)
	if err != nil {
		t.Fatalf("joinFrontMatter: %v", err)
	}
	fm, gotBody, found := splitFrontMatter(out)
	if !found {
		t.Fatal("front-matter not found after join")
	}
	var back mdMeta
	if err := unmarshalYAML(fm, &back); err != nil {
		t.Fatalf("unmarshalYAML: %v", err)
	}
	if back.Description != meta.Description || back.NoDistill != meta.NoDistill || strings.Join(back.Tags, ",") != "a,b" {
		t.Errorf("meta round-trip lost data: %+v", back)
	}
	if gotBody != body {
		t.Errorf("body = %q, want %q", gotBody, body)
	}
}

func TestPathHelpers(t *testing.T) {
	for _, tc := range []struct {
		path   string
		isMeta bool
		stem   string
	}{
		{"fragments/solid.md", false, "solid"},
		{"fragments/solid.distilled.md", false, "solid"},
		{"fragments/.solid.meta.yaml", true, "solid"},
		{"fragments/.solid.distilled.meta.yaml", true, "solid"},
		{"mcp/postgres.yaml", false, "postgres"},
		{"mcp/.postgres.meta.yaml", true, "postgres"},
		{"hooks/pre_tool/guard.yaml", false, "guard"},
		{"skills/.code-reviewer.meta.yaml", true, "code-reviewer"},
		{"skills/code-reviewer/SKILL.md", false, "SKILL"},
	} {
		if got := IsMetaPath(tc.path); got != tc.isMeta {
			t.Errorf("IsMetaPath(%q) = %v, want %v", tc.path, got, tc.isMeta)
		}
		if got := stemOf(tc.path); got != tc.stem {
			t.Errorf("stemOf(%q) = %q, want %q", tc.path, got, tc.stem)
		}
	}
	if got := MetaPath("mcp/postgres.yaml"); got != "mcp/.postgres.meta.yaml" {
		t.Errorf("MetaPath = %q", got)
	}
	if got := MetaPathForName("skills", "code-reviewer"); got != "skills/.code-reviewer.meta.yaml" {
		t.Errorf("MetaPathForName = %q", got)
	}
}

func TestFormOf(t *testing.T) {
	both := []signing.Form{signing.FormRaw, signing.FormDistilled}
	for _, tc := range []struct {
		path  string
		forms []signing.Form
		want  signing.Form
	}{
		{"fragments/solid.md", both, signing.FormRaw},
		{"fragments/solid.distilled.md", both, signing.FormDistilled},
		{"fragments/.solid.meta.yaml", both, signing.FormRaw},
		{"fragments/.solid.distilled.meta.yaml", both, signing.FormDistilled},
		{"mcp/postgres.yaml", execForms, signing.FormRaw},
		{"mcp/.postgres.meta.yaml", execForms, signing.FormRaw},
		{"skills/code-reviewer/SKILL.md", []signing.Form{signing.FormRaw}, signing.FormRaw},
	} {
		if got := formOf(tc.path, tc.forms); got != tc.want {
			t.Errorf("formOf(%q, %v) = %q, want %q", tc.path, tc.forms, got, tc.want)
		}
	}
}
