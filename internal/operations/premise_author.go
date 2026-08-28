package operations

import (
	"context"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/memory"
	"github.com/ctxloom/ctxloom/resources"
)

// The premise AUTHORING half: draft a premise for one fragment body and judge
// whether the body should be split. This is a PROPOSAL surface, deliberately
// not autonomous — the draft goes in front of the fragment's author, who
// accepts, edits or rejects it; nothing here writes a fragment. The premise
// SELECTION half (the index an agent chooses from) lives in premise.go.

// premiseAuthorPromptName is the prompt file's stem, shared by the embedded
// lookup and the on-disk PromptDir override so the two can never name
// different files.
const premiseAuthorPromptName = "premise-author"

// premiseAuthorPromptEmbedded is resolved at init: a missing embedded prompt
// is a build-time bug (the file is compiled in), not a runtime condition.
var premiseAuthorPromptEmbedded = resources.MustGetPromptText(premiseAuthorPromptName)

// PremiseAuthorConfig configures how a draft call reaches an LLM. The zero
// value works: the default plugin drafts with its default model.
type PremiseAuthorConfig struct {
	// LLM is the plugin to run (default: claude-code).
	LLM string
	// Model selects the model within the plugin (e.g. "haiku", "sonnet").
	Model string
	// Env is the resolved LLM label's config-declared environment
	// (llm.configs.<label>.env).
	Env map[string]string
	// ClientFactory creates the plugin client; nil uses the real one. It is
	// the stochastic boundary: a test supplies pb.MockClientFactory and both
	// sides of the call stay deterministic.
	ClientFactory pb.ClientFactory
	// PromptDir loads the authoring prompt from a directory on disk
	// (<dir>/premise-author.md) instead of the binary's embedded copy, so a
	// prompt-evaluation harness can A/B variants without a rebuild. Empty uses
	// the embedded prompt. A named prompt missing from the directory is a hard
	// failure, never a silent fall back to the embedded text: the whole value
	// of the override is knowing WHICH prompt produced a result.
	PromptDir string
}

// PremiseDraft is one proposed premise, as material for the author's judgment
// rather than a finished artifact.
type PremiseDraft struct {
	// Fragment is the fragment the draft is for — the name DraftPremise was
	// called with, echoed so a batch of drafts stays attributable.
	Fragment string
	// Premise is the proposed premise text. Empty means the model judged the
	// fragment unconditional — it should ALWAYS load — which is exactly what
	// an empty premise means on the fragment itself (BundleFragment.Premise:
	// absence asserts unconditional applicability).
	Premise string
	// Moments are the concrete moments the premise claims to fire on, stated
	// as the acting agent would state them.
	Moments []string
	// NotFor are adjacent moments the premise deliberately does NOT fire on —
	// the drafted boundary, made inspectable.
	NotFor []string
	// SplitHint is non-empty when the body spans UNRELATED moments — a
	// fragment doing two jobs — and names the moments that diverge and the
	// split proposed. Empty means the body reads as one coherent idea.
	SplitHint string
}

// DraftPremise proposes a premise for one fragment body, plus a split verdict,
// by one LLM call through the shared distillation path (memory.Distill). The
// result is a PROPOSAL for the author to judge; callers must not write it back
// to a fragment unreviewed.
func DraftPremise(ctx context.Context, cfg PremiseAuthorConfig, name, body string) (*PremiseDraft, error) {
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("draft premise: fragment name is empty")
	}
	if strings.TrimSpace(body) == "" {
		// An empty body must refuse, not "succeed": a draft from nothing would
		// be a confident premise for content that does not exist.
		return nil, fmt.Errorf("draft premise for %q: fragment body is empty; there is nothing to draft a premise from", name)
	}
	prompt, err := premiseAuthorPrompt(cfg.PromptDir)
	if err != nil {
		return nil, err
	}
	payload := fmt.Sprintf("<fragment name=%q>\n%s\n</fragment>", name, body)
	out, err := memory.Distill(ctx, memory.DistillConfig{
		LLM:           cfg.LLM,
		Model:         cfg.Model,
		Env:           cfg.Env,
		ClientFactory: cfg.ClientFactory,
	}, prompt, payload)
	if err != nil {
		return nil, fmt.Errorf("draft premise for %q: %w", name, err)
	}
	draft, err := parsePremiseDraft(name, out)
	if err != nil {
		return nil, fmt.Errorf("draft premise for %q: %w", name, err)
	}
	return draft, nil
}

// premiseAuthorPrompt returns the authoring prompt: the PromptDir override
// when configured, the embedded copy otherwise. A PromptDir read failure is
// returned, never swallowed: falling back to the embedded prompt would
// attribute the run to a prompt that never produced it.
func premiseAuthorPrompt(dir string) (string, error) {
	if dir != "" {
		loaded, err := resources.PromptTextFromDir(dir, premiseAuthorPromptName)
		if err != nil {
			return "", fmt.Errorf("load prompt %q from %s: %w", premiseAuthorPromptName, dir, err)
		}
		return loaded, nil
	}
	return premiseAuthorPromptEmbedded, nil
}

// premiseNone is the sentinel the prompt instructs the model to emit for a
// fragment that should always load. It maps to an EMPTY Premise, because an
// empty premise is what "always loads" already means on a fragment.
const premiseNone = "NONE"

// premiseDraftDoc is the YAML document the prompt instructs the model to emit.
// Premise is a pointer so a document with NO premise key — prose, or the wrong
// shape entirely — is distinguishable from an explicitly empty one; both are
// rejected, but only because the field is checkable at all.
type premiseDraftDoc struct {
	Premise *string  `yaml:"premise"`
	Moments []string `yaml:"moments"`
	NotFor  []string `yaml:"not_for"`
	Split   string   `yaml:"split"`
}

// parsePremiseDraft parses the model's output into a PremiseDraft for the
// named fragment. Tolerates a markdown code fence around the document (models
// add one despite instruction); anything else that is not the instructed YAML
// shape is an error, never a best-effort draft — a draft the parser guessed at
// would go in front of an author as if the model had said it.
func parsePremiseDraft(name, out string) (*PremiseDraft, error) {
	doc := stripCodeFence(out)
	var parsed premiseDraftDoc
	if err := yaml.Unmarshal([]byte(doc), &parsed); err != nil {
		return nil, fmt.Errorf("output is not the instructed YAML shape: %w", err)
	}
	if parsed.Premise == nil {
		return nil, fmt.Errorf("output has no premise field")
	}
	premise := strings.TrimSpace(*parsed.Premise)
	switch premise {
	case premiseNone:
		premise = ""
	case "":
		// An explicitly empty premise is neither a draft nor the NONE verdict
		// the prompt defines; treat it as the malformed output it is.
		return nil, fmt.Errorf("output premise is empty (an always-load verdict must say %s)", premiseNone)
	}
	return &PremiseDraft{
		Fragment:  name,
		Premise:   premise,
		Moments:   trimNonEmpty(parsed.Moments),
		NotFor:    trimNonEmpty(parsed.NotFor),
		SplitHint: strings.TrimSpace(parsed.Split),
	}, nil
}

// stripCodeFence removes one enclosing markdown code fence (``` or ```yaml)
// when the whole document is wrapped in one, and returns the input trimmed
// otherwise.
func stripCodeFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	lines := strings.Split(s, "\n")
	if len(lines) < 2 || strings.TrimSpace(lines[len(lines)-1]) != "```" {
		return s
	}
	return strings.TrimSpace(strings.Join(lines[1:len(lines)-1], "\n"))
}

// trimNonEmpty trims each entry and drops the empty ones, returning nil for
// an empty result so a draft with no entries compares clean.
func trimNonEmpty(in []string) []string {
	var out []string
	for _, s := range in {
		if t := strings.TrimSpace(s); t != "" {
			out = append(out, t)
		}
	}
	return out
}
