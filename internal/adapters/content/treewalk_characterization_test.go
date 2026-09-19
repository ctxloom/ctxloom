package content

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This file CHARACTERIZES the tree walker rather than specifying it.
//
// The walker is shipped, tested and about to be refactored onto a storage seam.
// The unit tests around it each pin one property; none of them pins the WHOLE
// answer, so a refactor could preserve every asserted property and still change
// which files group together, which items are enumerated, or what a form
// digests to. This snapshot pins the whole answer at once: every bundle, every
// file, every ref, every form, every component and every digest, rendered as
// text and compared against a golden file.
//
// It is deliberately a golden file rather than inline expectations. The point
// of a characterization test is that nobody hand-wrote the expected values —
// they were READ OFF the shipped implementation — so the diff on a refactor
// says exactly what moved.
//
// Regenerate with CTXLOOM_UPDATE_GOLDEN=1. Regenerating during a refactor
// defeats the entire purpose; a diff here is a finding, not a chore.

const goldenTreeWalk = "testdata/golden/treewalk.txt"

func TestTreeWalk_Characterization(t *testing.T) {
	got := snapshotStore(t, fixtureStore(t))
	compareGolden(t, goldenTreeWalk, got)
}

// TestTreeWalk_CharacterizationIsOrderIndependent pins the snapshot itself
// against the one property that would make it a useless witness: if the answer
// depended on the order files were created in, two runs over the same content
// would disagree and the golden would be noise.
func TestTreeWalk_CharacterizationIsOrderIndependent(t *testing.T) {
	forward := snapshotStore(t, fixtureStore(t))
	reversed := snapshotStore(t, reverseOrderFixtureStore(t))
	if forward != reversed {
		t.Fatalf("snapshot depends on file creation order:\n%s", firstDiff(forward, reversed))
	}
}

// snapshotStore renders every answer a Store gives, as deterministic text.
//
// It takes the Store INTERFACE, not *TreeStore, so the same snapshot can be
// taken of any backend — which is what lets a later test assert that a
// bytes-only backend and the afero-backed one agree byte-for-byte.
func snapshotStore(t *testing.T, s Store) string {
	t.Helper()
	ctx := context.Background()
	var b strings.Builder

	ids, err := s.Bundles(ctx)
	if err != nil {
		t.Fatalf("Bundles: %v", err)
	}
	for _, id := range ids {
		fmt.Fprintf(&b, "bundle %s\n", id)
		bundle, err := s.Open(ctx, id)
		if err != nil {
			t.Fatalf("Open %s: %v", id, err)
		}
		files, err := bundle.Files(ctx)
		if err != nil {
			t.Fatalf("Files %s: %v", id, err)
		}
		for _, f := range files {
			raw, err := bundle.ReadFile(ctx, f)
			if err != nil {
				t.Fatalf("ReadFile %s/%s: %v", id, f, err)
			}
			fmt.Fprintf(&b, "  file %s size=%d sha=%s\n", f, len(raw), shortSum(raw))
		}
		refs, err := bundle.Refs(ctx)
		if err != nil {
			t.Fatalf("Refs %s: %v", id, err)
		}
		for _, ref := range refs {
			fmt.Fprintf(&b, "  ref %s local=%t builtin=%t repo=%q\n",
				ref.Key(), ref.IsLocal, ref.IsBuiltin, ref.RepoURL)
			item, err := bundle.Item(ctx, ref)
			if err != nil {
				t.Fatalf("Item %s: %v", ref.Key(), err)
			}
			snapshotItem(t, &b, item)
		}
	}
	return b.String()
}

func snapshotItem(t *testing.T, b *strings.Builder, item Item) {
	t.Helper()
	ctx := context.Background()
	key := item.Ref().Key()
	forms, err := item.Forms(ctx)
	if err != nil {
		t.Fatalf("Forms %s: %v", key, err)
	}
	for _, f := range forms {
		form, err := item.Form(ctx, f)
		if err != nil {
			t.Fatalf("Form %s %s: %v", key, f, err)
		}
		digest, err := form.Content(ctx)
		if err != nil {
			t.Fatalf("Content %s %s: %v", key, f, err)
		}
		fmt.Fprintf(b, "    form %s key=%s\n", f, contentKey(digest))
		components, err := form.Components(ctx)
		if err != nil {
			t.Fatalf("Components %s %s: %v", key, f, err)
		}
		for _, c := range components {
			fmt.Fprintf(b, "      component %s mode=%s size=%d sha=%s\n",
				c.Path, c.Mode, len(c.Bytes), shortSum(c.Bytes))
		}
	}
}

func shortSum(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:16]
}

func compareGolden(t *testing.T, path, got string) {
	t.Helper()
	if os.Getenv("CTXLOOM_UPDATE_GOLDEN") != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("writing golden: %v", err)
		}
		t.Fatalf("golden %s rewritten; re-run without CTXLOOM_UPDATE_GOLDEN", path)
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading golden %s (regenerate with CTXLOOM_UPDATE_GOLDEN=1): %v", path, err)
	}
	if got != string(want) {
		t.Fatalf("tree walk answer changed:\n%s", firstDiff(string(want), got))
	}
}

// firstDiff reports the first differing line, with a little context. A whole-file
// dump of two ~90-line snapshots buries the one line that moved.
func firstDiff(want, got string) string {
	wl, gl := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := 0; i < len(wl) || i < len(gl); i++ {
		var w, g string
		if i < len(wl) {
			w = wl[i]
		}
		if i < len(gl) {
			g = gl[i]
		}
		if w != g {
			return fmt.Sprintf("line %d:\n  want: %q\n   got: %q\n(%d want lines, %d got lines)",
				i+1, w, g, len(wl), len(gl))
		}
	}
	return "(no line differs, but the strings do)"
}
