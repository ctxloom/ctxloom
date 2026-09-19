package composite_test

import (
	"testing"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// The unit corpus: two project bundles in memory. "alpha" ships fragments
// (one premised, one using a variable), two commands (one opting out of
// claude-code and carrying a block for an engine nobody registers), and an
// MCP server; "beta" ships one fragment tagged for tag selection and one
// command. Refs are the canonical local grammar the catalog resolves to.
// alpha's whole-bundle expansion is [maybe, rules, style] by name; the
// bookend places the second entry last, so the delivered order is
// [style, rules] with maybe held back.
const (
	alphaRef = "ctxloom+local:alpha"
	betaRef  = "ctxloom+local:beta"

	alphaRules = alphaRef + "#fragments/rules"
	alphaStyle = alphaRef + "#fragments/style"
	alphaMaybe = alphaRef + "#fragments/maybe"
	betaTagged = betaRef + "#fragments/tagged"
	// A command's trust ref keeps the "prompts" kind segment (trust.KindPrompt)
	// even though the load selector is "#commands/", so grants survive the
	// item-kind rename.
	alphaReview  = alphaRef + "#prompts/review"
	alphaRelease = alphaRef + "#prompts/release"
	betaShip     = betaRef + "#prompts/ship"
)

const alphaYAML = `version: 1.0.0
description: alpha corpus
fragments:
  rules:
    tags: [house]
    content: "Rules for {{project}}."
  style:
    content: "Prefer small functions."
  maybe:
    premise: "the agent is about to touch a signed bundle"
    content: "Do not edit a signed bundle in place."
commands:
  review:
    description: Review the diff
    content: "Review the staged diff."
    llm:
      claude-code:
        description: Review (claude)
        argument_hint: "[path]"
        allowed_tools: [Read]
        model: sonnet
  release:
    description: Cut a release
    content: "Prepare the release notes."
    llm:
      claude-code:
        enabled: false
mcp:
  db:
    command: mcp-db
`

const betaYAML = `version: 1.0.0
description: beta corpus
fragments:
  tagged:
    tags: [house]
    content: "Tagged body."
commands:
  ship:
    content: "Ship it."
`

// corpus builds the catalog over the in-memory bundles.
func corpus(t *testing.T) bundles.Catalog {
	t.Helper()
	fs := afero.NewMemMapFs()
	testsupport.SeedTree(t, fs, paths.BundlesLayoutRoot("/app", paths.LayoutV2), map[string]string{
		"alpha.yaml": alphaYAML,
		"beta.yaml":  betaYAML,
	})
	return bundles.NewLoader(bundles.NewProjectReader(fs, []string{"/app"})).Catalog()
}
