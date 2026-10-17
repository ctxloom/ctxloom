package layermod // want `layering rule "t-modules" allows "internal/layermod -> example.com/uikit/gone" \(modgone-why\) but internal/layermod no longer imports example.com/uikit/gone — delete the entry, or it will silently exempt that edge when it comes back`

import (
	"container/list"
	"fmt"

	"example.com/other"
	"example.com/uikit" // want `^package internal/layermod imports example.com/uikit, which layering rule "t-modules" forbids \(packages under \[internal/layermod\] must not import packages under \[container example.com/uikit\]\)\. If this is a deliberate, reviewed exception, add "internal/layermod -> example.com/uikit" to that rule's Allowed map in archrules\.LayeringRules naming the fix required to remove it\.$`
	"example.com/uikit/allowed"
)

// Sum uses every import. container/list is the standard library, which no
// module-directory Forbid entry ("container") may be mistaken for.
func Sum() string {
	return fmt.Sprint(list.New().Len(), other.V+uikit.V+allowed.V)
}
