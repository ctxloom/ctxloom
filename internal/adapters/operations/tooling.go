// This file is the companion-loadout → agent-image tooling pipeline. A
// companion whose content needs tools inside the agent container (linters,
// language runtimes, build helpers) declares them in the typed `init.tooling`
// field of its loadout (bundles.InitLoadout); `ctxloom container tooling`
// collects those texts from REGISTERED companions only (an unregistered one is
// never run) and emits them with instructions for the LLM to
// fold — with explicit per-change user permission — into the agent image's
// base: the project devcontainer's Dockerfile, which an unset isolation_base
// builds every locally-built agent image on. Nothing here runs on pull/sync:
// collection and the scaffold are explicit commands, and the edit itself is
// the LLM's, gated by the user.

package operations

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/ctxloom/ctxloom/container"
	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/spf13/afero"
)

// ToolingDeclaration is one companion's collected tooling declaration.
type ToolingDeclaration struct {
	// Source is the companion's ref the text came from, so the user can
	// trace every proposed base-image change to its companion.
	Source  string `json:"source"`
	Content string `json:"content"`
}

// CollectTooling gathers every registered companion's typed `init.tooling`
// declaration. Only a companion the user registered (companion add) is run
// for a loadout, so only registered companions contribute.
// Fault-tolerant: a nil config or any load failure returns nil, never errors.
// pipe is a test seam; nil uses the exposure pipeline.
func CollectTooling(cfg *config.Config, pipe *bundles.Pipeline) []ToolingDeclaration {
	if cfg == nil {
		return nil
	}
	if pipe == nil {
		pipe = exposurePipeline(cfg)
	}
	if pipe == nil {
		return nil
	}
	var out []ToolingDeclaration
	for _, admitted := range pipe.InitLoadouts() {
		if admitted.Init.Tooling == "" {
			continue
		}
		out = append(out, ToolingDeclaration{Source: admitted.Ref, Content: admitted.Init.Tooling})
	}
	return out
}

// devcontainerDirName is the directory ScaffoldDevcontainer writes under the
// project root — the devcontainer spec's canonical location, and one of the
// two isolation.FindDevcontainerJSON looks in.
const devcontainerDirName = ".devcontainer"

// devcontainerJSON is the scaffolded devcontainer.json: build the Dockerfile
// beside it. Nothing else — the human's editor fills in the rest.
const devcontainerJSON = `{
  "build": {
    "dockerfile": "Dockerfile"
  }
}
`

// DevcontainerExistsError refuses a scaffold over a project that already has
// a devcontainer: it is the human's environment, and the agent image already
// builds from it.
type DevcontainerExistsError struct {
	// Path is the existing .devcontainer/ directory or devcontainer.json.
	Path string
}

func (e *DevcontainerExistsError) Error() string {
	return fmt.Sprintf("this project already has a devcontainer (%s); edit it rather than scaffolding a new one — the agent image builds from it unless isolation_base says otherwise", e.Path)
}

// ScaffoldDevcontainer writes the project devcontainer — .devcontainer/
// holding a devcontainer.json that builds the Dockerfile beside it, seeded
// from the embedded default base — and returns the directory. It refuses with
// *DevcontainerExistsError when the project already has a .devcontainer/
// (anything at that path, so nothing of the human's is overwritten) or a
// devcontainer.json at either canonical path. It writes no config: an unset
// isolation_base (or isolation_base: devcontainer) adopts what it wrote.
func ScaffoldDevcontainer(cfg *config.Config) (string, error) {
	if cfg == nil {
		return "", fmt.Errorf("config is required")
	}
	fs := getFS(cfg.FS())
	root := cfg.GetAppRoot()
	dir := filepath.Join(root, devcontainerDirName)
	if _, err := lstatIfPossible(fs, dir); err == nil { // a planted symlink counts as present
		return "", &DevcontainerExistsError{Path: dir}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("stat %s: %w", dir, err)
	}
	if existing := isolation.FindDevcontainerJSON(root); existing != "" {
		return "", &DevcontainerExistsError{Path: existing}
	}
	if err := fs.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	if err := safefs.WriteFile(fs, filepath.Join(dir, "Dockerfile"), container.Base(), 0o644); err != nil {
		return "", fmt.Errorf("write devcontainer Dockerfile: %w", err)
	}
	if err := safefs.WriteFile(fs, filepath.Join(dir, "devcontainer.json"), []byte(devcontainerJSON), 0o644); err != nil {
		return "", fmt.Errorf("write devcontainer.json: %w", err)
	}
	return dir, nil
}

// lstatIfPossible stats path WITHOUT following a final symlink where the
// filesystem supports it (afero.OsFs does; MemMapFs has no symlinks to
// confuse), so a symlink is reported as itself rather than as its target.
func lstatIfPossible(fs afero.Fs, path string) (os.FileInfo, error) {
	if lstater, ok := fs.(afero.Lstater); ok {
		info, _, err := lstater.LstatIfPossible(path)
		return info, err
	}
	return fs.Stat(path)
}
