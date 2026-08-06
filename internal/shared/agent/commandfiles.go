package agent

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/ledger"
	"github.com/spf13/afero"
)

// CommandExport is the agent-agnostic slash-command export spec for one command.
// It is the abstraction the per-agent command writers (claude, antigravity) consume,
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
	ArgumentHint string   // Autocomplete hint (unused by antigravity)
	AllowedTools []string // Tool restrictions (unused by antigravity)
	Model        string   // Override model (unused by antigravity)
}

// CommandFileOption configures command file writing.
type CommandFileOption func(*commandFileOptions)

type commandFileOptions struct {
	fs              afero.Fs
	homeCommandsDir string
}

// WithCommandFS sets the filesystem for command file operations.
func WithCommandFS(fs afero.Fs) CommandFileOption {
	return func(o *commandFileOptions) {
		o.fs = fs
	}
}

// WithHomeCommandsDir names the user-global command directory the target agent
// also loads alongside the project scope (Claude Code: ~/.claude/commands). The
// per-agent writer forwards it to WriteManagedCommandFiles as WithDedupHomeDir so
// a project copy byte-identical to a global one is skipped rather than shipped as
// a duplicate slash-command. Empty (the default) disables the dedup.
func WithHomeCommandsDir(dir string) CommandFileOption {
	return func(o *commandFileOptions) {
		o.homeCommandsDir = dir
	}
}

// ResolveHomeCommandsDir applies the options and returns the configured global
// command directory to dedup against, or "" when none was set. The per-agent
// writers call this to bridge a WithHomeCommandsDir CommandFileOption into a
// WithDedupHomeDir ManagedWriteOption.
func ResolveHomeCommandsDir(opts ...CommandFileOption) string {
	options := &commandFileOptions{}
	for _, opt := range opts {
		opt(options)
	}
	return options.homeCommandsDir
}

// SafeCommandRelPath validates name as a relative path confined to dir and
// returns the cleaned joined path. Command/skill names and manifest lines can
// originate in bundle content (potentially remote), so the per-agent writers
// must never join them into their managed directory blindly: a "../x" name
// escapes the tree on write, and a malicious manifest line deletes files
// outside the tree on cleanup. Rejected (ok == false): empty names, absolute
// paths, any ".." path element, and any join whose result escapes dir.
// Subdirectory names without traversal ("group/cmd") pass.
func SafeCommandRelPath(dir, name string) (string, bool) {
	if name == "" || filepath.IsAbs(name) || filepath.IsAbs(filepath.FromSlash(name)) {
		return "", false
	}
	for part := range strings.SplitSeq(filepath.ToSlash(name), "/") {
		if part == ".." {
			return "", false
		}
	}
	joined := filepath.Join(dir, filepath.FromSlash(name))
	// Belt and braces: verify the cleaned join really stays under dir.
	rel, err := filepath.Rel(dir, joined)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return joined, true
}

// ResolveCommandFS applies the options and returns the filesystem to use,
// defaulting to the OS filesystem. Per-agent command writers (in the claude and
// antigravity packages) call this so they can honor WithCommandFS without reaching
// the unexported option struct.
func ResolveCommandFS(opts ...CommandFileOption) afero.Fs {
	options := &commandFileOptions{fs: afero.NewOsFs()}
	for _, opt := range opts {
		opt(options)
	}
	return options.fs
}

// ManagedWriteOption configures WriteManagedCommandFiles.
type ManagedWriteOption func(*managedWriteOptions)

type managedWriteOptions struct {
	dedupHomeDir string
}

// WithDedupHomeDir names a user-global command directory the agent also loads
// alongside dir. When set and distinct from dir, a file byte-identical to the
// same-named one already in this dir is skipped (not written, not tracked in the
// manifest), so a "home/global wins" copy isn't duplicated into the project
// scope. Only byte-identical files are skipped — a divergent file is still
// written so version skew is never silently hidden. Empty disables the dedup.
func WithDedupHomeDir(dir string) ManagedWriteOption {
	return func(o *managedWriteOptions) { o.dedupHomeDir = dir }
}

// WriteManagedCommandFiles is the manifest-scoped slash-command/skill file
// writer shared by the per-agent command writers (claude, codex, antigravity).
// dir is shared territory with user-authored files, so it is never wiped
// wholesale: ctxloom tracks the files it wrote in the shared managed-content
// ledger under the commands surface
// and removes exactly that set before writing the current one, so the written
// set always mirrors the enabled exports.
//
// render maps one enabled export to its file: the path relative to dir plus
// the file content. Both command names and manifest lines originate in bundle
// content (potentially remote), so every name and rendered path is validated
// with SafeCommandRelPath — traversal/absolute names are skipped with a
// warning on write and never followed on cleanup.
//
// dir itself is only created when at least one file is written, and the
// manifest is only (re)written when at least one file was written; with
// nothing to write the previous manifest-tracked set and manifest are simply
// removed.
//
// A command is the degenerate case of a package with exactly one file, so this
// is now a THIN ADAPTER over WriteManagedPackageFiles (packagefiles.go) — the
// general tree writer a skill package delivery reuses. Every command file is
// written at mode 0644 (PackageFile{}.Mode's zero-value default), matching
// this function's historical hardcoded mode, so existing callers see
// byte-identical output.
func WriteManagedCommandFiles(fs afero.Fs, dir string, cmds []CommandExport, render func(CommandExport) (relPath string, content []byte, err error), opts ...ManagedWriteOption) error {
	return WriteManagedPackageFiles(fs, dir, ledger.SurfaceCommands, cmds,
		func(c CommandExport) bool { return c.Enabled },
		func(c CommandExport) string { return c.Name },
		func(c CommandExport) ([]PackageFile, error) {
			relPath, content, err := render(c)
			if err != nil {
				return nil, err
			}
			return []PackageFile{{RelPath: relPath, Content: content}}, nil
		},
		opts...,
	)
}

// mustacheVarRe matches {{variable}} placeholders in command bodies.
var mustacheVarRe = regexp.MustCompile(`\{\{(\w+)\}\}`)

// TransformMustacheToPositional replaces {{variable}} patterns with $1, $2,
// etc. Variables are assigned positions by first occurrence order. This is the
// argument transform shared by the claude and codex command renderers (both
// CLIs use positional $N prompt arguments).
func TransformMustacheToPositional(content string) string {
	varNum := 1
	seen := make(map[string]int)

	return mustacheVarRe.ReplaceAllStringFunc(content, func(match string) string {
		varName := mustacheVarRe.FindStringSubmatch(match)[1]
		if num, exists := seen[varName]; exists {
			return fmt.Sprintf("$%d", num)
		}
		seen[varName] = varNum
		num := varNum
		varNum++
		return fmt.Sprintf("$%d", num)
	})
}

// yamlNumberRe matches values a typing YAML parser would read as a number
// (int/float, optional sign and exponent) rather than a string.
var yamlNumberRe = regexp.MustCompile(`^[+-]?(\d+(\.\d*)?|\.\d+)([eE][+-]?\d+)?$`)

// isYAMLTypeAmbiguous reports whether s would be typed as a non-string scalar
// (bool/null/number) by a strict YAML parser if emitted unquoted. Covers the
// YAML 1.1 boolean/null literals (true/false/null/yes/no/on/off and ~, any
// case) and numeric scalars, so a Description/hint like "null" or "123" stays a
// string instead of becoming nil/int.
func isYAMLTypeAmbiguous(s string) bool {
	switch strings.ToLower(s) {
	case "true", "false", "null", "yes", "no", "on", "off", "~":
		return true
	}
	return yamlNumberRe.MatchString(s)
}

// hasControlChar reports whether s carries a character that cannot appear
// literally in a YAML scalar. A newline line-FOLDS to a space inside a
// double-quoted scalar (silently changing the value), and a bare carriage
// return breaks the parse outright — both must be escaped, not merely quoted.
func hasControlChar(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f })
}

// EscapeYAMLString quotes a string for safe inclusion in YAML frontmatter when
// it contains special or control characters, when it would otherwise be typed
// as a non-string scalar (bool/null/number), or when it begins with a YAML
// indicator character. A value needing no quotes is emitted bare, which is the
// deliberate difference from the always-quoting yamlDoubleQuoted — but once the
// decision to quote is made, the ESCAPING is yamlDoubleQuoted's, so this
// package has one escaping algorithm rather than two: bespoke backslash/quote
// rules here escape neither \n nor \r, so a description carrying either is
// silently folded or makes the frontmatter unparseable.
func EscapeYAMLString(s string) string {
	needsQuotes := strings.ContainsAny(s, ":#{}[]&*!|>'\"%@`") ||
		strings.HasPrefix(s, " ") ||
		strings.HasSuffix(s, " ") ||
		hasControlChar(s) ||
		strings.HasPrefix(s, "- ") ||
		strings.HasPrefix(s, "? ") ||
		s == "-" || s == "?" ||
		isYAMLTypeAmbiguous(s)
	if needsQuotes {
		return yamlDoubleQuoted(s)
	}
	return s
}
