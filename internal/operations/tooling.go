// This file is the trusted-bundle → agent-image tooling pipeline. A bundle
// whose content needs tools inside the agent container (linters, language
// runtimes, build helpers) ships a well-known `tooling` command
// describing them; `ctxloom tooling` collects those texts THROUGH
// THE TRUST GATE and emits them with instructions for the LLM to fold — with
// explicit per-change user permission — into the local base Containerfile
// that every locally-built agent image (default auto-build included) layers
// on. Nothing here runs on pull/sync: collection and the scaffold are
// explicit commands, and the edit itself is the LLM's, gated by the user.

package operations

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ctxloom/ctxloom/container"
	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
)

// ToolingCommandName is the well-known command name a bundle ships to declare
// the tools its content needs available where agents run — today that means
// the agent container image. Matching mirrors the agent-setup override
// contract: the bare name, or the last path/# segment
// (`<bundle>#commands/tooling`).
const ToolingCommandName = "tooling"

// ToolingDeclaration is one bundle's collected tooling declaration.
type ToolingDeclaration struct {
	// Source is the bundle-qualified command name the text came from, so the
	// user can trace every proposed Containerfile change to its bundle.
	Source  string `json:"source"`
	Content string `json:"content"`
}

// CollectTooling gathers every trusted bundle's `tooling`
// command. SECURITY: collection goes through the TRUST-GATED loader — a
// command from an unreviewed/untrusted bundle is withheld exactly like any
// other gated content (bundle-supplied text driving Containerfile edits is a
// code-execution vector), and the withholding is surfaced content-free.
// Fault-tolerant: a nil config or any load failure returns nil, never errors.
// loader is a test seam; nil uses the gated exposure loader.
func CollectTooling(cfg *config.Config, loader *bundles.Loader) []ToolingDeclaration {
	if cfg == nil {
		return nil
	}
	var gate *contentGate
	if loader == nil {
		loader, gate = exposureLoaderGated(cfg)
	}
	if loader == nil {
		return nil
	}
	infos, err := loader.ListAllCommands()
	if err != nil {
		return nil
	}
	var out []ToolingDeclaration
	for _, info := range infos {
		if !setupCommandNameMatches(info.Name, ToolingCommandName) {
			continue
		}
		// Fetch by BUNDLE-QUALIFIED ref: many bundles ship a command with this
		// same well-known name, and a bare-name fetch would resolve every one
		// of them to the first match. The ref also gives the user a traceable
		// source per declaration.
		ref := info.Bundle + "#commands/" + info.Name
		content, gerr := loader.GetCommand(ref)
		if gerr != nil || strings.TrimSpace(content.Content) == "" {
			continue // withheld by the gate, or empty — skip, never block
		}
		out = append(out, ToolingDeclaration{Source: ref, Content: content.Content})
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
// config inside one Manager.Update transaction, and returns the path — so the
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
func ScaffoldContainerBase(mgr *config.Manager, cfg *config.Config, relPath string, force bool) (string, error) {
	if cfg == nil {
		return "", fmt.Errorf("config is required")
	}
	if mgr == nil {
		return "", fmt.Errorf("manager is required")
	}
	if existing := cfg.IsolationBaseContainerfilePath(); existing != "" && !force {
		return existing, nil
	}
	if relPath == "" {
		relPath = DefaultContainerBasePath
	}
	abs := relPath
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(cfg.GetAppRoot(), relPath)
	}

	if _, err := os.Stat(abs); os.IsNotExist(err) || force {
		if werr := os.WriteFile(abs, container.Base, 0o644); werr != nil {
			return "", fmt.Errorf("write base Containerfile: %w", werr)
		}
	}

	if err := mgr.Update(func(d *config.Draft) error {
		d.IsolationBaseContainerfile = relPath
		return nil
	}); err != nil {
		return "", fmt.Errorf("wire isolation_base_containerfile: %w", err)
	}
	return abs, nil
}
