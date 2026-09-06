//go:build migratev2

// migratev2 is a ONE-OFF migration: it decomposes a bundle repo's format-v1
// bundles into format-v2 tree form under the v2 layout root.
//
// It is deliberately not a `bundle convert` verb. Each format migration is its
// own transformation, and v3's shape is unknown, so a general converter would be
// speculative generality for an unknown target. DELETE THIS AFTER THE MIGRATION.
//
// It calls internal/content/convert -- the SAME converter authoring uses --
// rather than reimplementing the tree layout. A second implementation of the
// format would drift, and the drift surfaces as a signature that stops verifying
// rather than as a test failure.
//
//	go run -tags migratev2 ./cmd/migratev2 -repo /path/to/repo [-apply]
//
// Without -apply it reports what it WOULD do and writes nothing.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/content"
	"github.com/ctxloom/ctxloom/internal/content/convert"
	"github.com/ctxloom/ctxloom/internal/paths"
)

// source is one bundle to convert, and where its bytes and skill packages live.
type source struct {
	name string // the bundle's single-segment id
	doc  string // path to the envelope: <name>.yaml, or <name>/bundle.yaml
	dir  string // directory holding skill packages, or "" for a single-file bundle
}

func main() {
	repo := flag.String("repo", "", "path to the bundle repo (the checkout root)")
	apply := flag.Bool("apply", false, "actually write; without this it is a dry run")
	flag.Parse()
	if *repo == "" {
		fail("-repo is required")
	}

	fsys := afero.NewOsFs()
	bundlesDir := paths.LocalBundlesPath(filepath.Join(*repo, paths.AppDirName))
	dest := paths.LocalBundlesPathFor(filepath.Join(*repo, paths.AppDirName), paths.LayoutV2)

	srcs, err := discover(fsys, bundlesDir)
	if err != nil {
		fail("discover: %v", err)
	}
	if len(srcs) == 0 {
		fail("no bundles found under %s -- wrong -repo, or already migrated", bundlesDir)
	}

	fmt.Printf("repo   %s\nfrom   %s\nto     %s\nmode   %s\n\n",
		*repo, bundlesDir, dest, map[bool]string{true: "APPLY", false: "dry run"}[*apply])

	var converted, skipped, failed int
	for _, s := range srcs {
		note, err := convertOne(fsys, s, dest, *apply)
		switch {
		case err != nil:
			// Report and keep going: one unconvertible bundle must not hide the
			// state of the other sixty.
			fmt.Printf("  FAIL   %-32s %v\n", s.name, err)
			failed++
		case note != "":
			fmt.Printf("  skip   %-32s %s\n", s.name, note)
			skipped++
		default:
			fmt.Printf("  ok     %-32s -> %s\n", s.name, filepath.Join(dest, s.name))
			converted++
		}
	}

	fmt.Printf("\nconverted %d, skipped %d, failed %d\n", converted, skipped, failed)
	if failed > 0 {
		os.Exit(1)
	}
	if !*apply {
		fmt.Println("dry run: nothing was written. re-run with -apply")
	}
}

// discover finds every format-v1 bundle directly under the bare bundles root.
// It deliberately ignores the layout roots themselves, so re-running after a
// partial migration does not try to convert what it already converted.
func discover(fsys afero.Fs, root string) ([]source, error) {
	entries, err := afero.ReadDir(fsys, root)
	if err != nil {
		return nil, err
	}
	var out []source
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			if _, isLayout := layoutSegments()[name]; isLayout {
				continue // v1/ or v2/, not a bundle
			}
			doc := filepath.Join(root, name, bundles.DirectoryFormManifest)
			if ok, _ := afero.Exists(fsys, doc); !ok {
				continue // not a bundle at all (e.g. an orphaned SHA256SUMS dir)
			}
			out = append(out, source{name: name, doc: doc, dir: filepath.Join(root, name)})
			continue
		}
		if !strings.HasSuffix(name, ".yaml") {
			continue // .sig siblings and anything else
		}
		out = append(out, source{
			name: strings.TrimSuffix(name, ".yaml"),
			doc:  filepath.Join(root, name),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out, nil
}

func layoutSegments() map[string]struct{} {
	segs := map[string]struct{}{}
	for _, l := range paths.BundleLayouts() {
		if s, err := l.Segment(); err == nil && s != "" {
			segs[s] = struct{}{}
		}
	}
	return segs
}

func convertOne(fsys afero.Fs, s source, dest string, apply bool) (string, error) {
	data, err := afero.ReadFile(fsys, s.doc)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", s.doc, err)
	}
	b, err := bundles.ParseBundle(data)
	if err != nil {
		return "", fmt.Errorf("parse %s: %w", s.doc, err)
	}

	target := filepath.Join(dest, s.name)
	if ok, _ := afero.Exists(fsys, target); ok {
		return "already present at the v2 root", nil
	}

	var opts convert.Options
	if len(b.Skills) > 0 {
		if s.dir == "" {
			// The converter refuses this too; saying so here names the bundle.
			return "", fmt.Errorf("declares %d skill(s) but is a single file, so their package files have no source directory", len(b.Skills))
		}
		opts.SkillFiles = convert.SkillFilesFromDir(fsys, s.dir, b)
	}

	if !apply {
		items, err := convert.Plan(content.BundleID(s.name), b, opts)
		if err != nil {
			return "", fmt.Errorf("plan: %w", err)
		}
		// Reported as a SKIP, not a success: a dry run writes nothing, and
		// saying "ok" for work that did not happen is the failure mode this
		// whole migration keeps tripping over.
		return fmt.Sprintf("would write %d item(s)", len(items)), nil
	}

	if err := fsys.MkdirAll(dest, 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", dest, err)
	}
	store, err := content.NewTreeStore(fsys, dest, content.Provenance{IsLocal: true})
	if err != nil {
		return "", fmt.Errorf("open tree store at %s: %w", dest, err)
	}
	if err := convert.Convert(context.Background(), store, content.BundleID(s.name), b, opts); err != nil {
		_ = fsys.RemoveAll(target)
		return "", fmt.Errorf("convert: %w", err)
	}

	// convert.Convert is a NO-OP for a bundle that plans zero items, so without
	// this it returns nil having written nothing: exit 0 and no bytes. Assert
	// the envelope landed rather than trusting the call.
	envelope := filepath.Join(target, bundles.DirectoryFormManifest)
	if ok, err := afero.Exists(fsys, envelope); err != nil || !ok {
		_ = fsys.RemoveAll(target)
		return "", fmt.Errorf("wrote nothing: the bundle plans zero items, so there is no tree to write")
	}
	return "", nil
}

func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "migratev2: "+format+"\n", a...)
	os.Exit(2)
}
