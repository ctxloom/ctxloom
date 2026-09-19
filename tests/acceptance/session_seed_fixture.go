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
