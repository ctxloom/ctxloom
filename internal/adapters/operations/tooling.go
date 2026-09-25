// This file is the companion-loadout → agent-image tooling pipeline. A
// companion whose content needs tools inside the agent container (linters,
// language runtimes, build helpers) declares them in the typed `init.tooling`
// field of its loadout (bundles.InitLoadout); `ctxloom tooling` collects those
// texts THROUGH THE TRUST GATE and emits them with instructions for the LLM to
// fold — with explicit per-change user permission — into the local base
// Containerfile that every locally-built agent image (default auto-build
// included) layers on. Nothing here runs on pull/sync: collection and the
// scaffold are explicit commands, and the edit itself is the LLM's, gated by
// the user.

package operations

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/container"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

// ToolingDeclaration is one companion's collected tooling declaration.
type ToolingDeclaration struct {
	// Source is the companion's ref the text came from, so the user can
	// trace every proposed Containerfile change to its companion.
	Source  string `json:"source"`
	Content string `json:"content"`
}

// CollectTooling gathers every admitted companion's typed `init.tooling`
// declaration. SECURITY: collection goes through the TRUST-GATED pipeline —
// a companion the human rejected is withheld exactly like any other gated
// content (loadout-supplied text driving Containerfile edits is a
// code-execution vector), and the withholding is surfaced content-free.
// Fault-tolerant: a nil config or any load failure returns nil, never errors.
// pipe is a test seam; nil uses the gated exposure pipeline.
func CollectTooling(cfg *config.Config, pipe *bundles.Pipeline) []ToolingDeclaration {
	if cfg == nil {
		return nil
	}
	var gate bundles.Authorizer
	if pipe == nil {
		pipe, gate = exposurePipelineGated(cfg, bundles.LinksUnchecked())
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
	warnWithheld(gate)
	return out
}

// DefaultContainerBasePath is where ScaffoldContainerBase materializes the
// editable base Containerfile, relative to the project root (inside the app
// dir: it is project configuration, versioned with the rest of .ctxloom).
var DefaultContainerBasePath = filepath.Join(paths.AppDirName, "base.Containerfile")

// ScaffoldContainerBase makes the base Containerfile EDITABLE: it materializes
// the embedded default base to relPath (project-root-relative;
// "" = DefaultContainerBasePath), wires `isolation_base_containerfile` in
// config inside one Update transaction, and returns the path — so the
// default auto-build and `container build` pick the file up from then on.
// Idempotent and WIP-safe:
//
//   - config already points at a base Containerfile → returned as-is, nothing
//     written (the user already owns one);
//   - the target file already exists → ADOPTED (config wired to it), its
//     content never overwritten unless force;
//   - otherwise the embedded default base is written, so edits start from
//     exactly what the default build was using.
//
// The already-configured guard reads cfg (an advisory pre-transaction check,
// same shape as SetAgent's shadow-agent warning): the config write below is
// an unconditional field set, not a read-check-then-write, so no lost-update
// window exists for it to close.
func ScaffoldContainerBase(ctx context.Context, app *App, cfg *config.Config, relPath string, force bool) (string, error) {
	if cfg == nil {
		return "", fmt.Errorf("config is required")
	}
	if app == nil {
		return "", fmt.Errorf("app is required")
	}
	fs := getFS(cfg.FS())
	if existing := cfg.IsolationBaseContainerfilePath(); existing != "" && !force {
		return materializeConfiguredBase(fs, existing)
	}
	relPath, abs, err := containerBaseTarget(cfg, relPath)
	if err != nil {
		return "", err
	}
	if err := writeBaseUnlessPresent(fs, abs, force); err != nil {
		return "", err
	}

	if _, err := app.Update(ctx, func(d *config.Draft) error {
		d.IsolationBaseContainerfile = relPath
		return nil
	}); err != nil {
		return "", fmt.Errorf("wire isolation_base_containerfile: %w", err)
	}
	return abs, nil
}

// materializeConfiguredBase returns the already-configured base Containerfile
// path, writing the default base there first when it is configured but not
// actually on disk (deleted, never created, a typo'd path) — rather than
// silently reporting success with nothing written.
func materializeConfiguredBase(fs afero.Fs, existing string) (string, error) {
	if _, err := fs.Stat(existing); err == nil {
		return existing, nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("stat configured base Containerfile: %w", err)
	}
	if err := writeBaseContainerfile(fs, existing); err != nil {
		return "", err
	}
	return existing, nil
}

// containerBaseTarget resolves the scaffold's project-relative path (the
// default when empty) and its contained absolute form.
//
// relPath is documented as project-root-relative, and the CLI exposes it as a
// bare --path flag — untrusted user input. A "../"-laden relPath (or an
// absolute relPath naming anywhere on disk) must not be allowed to write
// outside the project. Contain it the same way safeRepoPath contains
// escalation-query paths: join/clean, reject if the result escapes the root,
// and re-check after symlink resolution for the case where the target
// already exists.
func containerBaseTarget(cfg *config.Config, relPath string) (rel, abs string, err error) {
	if relPath == "" {
		relPath = DefaultContainerBasePath
	}
	abs = relPath
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(cfg.GetAppRoot(), relPath)
	}
	abs, err = containedPath(cfg.GetAppRoot(), abs)
	if err != nil {
		return "", "", fmt.Errorf("base Containerfile path: %w", err)
	}
	return relPath, abs, nil
}

// writeBaseUnlessPresent writes the default base at abs when nothing is
// there yet, or unconditionally under force.
func writeBaseUnlessPresent(fs afero.Fs, abs string, force bool) error {
	exists := false
	if _, err := fs.Stat(abs); err == nil {
		exists = true
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat base Containerfile: %w", err)
	}
	if exists && !force {
		return nil
	}
	return writeBaseContainerfile(fs, abs)
}

// writeBaseContainerfile writes the embedded default base to path, creating
// its directory. No AllowEmpty: container.Base() is a fixed embedded
// template, never empty.
func writeBaseContainerfile(fs afero.Fs, path string) error {
	if merr := fs.MkdirAll(filepath.Dir(path), 0o755); merr != nil {
		return fmt.Errorf("create base Containerfile directory: %w", merr)
	}
	if werr := iox.WriteFileAtomicFs(fs, path, container.Base(), 0o644); werr != nil {
		return fmt.Errorf("write base Containerfile: %w", werr)
	}
	return nil
}

// containedPath validates that target is contained within root, rejecting a
// syntactic escape ("../../x") that survives filepath.Clean and, separately,
// an already-absolute target that simply names a path outside root. Mirrors
// safeRepoPath's (task_triggers_query.go) symlink-aware re-check: a
// not-yet-created target is fine — callers are about to create it — but a
// symlink already planted inside root that points outside it is still
// rejected. Returns the resolved absolute path on success.
func containedPath(root, target string) (string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	if !withinDir(absRoot, absTarget) {
		return "", fmt.Errorf("path %q escapes the project root %q", target, absRoot)
	}
	resolved, err := filepath.EvalSymlinks(absTarget)
	if err != nil {
		if !os.IsNotExist(err) {
			// A genuine resolution failure (permissions, a symlink loop) never
			// answered the containment question — do not let it through as if
			// it had been checked.
			return "", err
		}
		// Doesn't exist yet — not an escape, just nothing to resolve; the
		// syntactic Join+Clean check above already covers this case.
		return absTarget, nil
	}
	resolvedAbs, err := filepath.Abs(resolved)
	if err != nil {
		return "", err
	}
	if !withinDir(absRoot, resolvedAbs) {
		return "", fmt.Errorf("path %q escapes the project root %q via a symlink", target, absRoot)
	}
	return resolvedAbs, nil
}
