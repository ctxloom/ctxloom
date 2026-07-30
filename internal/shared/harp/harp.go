// Package harp generates Human Appropriate Random Phraselets — pronounceable,
// memorable identifiers of the form "swift-amber-falcon".
//
// This is a fully-native Go port of harp-core (github.com/benjaminabbitt/harp).
// Word lists are embedded; no cgo, no WASM, no wazero. API mirrors the Rust
// crate so a future extraction to github.com/benjaminabbitt/harp-go is
// mechanical: copy this directory out, change the package import path, done.
package harp

import (
	"crypto/rand"
	"embed"
	"encoding/binary"
	"fmt"
	"io/fs"
	"strings"
)

// DefaultGroup is the word-list group used when Options.Group is empty or
// names a group that does not exist.
const DefaultGroup = "default"

// MinComponents and MaxComponents bound Options.Components. normalize clamps to
// them; a caller that ADVERTISES the range to a human (a CLI flag's help text)
// must read it from here rather than restating it, so what is promised and what
// is enforced cannot drift apart.
const (
	MinComponents = 2
	MaxComponents = 16
)

// Word-list files are embedded as "<group>.<type>.txt", where type is
// "adjectives" or "nouns". A group is usable only if it provides both.
//
//go:embed *.txt
var wordFS embed.FS

// wordGroup holds the two lists that make up a name for one group.
type wordGroup struct {
	adjectives []string
	nouns      []string
}

const (
	typeAdjectives = "adjectives"
	typeNouns      = "nouns"
)

// groups maps group name -> its word lists, loaded once from the embedded
// files. Groups missing either list are dropped.
var groups = loadGroups()

// loadGroups parses every embedded "<group>.<type>.txt" file into the
// registry. A group survives only if it has both adjectives and nouns.
func loadGroups() map[string]wordGroup {
	entries, err := fs.ReadDir(wordFS, ".")
	if err != nil {
		panic(fmt.Sprintf("harp: read embedded word lists: %v", err))
	}
	out := make(map[string]wordGroup)
	for _, e := range entries {
		group, typ, words, ok := parseWordGroupEntry(e.Name())
		if !ok {
			continue
		}
		g := out[group]
		if assignWordGroup(&g, typ, words) {
			out[group] = g
		}
	}
	pruneIncompleteGroups(out)
	return out
}

// parseWordGroupEntry parses a "<group>.<type>.txt" embedded word-list filename,
// returning the parsed words. ok is false for files that don't match the shape.
func parseWordGroupEntry(name string) (group, typ string, words []string, ok bool) {
	rest, ok := strings.CutSuffix(name, ".txt")
	if !ok {
		return "", "", nil, false
	}
	group, typ, ok = strings.Cut(rest, ".")
	if !ok {
		return "", "", nil, false
	}
	data, err := wordFS.ReadFile(name)
	if err != nil {
		panic(fmt.Sprintf("harp: read %s: %v", name, err))
	}
	return group, typ, parseList(string(data)), true
}

// assignWordGroup stores words in g's adjective or noun slot by type, reporting
// false for an unrecognized type.
func assignWordGroup(g *wordGroup, typ string, words []string) bool {
	switch typ {
	case typeAdjectives:
		g.adjectives = words
	case typeNouns:
		g.nouns = words
	default:
		return false
	}
	return true
}

// pruneIncompleteGroups drops any group missing adjectives or nouns.
func pruneIncompleteGroups(out map[string]wordGroup) {
	for name, g := range out {
		if len(g.adjectives) == 0 || len(g.nouns) == 0 {
			delete(out, name)
		}
	}
}

// Groups returns the names of all usable word-list groups.
func Groups() []string {
	names := make([]string, 0, len(groups))
	for name := range groups {
		names = append(names, name)
	}
	return names
}

func parseList(data string) []string {
	lines := strings.Split(strings.TrimRight(data, "\n"), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// Options configures name generation. Zero values fall back to defaults
// (3 components, "-" separator, no length cap).
type Options struct {
	// Components is the total number of words in the name
	// (N-1 adjectives + 1 noun). Valid range: MinComponents..MaxComponents.
	// Defaults to 3.
	Components int

	// MaxElementLength caps the length of each individual word in chars.
	// 0 disables the cap.
	MaxElementLength int

	// Separator is the delimiter between words. Defaults to "-".
	Separator string

	// Group selects which word-list group to draw from. Empty or unknown
	// names fall back to DefaultGroup.
	Group string
}

func (o Options) normalize() Options {
	if o.Components < MinComponents {
		o.Components = 3
	}
	if o.Components > MaxComponents {
		o.Components = MaxComponents
	}
	if o.Separator == "" {
		o.Separator = "-"
	}
	if _, ok := groups[o.Group]; !ok {
		o.Group = DefaultGroup
	}
	return o
}

// rngRead is the entropy source. Package-level seam so tests can stub
// without exposing crypto/rand on the public API.
var rngRead = rand.Read

// GenerateName returns a name with default options ("adj-adj-noun").
func GenerateName() string {
	return GenerateNameWithOptions(Options{})
}

// GenerateShortName returns a two-word name ("adj-noun"). Used where the
// identity space can be smaller and a shorter, more uniform id reads better —
// e.g. per-project task ids, which are few and benefit from tidy columns.
// GenerateShortName is a 2-component name (adjective-noun, no verb/predicate
// component) drawn from the "long" word group: 1269 adjectives × 4396 nouns
// = ~5.58M combinations, an 11x wider identity space than drawing the same
// 2 components from DefaultGroup's 443×1102 (~488k) would give — the space
// task harp minting draws from, where the verified downstream consequence of
// a collision is a store where every read fails loud (U111-F03).
func GenerateShortName() string {
	return GenerateNameWithOptions(Options{Components: 2, Group: "long"})
}

// UniqueFrom returns the first id produced by gen that is not already a key of
// used, trying up to 100 times. It is the shared "unique name from a set"
// allocator behind the session index, the project-id registry and the
// per-project task ids, which differ only in which harp generator they pass
// (GenerateName vs GenerateShortName).
//
// On exhaustion — 100 consecutive collisions, astronomically rare for any
// reasonably-sized word group — it returns an error and an empty id. It never
// hands back an unchecked name that may still collide, so a caller cannot
// silently allocate a duplicate by ignoring the failure.
func UniqueFrom(used map[string]struct{}, gen func() string) (string, error) {
	for range 100 {
		id := gen()
		if _, dup := used[id]; !dup {
			return id, nil
		}
	}
	return "", fmt.Errorf("harp: exhausted 100 attempts to generate a name not already in a set of %d used names", len(used))
}

// Validate reports whether name is usable as a harp identifier — the key of a
// session index entry and, via paths.HarpDir, a single FILESYSTEM PATH
// COMPONENT under ~/.ctxloom/sessions/. That second role is what makes this a
// validator rather than a style check: `ctxloom session rename <old> ../..`
// otherwise reached MkdirAll/Symlink on a traversed path (U087-F04).
//
// The rule is deliberately permissive on charset — a harp is renameable to
// whatever a human finds memorable, and existing indexes hold such names — and
// strict only on what stops a name from being one path component: it must be
// non-empty, must not be "." or "..", must contain no path separator (/ or \),
// no volume separator (:), and no control character (NUL included), and must
// not begin or end with whitespace (a name whose edges are invisible cannot be
// typed back by the human who has to address it).
func Validate(name string) error {
	if name == "" {
		return fmt.Errorf("harp name is required")
	}
	if name == "." || name == ".." {
		return fmt.Errorf("invalid harp name %q: names a directory, not a session", name)
	}
	if strings.ContainsAny(name, `/\:`) {
		return fmt.Errorf("invalid harp name %q: must not contain a path separator (/, \\ or :)", name)
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("invalid harp name %q: must not contain control characters", name)
		}
	}
	if strings.TrimSpace(name) != name {
		return fmt.Errorf("invalid harp name %q: must not begin or end with whitespace", name)
	}
	return nil
}

// GenerateNameWithOptions returns a name built per the given options.
// Invalid options are silently clamped (see Options.normalize).
func GenerateNameWithOptions(opts Options) string {
	opts = opts.normalize()
	g := groups[opts.Group]
	parts := make([]string, opts.Components)
	for i := 0; i < opts.Components-1; i++ {
		parts[i] = pickWord(g.adjectives, opts.MaxElementLength)
	}
	parts[opts.Components-1] = pickWord(g.nouns, opts.MaxElementLength)
	return strings.Join(parts, opts.Separator)
}

// pickWord returns a uniformly random entry from words, retrying until
// MaxElementLength is satisfied. The 1000-attempt cap is a safety net
// against pathological inputs (e.g. maxLen smaller than every word).
func pickWord(words []string, maxLen int) string {
	for range 1000 {
		w := words[randIndex(len(words))]
		if maxLen <= 0 || len(w) <= maxLen {
			return w
		}
	}
	return words[0]
}

// randIndex returns a uniform random integer in [0, n). Rejection-sampled
// to avoid the modulo bias that a naive `binary.Uint32 % n` would have for
// non-power-of-two n.
func randIndex(n int) int {
	if n <= 0 {
		return 0
	}
	max := ^uint32(0)
	limit := max - (max % uint32(n))
	var buf [4]byte
	for {
		if _, err := rngRead(buf[:]); err != nil {
			panic(fmt.Sprintf("harp: rng read: %v", err))
		}
		v := binary.BigEndian.Uint32(buf[:])
		if v < limit {
			return int(v % uint32(n))
		}
	}
}
