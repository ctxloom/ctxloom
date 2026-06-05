package agent

import "github.com/spf13/afero"

// CommandExport is the agent-agnostic slash-command export spec for one prompt.
// It is the abstraction the per-agent command writers (claude, gemini) consume,
// so they never import ctxloom's bundle types: ctxloom maps each
// bundles.LoadedContent to a CommandExport for the target agent (resolving that
// agent's enablement + metadata) at the wiring boundary. Fields beyond
// Name/Content/Description are slash-command frontmatter; agents that don't use
// a given field simply ignore it.
type CommandExport struct {
	Name         string   // Full name (bundle/item); path separators allowed
	Content      string   // The command body
	Enabled      bool     // Resolved enablement for the target agent
	Description  string   // For /help display
	ArgumentHint string   // Autocomplete hint (unused by gemini)
	AllowedTools []string // Tool restrictions (unused by gemini)
	Model        string   // Override model (unused by gemini)
}

// CommandFileOption configures command file writing.
type CommandFileOption func(*commandFileOptions)

type commandFileOptions struct {
	fs afero.Fs
}

// WithCommandFS sets the filesystem for command file operations.
func WithCommandFS(fs afero.Fs) CommandFileOption {
	return func(o *commandFileOptions) {
		o.fs = fs
	}
}

// ResolveCommandFS applies the options and returns the filesystem to use,
// defaulting to the OS filesystem. Per-agent command writers (in the claude and
// gemini packages) call this so they can honor WithCommandFS without reaching
// the unexported option struct.
func ResolveCommandFS(opts ...CommandFileOption) afero.Fs {
	options := &commandFileOptions{fs: afero.NewOsFs()}
	for _, opt := range opts {
		opt(options)
	}
	return options.fs
}
