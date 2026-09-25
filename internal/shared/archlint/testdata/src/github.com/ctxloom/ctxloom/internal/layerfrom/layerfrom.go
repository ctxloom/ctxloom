package layerfrom // want `layering rule "t-other" allows "internal/layerfrom -> internal/layerlow" \(other-why\) but "internal/layerfrom" is not under the rule's From prefixes \[internal/elsewhere\] — delete the entry` `layering rule "t-rule" allows "internal/layerfrom -> internal/layershared" \(shared-why\) but "internal/layershared" is not something the rule forbids — delete the entry` `layering rule "t-rule" allows "internal/layerfrom -> internal/layerlow/gone" \(gone-why\) but internal/layerfrom no longer imports internal/layerlow/gone — delete the entry, or it will silently exempt that edge when it comes back`

import (
	"fmt"

	"github.com/ctxloom/ctxloom/internal/layerlow" // want `^package internal/layerfrom imports github.com/ctxloom/ctxloom/internal/layerlow, which layering rule "t-rule" forbids \(packages under \[internal/layerfrom internal/layerquiet\] must not import packages under \[internal/layerlow\]\)\. If this is a deliberate, reviewed exception, add "internal/layerfrom -> internal/layerlow" to that rule's Allowed map in archrules\.LayeringRules naming the fix required to remove it\.$`
	"github.com/ctxloom/ctxloom/internal/layerlow/allowed"
	"github.com/ctxloom/ctxloom/internal/layerlow/okay"
	"github.com/ctxloom/ctxloom/internal/layershared"
)

// Sum uses every import.
func Sum() string {
	return fmt.Sprint(layerlow.V + allowed.V + okay.V + layershared.V)
}
