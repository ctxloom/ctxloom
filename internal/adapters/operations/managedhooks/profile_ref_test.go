// Unit-level proofs that a directory profile's declared hooks are addressed
// off the profile's SOURCE ref (profiles.ResolvedProfile.SourceRef via
// profileRefBase), never its display name, with exactly one '#', and that
// one nothing can address is withheld rather than shipped.
package managedhooks

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/profiles"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/shared/refuri"
	"github.com/ctxloom/ctxloom/internal/testsupport/bundletree"
)

func TestProfileRefBase_BundleShippedUsesSourceRef(t *testing.T) {
	resolved := &profiles.ResolvedProfile{SourceRef: "https://github.com/acme/tools@bundles/kit"}
	assert.Equal(t, "https://github.com/acme/tools@bundles/kit", profileRefBase(resolved, "dev"))
}

func TestProfileRefBase_SourcelessFallsBackToTheName(t *testing.T) {
	assert.Equal(t, "my-local-profile", profileRefBase(&profiles.ResolvedProfile{}, "my-local-profile"))
	assert.Equal(t, "my-local-profile", profileRefBase(nil, "my-local-profile"))
}

// addressableProfileHooks REBUILDS the unified set from a hand-written
// literal, one `keep(...)` line per event. An event missing from that literal
// is not a compile error — it is a hook the user declared that the writer
// never receives. Reflection over wire.UnifiedHooks rather than a list of
// events, for the usual reason: the list is the thing that goes stale.
func TestAddressableProfileHooks_EveryUnifiedEventSurvives(t *testing.T) {
	typ := reflect.TypeOf(wire.UnifiedHooks{})
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		event := strings.Split(field.Tag.Get("yaml"), ",")[0]
		t.Run(event, func(t *testing.T) {
			var u wire.UnifiedHooks
			cmd := "cmd-" + event
			reflect.ValueOf(&u).Elem().Field(i).Set(reflect.ValueOf([]wire.Hook{{Command: cmd, Type: "command"}}))

			out := addressableProfileHooks("my-local-profile", wire.HooksConfig{Unified: u})

			got, _ := reflect.ValueOf(out.Unified).Field(i).Interface().([]wire.Hook)
			require.Lenf(t, got, 1, "a declared %s hook was dropped — it is missing from the rebuild literal", field.Name)
			assert.Equal(t, cmd, got[0].Command)
		})
	}
}

// A hook whose source cannot be addressed is a load error: it is withheld,
// never shipped under an identity nothing could parse. A source carrying a
// scheme marker that does not parse was meant as a qualified reference, so it
// is refused rather than read as a bare local name.
func TestAddressableProfileHooks_UnaddressableSourceIsWithheld(t *testing.T) {
	in := wire.HooksConfig{Unified: wire.UnifiedHooks{PreTool: []wire.Hook{{Command: "x", Type: "command"}}}}

	out := addressableProfileHooks("ctxloom+git://", in)

	assert.Empty(t, out.Unified.PreTool)
}

// A bundle-shipped profile (seeded by production bundle-profile seeding,
// config.loadBundleProfileSeed) delivers its declared hook: the composed ref is
// "<SourceRef>#hooks/pre_tool/0", single '#', so it parses. A ref composed onto
// the profile ref ("<bundle>#profiles/<name>#hooks/pre_tool/0") would not, and
// the hook would be withheld.
func TestAssemble_LocalBundleShippedProfile_HookIsDelivered(t *testing.T) {
	appDir := filepath.Join(t.TempDir(), paths.AppDirName)
	bundlesDir := paths.LocalBundlesPathFor(appDir, paths.LayoutV2)
	require.NoError(t, os.MkdirAll(bundlesDir, 0o755))
	bundletree.WriteOS(t, bundlesDir, "kit", ""+
		"version: \"1.0\"\n"+
		"profiles:\n  dev:\n    hooks:\n      unified:\n        pre_tool:\n          - command: bundle-shipped-hook\n            type: command\n")

	profileRef := remote.LocalBundleRef("kit") + refuri.ProfileSelector + "dev"
	cfg := config.NewFixture(config.Fixture{
		DefaultAgent: "default",
		Agents:       map[string]agents.Agent{"default": {Profiles: []string{profileRef}}},
		AppPaths:     []string{appDir},
	})

	assembled := Assemble(cfg, nil)
	require.Len(t, assembled.Wire().Unified.PreTool, 1, "a bundle-shipped profile hook must reach the managed set")
	assert.Equal(t, "bundle-shipped-hook", assembled.Wire().Unified.PreTool[0].Command)
}
