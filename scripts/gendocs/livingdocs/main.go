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
// tests/acceptance/features/j1_setup.feature -> j1-setup.md.
func slug(featurePath string) string {
	base := strings.TrimSuffix(filepath.Base(featurePath), ".feature")
	return strings.ReplaceAll(base, "_", "-") + ".md"
}

// narrationPathFor returns the .doc.md companion path for a .feature file,
// and whether it actually exists (narration is optional — see LoadNarration).
func narrationPathFor(featurePath string) string {
	return strings.TrimSuffix(featurePath, ".feature") + ".doc.md"
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
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
		feat, err := ParseFeature(fp)
		if err != nil {
			return err
		}

		// Fail CLOSED, not open: a @doc feature with at least one scenario but
		// ZERO of them captured is a strong, unambiguous signal that
		// CTXLOOM_DOC_CAPTURE_DIR was misconfigured for this run (wrong path,
		// the acceptance suite didn't actually execute, the capture dir got
		// cleared after the run) — not a legitimate "nothing to show" case.
		// Every @doc feature in this repo mixes captured scenarios with, at
		// most, a handful of individually-tagged @live/@wip ones (see
		// j5_multi_engine.feature) — no @doc feature is EVER entirely
		// uncaptured by design. Left unchecked, this is the other half of the
		// same class of bug as EvidenceGapError: every scenario would quietly
		// render "Not captured in this build" and both the test run and this
		// generator would still exit 0.
		if len(feat.Scenarios) > 0 {
			anyCaptured := false
			for _, sc := range feat.Scenarios {
				if len(captures[sc.Name]) > 0 {
					anyCaptured = true
					break
				}
			}
			if !anyCaptured {
				return fmt.Errorf(
					"REFUSING TO GENERATE: @doc feature %q (%s) has ZERO captured scenarios out of %d — "+
						"CTXLOOM_DOC_CAPTURE_DIR (%s) is likely misconfigured or the acceptance run never executed this feature; "+
						"every step would otherwise silently render \"Not captured in this build\"",
					feat.Name, fp, len(feat.Scenarios), captureDir,
				)
			}
		}

		docPath := narrationPathFor(fp)
		narrArg := ""
		if fileExists(docPath) {
			narrArg = docPath
		}
		narr, err := LoadNarration(docPath)
		if err != nil {
			return err
		}

		content, err := GeneratePage(feat, narr, captures, narrArg)
		if err != nil {
			return err
		}
		pages = append(pages, generatedPage{name: slug(fp), content: content})
	}

	// Every feature validated clean — now (and only now) touch disk. outDir
	// holds nothing but generated pages, so it is fully replaced each run:
	// a feature file that was renamed or removed can never leave a stale page
	// behind.
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
