// Command livingdocs turns passing Gherkin acceptance scenarios into
// published Starlight docs pages: the Gherkin step on the left, the REAL
// captured CLI output from that step on the right. It renders a page for
// every @doc-tagged feature file under --features-dir, using whatever
// evidence tests/acceptance/steps_doc_capture.go wrote to --capture-dir
// during an actual passing `go test -tags acceptance` run, plus an optional
// <feature>.doc.md narration companion.
//
// The product claim this enforces: a feature that does not work cannot be
// documented — AND a feature that proves nothing cannot be documented either.
// If any scenario's capture has a non-passing step, or a passing scenario's
// assertion step captured no evidence at all, or an entire @doc feature has
// zero captured scenarios (the capture directory misconfigured, or the
// capture run never executed it), this command writes NOTHING and exits
// nonzero — see RefusalError and EvidenceGapError in render.go.
//
// Run via `just gen-living-docs` (go run ./scripts/gendocs/livingdocs).
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// slug turns a feature file path into its output page name:
// tests/acceptance/features/j000200_setup.feature -> j000200-setup.md.
func slug(featurePath string) string {
	base := strings.TrimSuffix(filepath.Base(featurePath), ".feature")
	return strings.ReplaceAll(base, "_", "-") + ".md"
}

// narrationPathFor returns the .doc.md companion path for a .feature file,
// and whether it actually exists (narration is optional — see LoadNarration).
func narrationPathFor(featurePath string) string {
	return strings.TrimSuffix(featurePath, ".feature") + ".doc.md"
}

// warnf writes a generator warning. A package var so a test can capture it;
// production sends it to stderr, alongside the "wrote ..." lines on stdout.
var warnf = func(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "gen-living-docs: "+format+"\n", a...)
}

type generatedPage struct {
	name    string
	content string
}

// run discovers every @doc feature, validates ALL of them before writing
// anything (a refusal anywhere aborts the whole batch — no partial output),
// then replaces the contents of outDir with the freshly generated pages.
func run(featuresDir, captureDir, outDir string) error {
	featureFiles, err := DiscoverDocFeatures(featuresDir)
	if err != nil {
		return fmt.Errorf("discover @doc features in %s: %w", featuresDir, err)
	}
	if len(featureFiles) == 0 {
		return fmt.Errorf("no @doc-tagged feature files found in %s", featuresDir)
	}

	captures, err := LoadCaptures(captureDir)
	if err != nil {
		return fmt.Errorf("load captures from %s: %w", captureDir, err)
	}

	pages := make([]generatedPage, 0, len(featureFiles))
	for _, fp := range featureFiles {
		page, err := buildPage(fp, captureDir, captures)
		if err != nil {
			return err
		}
		pages = append(pages, page)
	}

	return writePages(outDir, pages)
}

// buildPage validates one @doc feature and renders its page. It writes
// nothing: run collects every page and only then touches disk, so a refusal
// anywhere aborts the whole batch with no partial output.
func buildPage(featurePath, captureDir string, captures map[string][]DocCapture) (generatedPage, error) {
	feat, err := ParseFeature(featurePath)
	if err != nil {
		return generatedPage{}, err
	}
	if err := assertSomethingCaptured(feat, featurePath, captureDir, captures); err != nil {
		return generatedPage{}, err
	}

	docPath := narrationPathFor(featurePath)
	narrArg := ""
	if _, statErr := os.Stat(docPath); statErr == nil {
		narrArg = docPath
	}
	narr, err := LoadNarration(docPath)
	if err != nil {
		return generatedPage{}, err
	}

	// A doc:scenario marker naming a scenario that no longer exists is prose
	// the author wrote and the page will not carry. Rendering consults
	// narration only BY an existing scenario's name, so without this the
	// mismatch produces a complete-looking page, a zero exit, and no mention
	// anywhere that a paragraph was dropped. Reported rather than refused: the
	// page itself is still true, and refusing would make a docs build fail on
	// an editorial mismatch the generator cannot fix.
	for _, orphan := range OrphanNarrations(feat, narr) {
		warnf("%s: narration for %q matches no scenario in %s — that prose will NOT appear on the generated page", docPath, orphan, featurePath)
	}

	content, err := GeneratePage(feat, narr, captures, narrArg)
	if err != nil {
		return generatedPage{}, err
	}
	return generatedPage{name: slug(featurePath), content: content}, nil
}

// assertSomethingCaptured fails CLOSED, not open: a @doc feature with at least
// one scenario but ZERO of them captured is a strong, unambiguous signal that
// CTXLOOM_DOC_CAPTURE_DIR was misconfigured for this run (wrong path, the
// acceptance suite didn't actually execute, the capture dir got cleared after
// the run) — not a legitimate "nothing to show" case. Every @doc feature in
// this repo mixes captured scenarios with, at most, a handful of
// individually-tagged @live/@wip ones (see j000400_multi_engine.feature) — no @doc
// feature is EVER entirely uncaptured by design. Left unchecked, this is the
// other half of the same class of bug as EvidenceGapError: every scenario
// would quietly render "Not captured in this build" and both the test run and
// this generator would still exit 0.
func assertSomethingCaptured(feat Feature, featurePath, captureDir string, captures map[string][]DocCapture) error {
	if len(feat.Scenarios) == 0 {
		return nil
	}
	for _, sc := range feat.Scenarios {
		if len(captures[sc.Name]) > 0 {
			return nil
		}
	}
	return fmt.Errorf(
		"REFUSING TO GENERATE: @doc feature %q (%s) has ZERO captured scenarios out of %d — "+
			"CTXLOOM_DOC_CAPTURE_DIR (%s) is likely misconfigured or the acceptance run never executed this feature; "+
			"every step would otherwise silently render \"Not captured in this build\"",
		feat.Name, featurePath, len(feat.Scenarios), captureDir,
	)
}

// writePages replaces outDir's contents with pages. outDir holds nothing but
// generated pages, so it is fully replaced each run: a feature file that was
// renamed or removed can never leave a stale page behind.
func writePages(outDir string, pages []generatedPage) error {
	if err := os.RemoveAll(outDir); err != nil {
		return fmt.Errorf("clear %s: %w", outDir, err)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", outDir, err)
	}
	for _, p := range pages {
		dest := filepath.Join(outDir, p.name)
		if err := os.WriteFile(dest, []byte(p.content), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", dest, err)
		}
		fmt.Printf("wrote %s (%d bytes)\n", dest, len(p.content))
	}
	return nil
}

func main() {
	fs := flag.NewFlagSet("livingdocs", flag.ExitOnError)
	featuresDir := fs.String("features-dir", "tests/acceptance/features",
		"directory of .feature files to scan for @doc tags")
	defaultCaptureDir := os.Getenv("CTXLOOM_DOC_CAPTURE_DIR")
	if defaultCaptureDir == "" {
		defaultCaptureDir = ".cache/doc-capture"
	}
	captureDir := fs.String("capture-dir", defaultCaptureDir,
		"directory of per-scenario JSON captures written by steps_doc_capture.go")
	outDir := fs.String("out-dir", "website/src/content/docs/journeys",
		"directory to write generated Starlight journey pages into")
	if err := fs.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}

	if err := run(*featuresDir, *captureDir, *outDir); err != nil {
		fmt.Fprintf(os.Stderr, "gen-living-docs: %v\n", err)
		os.Exit(1)
	}
}
