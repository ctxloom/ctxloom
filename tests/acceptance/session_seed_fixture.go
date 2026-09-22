//go:build acceptance

package acceptance

import (
	"fmt"

	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// sessionSeed is what a fixture records about a session it plants: the keys
// the store's sidecar carries, spelled as the sidecar spells them. Every
// field is optional; the helper fills ProjectDir from the live project so a
// current-project listing finds the session, which is what every fixture
// here wants. Timestamps are RFC3339 strings, decoded by the store exactly as
// its own writer's output is.
type sessionSeed struct {
	SessionID      string             `yaml:"session_id,omitempty"`
	Backend        string             `yaml:"backend,omitempty"`
	ProjectDir     string             `yaml:"project_dir"`
	StartedAt      string             `yaml:"started_at,omitempty"`
	EndedAt        string             `yaml:"ended_at,omitempty"`
	TranscriptPath string             `yaml:"transcript_path,omitempty"`
	EngineVersion  string             `yaml:"engine_version,omitempty"`
	Rotations      []sessionSeedEntry `yaml:"rotations,omitempty"`
}

// sessionSeedEntry is one prior binding of a seeded session, in the sidecar's
// own rotation shape.
type sessionSeedEntry struct {
	SessionID      string `yaml:"session_id"`
	TranscriptPath string `yaml:"transcript_path,omitempty"`
	RotatedAt      string `yaml:"rotated_at"`
}

// seedSessionSidecar makes harp exist as a recorded session by writing its
// sidecar — <home>/.ctxloom/sessions/<harp>/session.yaml — in the store's
// live on-disk shape. The session store is the set of session directories
// plus their sidecar (sessions.IsSessionDir is the predicate every listing
// answers through), so this is the ONE way a fixture plants a session; there
// is no global index to append to, and a scenario may re-seed a harp after a
// command has run by calling this again.
func seedSessionSidecar(w *World, harp string, seed sessionSeed) error {
	if seed.ProjectDir == "" {
		seed.ProjectDir = w.env.ProjectDir
	}
	body, err := yaml.Marshal(seed)
	if err != nil {
		return fmt.Errorf("seed session %q: %w", harp, err)
	}
	return w.env.WriteHomeFile(".ctxloom/sessions/"+harp+"/"+paths.SessionSidecarFileName, string(body))
}

// mergeSessionSidecar overlays seed's SET fields onto harp's existing
// sidecar and leaves every other key as the store wrote it. It exists for a
// fixture that seeds history under a LIVE session — the standing owner's,
// whose record carries the MCP endpoint its runner serves and whatever else
// the launch bound — where a rewrite from the seed alone would erase the
// session the scenario is about.
func mergeSessionSidecar(w *World, harp string, seed sessionSeed) error {
	rel := ".ctxloom/sessions/" + harp + "/" + paths.SessionSidecarFileName
	existing, err := w.env.ReadHomeFile(rel)
	if err != nil {
		return fmt.Errorf("merge into session %q: read its record: %w", harp, err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(existing), &doc); err != nil {
		return fmt.Errorf("merge into session %q: parse its record: %w", harp, err)
	}
	overlay, err := yaml.Marshal(seed)
	if err != nil {
		return fmt.Errorf("merge into session %q: %w", harp, err)
	}
	var fields map[string]any
	if err := yaml.Unmarshal(overlay, &fields); err != nil {
		return err
	}
	if doc == nil {
		doc = map[string]any{}
	}
	for k, v := range fields {
		doc[k] = v
	}
	body, err := yaml.Marshal(doc)
	if err != nil {
		return err
	}
	return w.env.WriteHomeFile(rel, string(body))
}
