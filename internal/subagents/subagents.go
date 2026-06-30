// Package subagents implements the LOCAL-ONLY subagent entity: a named binding
// of an LLM engine to one or more composed profiles.
//
// A subagent is end-user/local configuration — defined SOLELY in the user's
// .ctxloom, via the `subagents:` config key and/or .ctxloom/subagents/<name>.yaml.
// It is NEVER a bundle item kind, NEVER remote-distributed: there is no
// Bundle.Subagents, no "#subagents/" ref, and no remote/pull path. Engine/model
// assignment is a user/cost/environment decision, not an author's, so it travels
// with the project, not with shippable content.
//
// The subagent DEFINITION is also UNGATED orchestration/config: there is no
// trust.ItemKind for subagents, they are never baselined, and they never pass
// through EffectiveTrust. (Their constituent profiles' fragments/skills/mcp/hooks
// still gate when the composed context is assembled/applied — but the binding
// itself is not a trust-addressable surface.)
//
// This package owns only the entity type and the directory reader. Resolution
// (composing the profiles into one context and applying the engine override)
// lives in internal/operations, which has the profile loader and backend
// selection; the config-key source and the two-source merge live in
// internal/config, which owns config.yaml.
package subagents

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/afero"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// SourceConfig is the Subagent.Source sentinel for a subagent declared under the
// `subagents:` key of config.yaml (as opposed to a .ctxloom/subagents/*.yaml
// file, whose Source is the file path).
const SourceConfig = "config"

// Subagent is a named, LOCAL-only binding of an engine to a set of profiles.
//
//   - Engine is the LLM config label / backend selection hoisted to the
//     subagent. It OVERRIDES the constituent profiles' own llm:. Optional; an
//     empty engine falls back to the composed profiles' llm and finally to the
//     project default backend (resolution lives in operations.ResolveSubagent).
//   - Profiles are one or more profiles composed into ONE assembled context
//     (mirroring profile-parent merge: later wins / union). Members may be local,
//     top-level remote, or bundle profiles ("<bundle>#profiles/<name>") — all
//     resolve through the shared profile loader.
type Subagent struct {
	// Name is the binding's name. It is the map key (config source) or the
	// file's base name (directory source), never encoded in the body.
	Name string `yaml:"-"`
	// Source records where the definition came from: SourceConfig for the
	// config.yaml `subagents:` key, otherwise the .yaml file path. Diagnostic
	// only (never serialized), the subagent-side mirror of profiles.Profile.Path.
	Source string `yaml:"-"`

	// Engine is the LLM config label/backend; overrides the profiles' llm.
	Engine string `yaml:"engine,omitempty"`
	// Profiles compose into one assembled context.
	Profiles []string `yaml:"profiles,omitempty"`
}

// ParseSubagent unmarshals subagent YAML into a Subagent. Name and Source are
// not set (they are not encoded in the file); callers assign them.
func ParseSubagent(data []byte) (*Subagent, error) {
	var s Subagent
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("invalid YAML: %w", err)
	}
	return &s, nil
}

// Loader reads subagent definitions from .ctxloom/subagents directories. It is
// the directory-source half; the config-key source and the merge live in
// internal/config. Deliberately simpler than profiles.Loader: subagents are pure
// local config with no remote seeding, schema upgrades, or ref canonicalization.
type Loader struct {
	dirs []string
	fs   afero.Fs
}

// LoaderOption configures a Loader.
type LoaderOption func(*Loader)

// WithFS sets a custom filesystem implementation (for testing).
func WithFS(fs afero.Fs) LoaderOption {
	return func(l *Loader) { l.fs = fs }
}

// NewLoader creates a subagent directory loader.
func NewLoader(dirs []string, opts ...LoaderOption) *Loader {
	l := &Loader{dirs: dirs, fs: afero.NewOsFs()}
	for _, opt := range opts {
		opt(l)
	}
	return l
}

// List returns every subagent defined under the loader's directories, sorted by
// name. Per ctxloom's fault-tolerance philosophy a file that fails to parse is a
// stderr warning and is skipped — one bad definition never sinks the rest. The
// first directory wins on a name collision.
func (l *Loader) List() ([]*Subagent, error) {
	var out []*Subagent
	seen := make(map[string]bool)

	for _, dir := range l.dirs {
		exists, err := afero.DirExists(l.fs, dir)
		if err != nil || !exists {
			continue
		}
		err = afero.Walk(l.fs, dir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil // skip unreadable entries / directories
			}
			name := info.Name()
			if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
				return nil
			}
			rel, _ := filepath.Rel(dir, path)
			subName := filepath.ToSlash(strings.TrimSuffix(strings.TrimSuffix(rel, ".yaml"), ".yml"))
			if seen[subName] {
				return nil
			}
			seen[subName] = true
			sub, lerr := l.loadFile(path)
			if lerr != nil {
				// Degrade, but say so: a corrupt subagent silently vanishing is
				// undiagnosable (CLAUDE.md fault tolerance).
				clidiag.Warn("ctxloom", "skipping subagent %s: %v", path, lerr)
				return nil
			}
			sub.Name = subName
			out = append(out, sub)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("failed to scan subagents directory %s: %w", dir, err)
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (l *Loader) loadFile(path string) (*Subagent, error) {
	data, err := afero.ReadFile(l.fs, path)
	if err != nil {
		return nil, err
	}
	sub, err := ParseSubagent(data)
	if err != nil {
		return nil, err
	}
	sub.Source = path
	return sub, nil
}

// GetSubagentDirs returns the existing subagent directories for the given
// ctxloom paths. Mirrors profiles.GetProfileDirs: it stats the real filesystem,
// so only directories that exist on disk are returned.
func GetSubagentDirs(scmPaths []string) []string {
	var dirs []string
	for _, scmPath := range scmPaths {
		dir := paths.SubagentsPath(scmPath)
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			dirs = append(dirs, dir)
		}
	}
	return dirs
}
