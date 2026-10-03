package operations

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const distillWarnBody = "a fragment body long enough that a real distillation is plausible"

// A rejected distillation's warning says what is actually left on the item.
// On an edit the item's previous distillation was already discarded with its
// old content, so there is none to keep; on a re-distill of unchanged content
// there is, and it survives.
func TestDistillRejection_WarningNamesWhatIsLeft(t *testing.T) {
	ctx := context.Background()
	cfg := newItemTestBundle(t)
	_, err := UpdateBundle(ctx, cfg, UpdateBundleRequest{
		Name:         "b",
		SetFragments: map[string]BundleFragmentInput{"f": {Content: distillWarnBody}},
		Distiller:    &recordingDistiller{returnValue: "PRIOR distilled text", returnModel: "m"},
	})
	require.NoError(t, err)

	for _, empty := range []string{"", "x"} { // empty, and too short to be real
		t.Run("edit/"+empty, func(t *testing.T) {
			warnings := captureWarnings(t)
			_, err := UpdateBundle(ctx, cfg, UpdateBundleRequest{
				Name:         "b",
				SetFragments: map[string]BundleFragmentInput{"f": {Content: distillWarnBody + " " + empty + "edited"}},
				Distiller:    &recordingDistiller{returnValue: empty},
			})
			require.NoError(t, err)
			b, err := GetBundle(cfg, "b")
			require.NoError(t, err)
			require.Empty(t, b.Fragments["f"].Distilled, "the edit discarded the old distillation")
			assert.NotContains(t, warnings.String(), "keeping the previous distillation")
			assert.Contains(t, warnings.String(), "left undistilled")
		})
	}

	// Restore a distillation, then re-distill unchanged content and reject it.
	_, err = DistillItem(ctx, cfg, DistillItemRequest{Bundle: "b", Kind: ItemKindFragment, Name: "f", Force: true,
		Distiller: &recordingDistiller{returnValue: "PRIOR distilled text", returnModel: "m"}})
	require.NoError(t, err)
	for _, empty := range []string{"", "x"} {
		t.Run("redistill/"+empty, func(t *testing.T) {
			warnings := captureWarnings(t)
			_, err := DistillItem(ctx, cfg, DistillItemRequest{Bundle: "b", Kind: ItemKindFragment, Name: "f", Force: true,
				Distiller: &recordingDistiller{returnValue: empty}})
			require.NoError(t, err)
			b, err := GetBundle(cfg, "b")
			require.NoError(t, err)
			assert.Equal(t, "PRIOR distilled text", b.Fragments["f"].Distilled)
			assert.Contains(t, warnings.String(), "keeping the previous distillation")
			assert.False(t, strings.Contains(warnings.String(), "left undistilled"))
		})
	}
}
