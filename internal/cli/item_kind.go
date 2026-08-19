package cli

import (
	"fmt"
	"unicode"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// The fragment/command item vocabulary and its reference grammar: the kind names
// the operations core validates, and the `bundle#<kind>s/<name>` refs this
// frontend accepts for them.

// ItemType is operations.ItemKind under the name this flow uses. It is an ALIAS,
// not a second type: the CLI and the operations core address fragments and
// commands with ONE vocabulary, so there is nothing to keep in sync and nothing to
// convert at the boundary.
type ItemType = operations.ItemKind

const (
	ItemTypeFragment = operations.ItemKindFragment
	ItemTypeCommand  = operations.ItemKindCommand
)

// itemRefPrefix returns the selector directory an item kind is WRITTEN under
// in a reference ("fragments/", "commands/"). It only ever mints; the parse
// side of the grammar is trust.ParseSelector, reached through
// bundles.ParseItemAsk. A free function rather than a method because the kind
// is owned by the operations core.
func itemRefPrefix(t ItemType) string {
	return string(t) + "s/"
}

// itemKindOf is the trust item kind an ItemType addresses. The two
// vocabularies differ by exactly one word — a command is WRITTEN "#commands/"
// but STORED under trust.KindPrompt, so existing grants survive the item-kind
// rename — and stating the mapping once here is what keeps that divergence
// from being re-derived, differently, at each frontend.
func itemKindOf(t ItemType) trust.ItemKind {
	if t == ItemTypeCommand {
		return trust.KindPrompt
	}
	return trust.KindFragment
}

// itemRefTarget resolves a `<bundle>#<kind>/<name>` argument into the bundle
// and item halves this frontend passes to the operations core.
//
// The selector is judged by bundles.ParseItemAsk — the ONE parser every reader
// routes through — so "#prompts/x" and "#commands/x" are the same ask here as
// everywhere else. A well-formed selector naming a kind this command does not
// serve is a KIND MISMATCH, not a syntax error, and is reported as one: told
// "invalid reference format", a user re-reads the punctuation they got right.
func itemRefTarget(ref string, itemType ItemType) (bundleName, itemName string, err error) {
	ask, err := bundles.ParseItemAsk(ref)
	if err != nil {
		return "", "", err
	}
	if !ask.Scoped {
		return "", "", fmt.Errorf("invalid reference format: expected bundle#%sname (got %q)", itemRefPrefix(itemType), ref)
	}
	if want := itemKindOf(itemType); ask.Kind != want {
		return "", "", fmt.Errorf("%q selects a %s, not a %s (expected bundle#%sname)",
			ref, ask.Kind.Dir(), itemRefPrefix(itemType), itemRefPrefix(itemType))
	}
	if ask.Bundle == "" {
		return "", "", fmt.Errorf("invalid reference: missing bundle name in %q", ref)
	}
	return ask.Bundle, ask.Item, nil
}

// titleCase capitalizes the first letter of a string.
func titleCase(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}
