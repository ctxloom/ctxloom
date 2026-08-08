package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// Feature is one parsed .feature file: its name, free-text description, the
// tags on the "Feature:" line itself (used for @doc discovery), and its
// scenarios in file order.
type Feature struct {
	Path        string
	Name        string
	Description []string
	Tags        []string
	Scenarios   []Scenario
}

// Scenario is one "Scenario:" or "Scenario Outline:" block: its name, its own
// tags, and its raw Gherkin body (every line from the header through its last
// step/Examples row), used verbatim when a scenario has no capture to render
// from.
type Scenario struct {
	Name string
	Tags []string
	Body string
}

// ParseFeature parses a .feature file just enough to render it: deliberately
// not a general Gherkin parser, it knows only this project's feature-file
// shape ('#'-comments, '@tag' lines, 'Feature:'/'Scenario:'/'Scenario
// Outline:' headers). Ported from the scripts/living-docs-prototype Python
// prototype's parse_feature.
func ParseFeature(path string) (Feature, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Feature{}, fmt.Errorf("read %s: %w", path, err)
	}
	lines := strings.Split(string(data), "\n")

	var (
		featureName string
		featureDesc []string
		featureTags []string
		scenarios   []Scenario
		currentTags []string
		seenFeature bool
	)

	n := len(lines)
	i := 0
	for i < n {
		raw := lines[i]
		stripped := strings.TrimSpace(raw)

		switch {
		case strings.HasPrefix(stripped, "#"):
			i++

		case strings.HasPrefix(stripped, "@"):
			currentTags = strings.Fields(stripped)
			i++

		case strings.HasPrefix(stripped, "Feature:"):
			featureName = strings.TrimSpace(strings.TrimPrefix(stripped, "Feature:"))
			featureTags = currentTags
			seenFeature = true
			currentTags = nil
			featureDesc, i = parseFeatureDescription(lines, i+1)

		case strings.HasPrefix(stripped, "Scenario Outline:") || strings.HasPrefix(stripped, "Scenario:"):
			_, rest, _ := strings.Cut(stripped, ":")
			var body string
			body, i = parseScenarioBody(lines, i)
			scenarios = append(scenarios, Scenario{
				Name: strings.TrimSpace(rest),
				Tags: currentTags,
				Body: body,
			})
			currentTags = nil

		default:
			i++
		}
	}

	if !seenFeature {
		return Feature{}, fmt.Errorf("%s: no 'Feature:' line found", path)
	}
	return Feature{
		Path:        path,
		Name:        featureName,
		Description: featureDesc,
		Tags:        featureTags,
		Scenarios:   scenarios,
	}, nil
}

// parseFeatureDescription consumes the free-text lines following a "Feature:"
// header, starting at index i, and returns them with the index to resume at.
// Blank lines are dropped; the first tag, comment or scenario header ends the
// description.
func parseFeatureDescription(lines []string, i int) ([]string, int) {
	var desc []string
	for ; i < len(lines); i++ {
		s := strings.TrimSpace(lines[i])
		if strings.HasPrefix(s, "@") || strings.HasPrefix(s, "#") ||
			strings.HasPrefix(s, "Scenario:") || strings.HasPrefix(s, "Scenario Outline:") {
			break
		}
		if s != "" {
			desc = append(desc, s)
		}
	}
	return desc, i
}

// parseScenarioBody consumes one scenario, starting at its header line at
// index i, and returns its raw Gherkin body with trailing blank lines trimmed
// plus the index to resume at. Comments are source rather than spec and are
// dropped; a tag run ends the body only when a new scenario follows it (see
// tagsStartNewScenario).
func parseScenarioBody(lines []string, i int) (string, int) {
	body := []string{lines[i]}
	for i++; i < len(lines); i++ {
		s := strings.TrimSpace(lines[i])
		if strings.HasPrefix(s, "#") {
			continue
		}
		if strings.HasPrefix(s, "@") && tagsStartNewScenario(lines, i) {
			break
		}
		if strings.HasPrefix(s, "Scenario:") || strings.HasPrefix(s, "Scenario Outline:") {
			break
		}
		body = append(body, lines[i])
	}
	for len(body) > 0 && strings.TrimSpace(body[len(body)-1]) == "" {
		body = body[:len(body)-1]
	}
	return strings.Join(body, "\n"), i
}

// tagsStartNewScenario reports whether the tag line at index i belongs to a
// NEW scenario rather than to the one being parsed. Gherkin attaches tags to
// an Examples: block (they cannot attach to a single table row), so a run of
// tags followed by "Examples:" is part of the current Scenario Outline, while
// a run of tags followed by a Scenario header ends it. Blank lines, comments
// and further tag lines are skipped on the way. A run of tags at end of file
// belongs to nothing and is treated as a terminator.
func tagsStartNewScenario(lines []string, i int) bool {
	for ; i < len(lines); i++ {
		s := strings.TrimSpace(lines[i])
		if s == "" || strings.HasPrefix(s, "#") || strings.HasPrefix(s, "@") {
			continue
		}
		return strings.HasPrefix(s, "Scenario:") || strings.HasPrefix(s, "Scenario Outline:")
	}
	return true
}

// DiscoverDocFeatures returns every @doc-tagged .feature file in dir, sorted
// by path. Rendering the whole directory rather than a hardcoded journey list
// means a new journey (J000400+) gets a page for free the moment it lands and is
// tagged @doc.
func DiscoverDocFeatures(dir string) ([]string, error) {
	// RECURSIVE, deliberately: features/ is split into journeys/ and cli/
	// subdirectories, and a flat glob stops seeing a file the moment it moves
	// into one. Failing to FIND a @doc feature does not fail loudly here — it
	// silently drops that feature's page from the published site, which is the
	// quietest possible way for documentation to go missing.
	var matches []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".feature") {
			matches = append(matches, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", dir, err)
	}
	sort.Strings(matches)

	var docFeatures []string
	for _, m := range matches {
		feat, err := ParseFeature(m)
		if err != nil {
			return nil, err
		}
		if slices.Contains(feat.Tags, "@doc") {
			docFeatures = append(docFeatures, m)
		}
	}
	return docFeatures, nil
}
