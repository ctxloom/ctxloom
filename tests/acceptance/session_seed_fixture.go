//go:build acceptance

package acceptance

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/yamlx"
)

// sessionSeed is what a fixture records about a session it plants: the keys
// the store's sidecar carries, spelled as the sidecar spells them. Every
// field is optional; the helper fills ProjectDir from the live project so a
// current-project listing finds the session, which is what every fixture
// here wants. Timestamps are RFC3339 strings, decoded by the store exactly as
// its own writer's output is.
type sessionSeed struct {
	SchemaVersion  int                `yaml:"schema_version,omitempty"`
	SessionID      string             `yaml:"session_id,omitempty"`
	Backend        string             `yaml:"backend,omitempty"`
	ProjectDir     string             `yaml:"project_dir"`
	StartedAt      string             `yaml:"started_at,omitempty"`
	EndedAt        string             `yaml:"ended_at,omitempty"`
	TranscriptPath string             `yaml:"transcript_path,omitempty"`
	EngineVersion  string             `yaml:"engine_version,omitempty"`
	Origin         string             `yaml:"origin,omitempty"`
	Rotations      []sessionSeedEntry `yaml:"rotations,omitempty"`
	OutputDir      string             `yaml:"output_dir,omitempty"`
}

// sessionSidecarSchemaVersion is the generation a planted sidecar is
// stamped with: one the store loads.
const sessionSidecarSchemaVersion = 1

// leftoverSummaryName is the file an older ctxloom's session compaction left
// in a session's output dir. Nothing reads or destroys it now; it stays as a
// note for the human, and every destroyer is asserted to leave it.
const leftoverSummaryName = "essence.md"

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
	if seed.OutputDir == "" {
		seed.OutputDir = defaultOutputDirIn(w, seed.ProjectDir, harp)
	}
	// The store refuses a sidecar with no schema_version, so a planted one
	// carries the stamp its writer would; mergeSessionSidecar leaves it unset
	// and keeps the record's own.
	seed.SchemaVersion = sessionSidecarSchemaVersion
	body, err := yamlx.Marshal(seed)
	if err != nil {
		return fmt.Errorf("seed session %q: %w", harp, err)
	}
	return w.env.WriteHomeFile(".ctxloom/sessions/"+harp+"/"+paths.SessionSidecarFileName, string(body))
}

// defaultOutputDirIn is where a session minted under the scenario's HOME
// records its output dir by default: <HOME>/Documents/ctxloom/<project>/<harp>.
// Spelled out here rather than taken from paths.DefaultOutputBase so a
// fixture does not move with the helper it checks.
func defaultOutputDirIn(w *World, projectDir, harp string) string {
	return filepath.Join(w.env.HomeDir, "Documents", "ctxloom", filepath.Base(projectDir), harp)
}

// outputDirFor is harp's output dir as its record states it — seeded, or
// recorded by a real run — else the default a seed would record.
func outputDirFor(w *World, harp string) string {
	if raw, err := w.env.ReadHomeFile(".ctxloom/sessions/" + harp + "/" + paths.SessionSidecarFileName); err == nil {
		var rec struct {
			OutputDir string `yaml:"output_dir"`
		}
		if yaml.Unmarshal([]byte(raw), &rec) == nil && rec.OutputDir != "" {
			return rec.OutputDir
		}
	}
	return defaultOutputDirIn(w, w.env.ProjectDir, harp)
}

// seededPrompt is the first user prompt a seeded session's transcript
// records — and so the title its listing shows. "Seeded" is the word a
// scenario searches every seeded session by.
func seededPrompt(harp string) string { return "Seeded prompt for " + harp + "." }

// seededTranscriptRel is harp's canonical transcript, home-relative.
func seededTranscriptRel(harp string) string {
	return filepath.Join(".ctxloom", "sessions", harp, "transcripts", "transcript.jsonl")
}

// writeSeededTranscript records harp's canonical transcript: one user prompt
// (seededPrompt), in the canonical record schema the store's readers parse.
// Real bytes, so a purge has something to free.
func writeSeededTranscript(w *World, harp string) error {
	line := fmt.Sprintf(`{"v":1,"harp":%q,"engine":"claude-code","seq":0,"ts":"2026-01-01T00:00:00Z","kind":"entry","entry":{"type":"user","content":%q}}`,
		harp, seededPrompt(harp))
	return w.env.WriteHomeFile(seededTranscriptRel(harp), line+"\n")
}

// backdateSessionDir sets every mtime under harp's session directory to at:
// a session's activity is its directory's newest mtime, so a session that
// ENDED at a recorded time must also look untouched since then, or
// transcript retention reads it as just used.
func backdateSessionDir(w *World, harp string, at time.Time) error {
	dir := filepath.Join(w.env.HomeDir, ".ctxloom", "sessions", harp)
	return filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		return os.Chtimes(p, at, at)
	})
}

// writeOutputFile writes name into harp's output dir.
func writeOutputFile(w *World, harp, name, body string) error {
	p := filepath.Join(outputDirFor(w, harp), filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(body), 0o644)
}
