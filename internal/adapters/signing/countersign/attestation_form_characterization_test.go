package countersign

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/signing"
	"github.com/ctxloom/ctxloom/internal/core/trust"
)

// TestAttestationFormFor_PinsEveryKindAndLayout pins the attestation form
// table: which (kind, layout) pairs can be countersigned and under which
// form, and that every other pair — an unknown kind, or a layout the kind is
// never signed in — is refused rather than passed through.
func TestAttestationFormFor_PinsEveryKindAndLayout(t *testing.T) {
	want := map[trust.ItemKind]map[signing.Form]signing.AttestationForm{
		trust.KindFragment: {signing.FormRaw: signing.AttestFragmentRaw, signing.FormDistilled: signing.AttestFragmentDistilled},
		trust.KindPrompt:   {signing.FormRaw: signing.AttestCommandRaw, signing.FormDistilled: signing.AttestCommandDistilled},
		trust.KindMCP:      {signing.FormRaw: signing.AttestExecMCP},
		trust.KindHook:     {signing.FormRaw: signing.AttestExecHook},
		trust.KindSkill:    {signing.FormRaw: signing.AttestSkill},
	}
	kinds := []trust.ItemKind{trust.KindFragment, trust.KindPrompt, trust.KindMCP, trust.KindHook, trust.KindSkill, trust.ItemKind("bogus")}
	layouts := []signing.Form{signing.FormRaw, signing.FormDistilled, signing.Form("bogus")}
	for _, kind := range kinds {
		for _, layout := range layouts {
			t.Run(fmt.Sprintf("%s/%s", kind, layout), func(t *testing.T) {
				got, err := AttestationFormFor(kind, layout)
				if expected, ok := want[kind][layout]; ok {
					require.NoError(t, err)
					assert.Equal(t, expected, got)
					return
				}
				require.Error(t, err)
				assert.Equal(t, signing.AttestNone, got)
			})
		}
	}
}
