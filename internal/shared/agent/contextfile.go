package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/collections"
)

const (
	// SCMContextDir is the directory for ctxloom-managed files
	SCMContextDir = ".ctxloom"
	// SCMContextSubdir is the subdirectory for context files (in cache)
	SCMContextSubdir = ".ctxloom/cache/context"
	// SCMContextFileEnv is the environment variable containing the context file path
	SCMContextFileEnv = "CTXLOOM_CONTEXT_FILE"
	// SCMFramedContextSuffix names the framed (system-prompt-ready) sibling of the
	// raw <hash>.md context file — the whole-file form claude loads via
	// --append-system-prompt-file, with the ctxloom framing baked in.
	SCMFramedContextSuffix = ".sysprompt.md"

	// MaxRecommendedContextSize is the threshold above which warnings are emitted.
	MaxRecommendedContextSize = 16 * 1024 // 16KB (~4,000 tokens)

	// WarnContextSizeExceeded is the warning format for oversized context.
	WarnContextSizeExceeded = "ctxloom: warning: assembled context is %dKB (recommended max: 16KB)\n"
	// WarnContextEffectiveness is the follow-up warning about LLM effectiveness.
	WarnContextEffectiveness = "ctxloom: warning: large context may reduce LLM effectiveness; consider distillation or fewer fragments\n"
)

// contextFileOptions holds configuration for context file operations.
type contextFileOptions struct {
	fs     afero.Fs
	stderr io.Writer
}

// ContextFileOption is a functional option for context file operations.
type ContextFileOption func(*contextFileOptions)

// WithContextFS sets the filesystem to use for context file operations.
// If not provided, the real OS filesystem is used.
func WithContextFS(fs afero.Fs) ContextFileOption {
	return func(o *contextFileOptions) {
		o.fs = fs
	}
}

// WithContextStderr sets the writer for warning messages.
// If not provided, os.Stderr is used.
func WithContextStderr(w io.Writer) ContextFileOption {
	return func(o *contextFileOptions) {
		o.stderr = w
	}
}

// applyContextOptions applies the given options and returns the configured options.
func applyContextOptions(opts []ContextFileOption) *contextFileOptions {
	options := &contextFileOptions{
		fs:     afero.NewOsFs(), // default to real filesystem
		stderr: os.Stderr,       // default to real stderr
	}
	for _, opt := range opts {
		opt(options)
	}
	return options
}

// assembleDedupedContext joins the fragment contents into the single context
// string ctxloom delivers, deduplicating by content hash (so the same fragment
// reached through multiple bundles/paths appears once) and emitting the oversize
// warning. It is the assembly half of WriteContextFile, factored out so a
// delivery strategy can obtain the exact string WriteContextFile would frame —
// WITHOUT writing the raw cache file into the project tree. Returns "" when
// there is no content. It differs from the simpler exported AssembleContext (no
// dedup, no warning) whose output the raw context file must NOT diverge from.
// Use WithContextStderr to redirect the warning in tests.
func assembleDedupedContext(fragments []*Fragment, opts ...ContextFileOption) string {
	options := applyContextOptions(opts)

	// Assemble the context content, deduplicating by content hash.
	// This prevents duplicate content even when the same fragment exists
	// in multiple bundles or is referenced through different paths.
	var parts []string
	seenContent := collections.NewSet[string]()
	for _, f := range fragments {
		if f.Content == "" {
			continue
		}
		content := strings.TrimSpace(f.Content)
		// Compute hash of content to detect duplicates
		h := sha256.Sum256([]byte(content))
		contentHash := hex.EncodeToString(h[:])
		if seenContent.Has(contentHash) {
			continue
		}
		seenContent.Add(contentHash)
		parts = append(parts, content)
	}

	if len(parts) == 0 {
		return ""
	}

	content := strings.Join(parts, contextSectionSep)

	// Warn if context exceeds recommended size threshold.
	//
	// Research on LLM context effectiveness:
	// - "Context Rot" (Chroma, 2025): Performance degrades continuously as input grows,
	//   with accuracy highest for early tokens. https://trychroma.com/research/context-rot
	// - "Maximum Effective Context Window" (arXiv:2509.21361): Most models show severe
	//   degradation by 1,000 tokens; all fall far short of advertised windows.
	// - "Lost in the Middle" (Liu et al., 2023, arXiv:2307.03172): U-shaped performance
	//   curve; >30% degradation for middle-positioned content vs start/end.
	//
	// 16KB (~4,000 tokens) is a conservative threshold where degradation becomes
	// noticeable across most models. Structure and relevance matter more than size.
	if len(content) > MaxRecommendedContextSize {
		_, _ = fmt.Fprintf(options.stderr, WarnContextSizeExceeded, len(content)/1024)
		_, _ = fmt.Fprint(options.stderr, WarnContextEffectiveness)
	}

	return content
}

// ErrNoContext reports that fragments were supplied but assembled to zero
// bytes, so no context file was written. It is deliberately distinct from the
// no-fragments case: a session configured with NO context legitimately writes
// nothing, but a session whose fragments all resolved empty asked for context
// and silently got none. Callers that genuinely tolerate an empty assembly can
// check errors.Is(err, ErrNoContext) and continue; the point is that they have
// to say so.
var ErrNoContext = errors.New("fragments assembled to no context")

// WriteContextFile writes the assembled context to a hashed filename in .ctxloom/context/.
// Returns the hash (used as filename without .md extension).
// workDir is the directory where the .ctxloom/ directory exists.
// Use WithContextFS to provide a custom filesystem for testing.
//
// With no fragments at all it writes nothing and returns ("", nil) — there was
// nothing to deliver. With fragments that assemble to nothing it returns
// ErrNoContext rather than a silent success, because "the user configured no
// context" and "every fragment the user configured resolved to nothing" are
// different facts and only one of them is fine.
func WriteContextFile(workDir string, fragments []*Fragment, opts ...ContextFileOption) (string, error) {
	options := applyContextOptions(opts)
	fs := options.fs

	content := assembleDedupedContext(fragments, opts...)
	if content == "" {
		if len(fragments) == 0 {
			// Nothing was asked for; nothing to write.
			return "", nil
		}
		return "", fmt.Errorf("%w: %d fragment(s) produced zero bytes", ErrNoContext, len(fragments))
	}

	// Generate hash-based filename from content
	hash := sha256.Sum256([]byte(content))
	hashStr := hex.EncodeToString(hash[:8]) // First 8 bytes = 16 hex chars

	// Ensure .ctxloom/context directory exists
	contextDir := filepath.Join(workDir, SCMContextSubdir)
	if err := fs.MkdirAll(contextDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create %s directory: %w", SCMContextSubdir, err)
	}

	// Write context file
	contextPath := filepath.Join(contextDir, hashStr+".md")
	if err := afero.WriteFile(fs, contextPath, []byte(content), 0644); err != nil {
		return "", fmt.Errorf("failed to write context file: %w", err)
	}

	return hashStr, nil
}

// ReadContextFile reads the context file for the given hash from .ctxloom/context/[hash].md.
// Use WithContextFS to provide a custom filesystem for testing.
//
// A MISSING file is an error wrapping os.ErrNotExist, not ("", nil). Every
// caller reaches here holding a content hash, which only exists because the
// file was written — so its absence (reaped cache, wrong workDir, cleaned
// tree) is a real fact, and collapsing it into "no context" made an
// undelivered context indistinguishable from a session that was configured
// with none. Callers that legitimately tolerate absence keep doing so by
// warning and continuing with empty content; they simply now know.
func ReadContextFile(workDir, hash string, opts ...ContextFileOption) (string, error) {
	options := applyContextOptions(opts)
	fs := options.fs

	contextPath := filepath.Join(workDir, SCMContextSubdir, hash+".md")
	content, err := afero.ReadFile(fs, contextPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("context file %s.md is missing: %w", hash, os.ErrNotExist)
		}
		return "", fmt.Errorf("failed to read context file: %w", err)
	}
	return string(content), nil
}
