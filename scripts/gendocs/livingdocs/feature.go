package main

import (
	"fmt"
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
			i++
			for i < n {
				s2 := strings.TrimSpace(lines[i])
				if strings.HasPrefix(s2, "@") || strings.HasPrefix(s2, "#") ||
					strings.HasPrefix(s2, "Scenario:") || strings.HasPrefix(s2, "Scenario Outline:") {
					break
				}
				if s2 != "" {
					featureDesc = append(featureDesc, s2)
				}
				i++
			}

		case strings.HasPrefix(stripped, "Scenario Outline:") || strings.HasPrefix(stripped, "Scenario:"):
			_, rest, _ := strings.Cut(stripped, ":")
			name := strings.TrimSpace(rest)
			tags := currentTags
			currentTags = nil
			body := []string{raw}
			i++
			for i < n {
				nxt := lines[i]
				s2 := strings.TrimSpace(nxt)
				if strings.HasPrefix(s2, "#") {
					i++
					continue
				}
				if strings.HasPrefix(s2, "@") {
					break
				}
				if strings.HasPrefix(s2, "Scenario:") || strings.HasPrefix(s2, "Scenario Outline:") {
					break
				}
				body = append(body, nxt)
				i++
			}
			for len(body) > 0 && strings.TrimSpace(body[len(body)-1]) == "" {
				body = body[:len(body)-1]
			}
			scenarios = append(scenarios, Scenario{
				Name: name,
				Tags: tags,
				Body: strings.Join(body, "\n"),
			})

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

// DiscoverDocFeatures returns every @doc-tagged .feature file in dir, sorted
// by path. Rendering the whole directory rather than a hardcoded journey list
// means a new journey (J5+) gets a page for free the moment it lands and is
// tagged @doc.
func DiscoverDocFeatures(dir string) ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.feature"))
	if err != nil {
		return nil, fmt.Errorf("glob %s: %w", dir, err)
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
