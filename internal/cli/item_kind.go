package cli

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/ctxloom/ctxloom/internal/operations"
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

// itemRefPrefix returns the prefix an item kind takes in a reference (e.g.
// "fragments/" or "commands/"). A free function rather than a method because the
// kind is owned by the operations core; the ref grammar is this frontend's.
func itemRefPrefix(t ItemType) string {
	return string(t) + "s/"
}

// parseItemRef parses a reference like "bundle#fragments/name" or "bundle#commands/name".
func parseItemRef(ref string, itemType ItemType) (bundleName, itemName string, err error) {
	hashIdx := strings.Index(ref, "#")
	if hashIdx == -1 {
		return "", "", fmt.Errorf("invalid reference format: expected bundle#%sname (got %q)", itemRefPrefix(itemType), ref)
	}

	bundleName = ref[:hashIdx]
	itemPath := ref[hashIdx+1:]

	prefix := itemRefPrefix(itemType)
	if !strings.HasPrefix(itemPath, prefix) {
		return "", "", fmt.Errorf("invalid reference format: expected bundle#%sname (got %q)", prefix, ref)
	}

	itemName = strings.TrimPrefix(itemPath, prefix)
	if itemName == "" {
		return "", "", fmt.Errorf("invalid reference: missing %s name", itemType)
	}

	return bundleName, itemName, nil
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
