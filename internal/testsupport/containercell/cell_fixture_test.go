package containercell_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/errs"
	"github.com/ctxloom/ctxloom/internal/testsupport/bundletree"
)

// The fixture lives outside the docker_integration build tag so that the
// refusal below is proven by the ordinary suite, without a container runtime.

// The fixture's markers. Long enough that a containment assertion is a real
// claim about the payload rather than a search for a token that could survive
// truncation.
const (
	// cellBundle is the bundle the fixture's default profile draws on.
	cellBundle = "cell"

	fragmentBody = "CELL-FRAGMENT-4a91c2 the published fragment body must cross the process boundary verbatim,\n" +
		"which is a claim about several lines of prose and not about one marker token.\n"
	skillBody  = "CELL-SKILL-3e77da the skill package's own bytes, delivered whole."
	scriptBody = "#!/bin/sh\necho CELL-SCRIPT-7d33e1\n"
)

// writeCellFixture authors a minimal ctxloom project whose default profile
// selects one fragment and one skill package, and returns the project dir.
//
// A directory-form bundle is required, not incidental: a skill package is
// multi-file and a single-file bundle cannot hold one.
func writeCellFixture(t *testing.T, root string) string {
	t.Helper()
	project := filepath.Join(root, "project")
	bundlesRoot := paths.LocalBundlesPathFor(filepath.Join(project, ".ctxloom"), paths.LayoutV2)
	bundle := filepath.Join(bundlesRoot, cellBundle)
	mustMkdirAll(t, filepath.Join(root, "home"))
	mustMkdirAll(t, filepath.Join(project, ".ctxloom", "profiles"))

	var b strings.Builder
	b.WriteString("version: 1.0.0\ndescription: container cell fixture\nfragments:\n  cell-marker:\n    tags: [cell]\n    content: |\n")
	for _, line := range strings.Split(strings.TrimRight(fragmentBody, "\n"), "\n") {
		fmt.Fprintf(&b, "      %s\n", line)
	}
	bundletree.WriteOS(t, bundlesRoot, cellBundle, b.String())
	mustMkdirAll(t, filepath.Join(bundle, "skills", "reviewer", "scripts"))

	mustWrite(t, filepath.Join(bundle, "skills", "reviewer", "SKILL.md"),
		"---\nname: reviewer\ndescription: container cell fixture skill\n---\n"+skillBody+"\n", 0o644)
	// 0755 AT THE SOURCE is what makes the delivered 0755 a real claim: a
	// fixture that published a non-executable script would assert the harness
	// rather than the product.
	mustWrite(t, filepath.Join(bundle, "skills", "reviewer", "scripts", "run.sh"), scriptBody, 0o755)

	mustWrite(t, filepath.Join(project, ".ctxloom", "profiles", "default.yaml"),
		"name: default\nfragments:\n  - cell#fragments/cell-marker\nskills:\n  - cell#skills/reviewer\n", 0o644)
	mustWrite(t, filepath.Join(project, ".ctxloom", "config.yaml"), "version: 4\n", 0o644)
	return project
}

func mustMkdirAll(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	// os.WriteFile applies the process umask, and the exec bit is the whole
	// point of the 0755 entry — re-assert it rather than publishing a fixture
	// the umask silently weakened.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// requireCellBundle refuses, naming the bundle, when the fixture's bundle is
// not one the product would load from the project.
//
// It reads through the product's own project reader rather than checking for a
// path: the original form of this fault was a fixture writing a bundle where
// the product no longer looked, and a path check written beside the fixture
// would have agreed with the fixture.
func requireCellBundle(project string) error {
	if _, err := bundles.NewFSStore(nil, []string{paths.LocalBundlesPath(filepath.Join(project, ".ctxloom"))}).Load(cellBundle); err != nil {
		return fmt.Errorf("the container-cell lane needs the fixture bundle %q, and the product cannot load it from %s: %w", cellBundle, project, err)
	}
	return nil
}

func TestCellFixture_BundleLoadsAsTheProductWouldLoadIt(t *testing.T) {
	if err := requireCellBundle(writeCellFixture(t, t.TempDir())); err != nil {
		t.Fatal(err)
	}
}

// A profile naming a fragment whose bundle is absent is a warning and a skip to
// the product, so without this the lane reports the absence only as a context
// file that lacks the fragment body, four assertions from its cause.
func TestCellFixture_RefusesNamingTheBundleWhenItIsMissing(t *testing.T) {
	project := writeCellFixture(t, t.TempDir())
	bundleDir := filepath.Join(paths.LocalBundlesPathFor(filepath.Join(project, ".ctxloom"), paths.LayoutV2), cellBundle)
	if err := os.RemoveAll(bundleDir); err != nil {
		t.Fatal(err)
	}

	err := requireCellBundle(project)
	if !errors.Is(err, errs.ErrBundleNotFound) {
		t.Fatalf("requireCellBundle with the bundle removed = %v, want an error wrapping %v", err, errs.ErrBundleNotFound)
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("%q", cellBundle)) {
		t.Fatalf("the refusal does not name the bundle %q: %v", cellBundle, err)
	}
}
