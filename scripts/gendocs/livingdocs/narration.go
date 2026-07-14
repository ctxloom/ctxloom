package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// Narration is a journey's human-written prose, keyed to a .feature file's
// scenarios by name via <!-- doc:scenario: <name> --> markers in a companion
// .doc.md file.
type Narration struct {
	Intro     string
	Outro     string
	Scenarios map[string]string
}

var (
	introRe    = regexp.MustCompile(`(?s)<!--\s*doc:intro\s*-->(.*?)<!--\s*/doc:intro\s*-->`)
	outroRe    = regexp.MustCompile(`(?s)<!--\s*doc:outro\s*-->(.*?)<!--\s*/doc:outro\s*-->`)
	scenarioRe = regexp.MustCompile(`(?s)<!--\s*doc:scenario:\s*(.*?)\s*-->(.*?)<!--\s*/doc:scenario\s*-->`)
)

// LoadNarration reads a .doc.md companion. Narration is OPTIONAL: a journey
// with no companion file (e.g. a newly landed one, not yet hand-narrated)
// yields a zero-value Narration and no error, so the page still generates —
// just without intro/outro/scenario prose.
func LoadNarration(path string) (Narration, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Narration{Scenarios: map[string]string{}}, nil
		}
		return Narration{}, fmt.Errorf("read %s: %w", path, err)
	}
	text := string(data)

	narr := Narration{Scenarios: map[string]string{}}
	if m := introRe.FindStringSubmatch(text); m != nil {
		narr.Intro = strings.TrimSpace(m[1])
	}
	if m := outroRe.FindStringSubmatch(text); m != nil {
		narr.Outro = strings.TrimSpace(m[1])
	}
	for _, m := range scenarioRe.FindAllStringSubmatch(text, -1) {
		narr.Scenarios[strings.TrimSpace(m[1])] = strings.TrimSpace(m[2])
	}
	return narr, nil
}
