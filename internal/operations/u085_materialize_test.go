package operations

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/trust"
	"github.com/ctxloom/ctxloom/resources"
)

// rejectEveryBuiltinFragment rejects the content of every fragment shipped in a
// built-in bundle. The built-in fragments are unconditional — the isolation-axes
// fragment lands in EVERY assembly — so without this an assembled context can
// never be empty and the empty-context path is unreachable. A user rejecting a
// builtin is a supported state: config_bundles.go routes builtins BELOW the
// rejection step precisely "so a user can still reject a builtin".
func rejectEveryBuiltinFragment(t *testing.T) {
	t.Helper()
	names, err := resources.ListBuiltinBundles()
	require.NoError(t, err)
	for _, name := range names {
		data, err := resources.GetBuiltinBundle(name)
		require.NoError(t, err)
		var b bundles.Bundle
		require.NoError(t, yaml.Unmarshal(data, &b))
		for fragName, frag := range b.Fragments {
			payload, form := frag.ContentPayload(false)
			installUnsignedRejection(t,
				trust.Ref{Bundle: name, Kind: trust.KindFragment, Name: fragName, IsLocal: true},
				signing.Form(form), payload)
		}
	}
}

// emptyContextMaterializeFixture is materializeFixture with every source of
// context removed: the profile selects a tag nothing carries and every builtin
// fragment is rejected, so AssembleContext resolves cleanly to "".
func emptyContextMaterializeFixture(t *testing.T) (*config.Config, string) {
	t.Helper()
	cfg, target := materializeFixture(t, "UNSELECTED-CONTENT")
	rejectEveryBuiltinFragment(t)
	f := cfg.ToFixture()
	for name, p := range f.Profiles.Definitions {
		p.SelectTags = []string{"no-fragment-carries-this-tag"}
		f.Profiles.Definitions[name] = p
	}
	return config.NewFixture(f), target
}

// TestMaterializeProfile_RefusesEmptyAssembledContext pins this
// project's signature silent no-op: measured pre-fix, MaterializeProfile
// returned a nil error, reported Wrote: [context mcp settings commands skills]
// — naming the context surface — and wrote NO CLAUDE.md at all. Zero bytes, a
// success message, and a --target tree whose entire specialization is missing.
// The function's own doc already calls the assembled context "the core payload
// every native surface is built from" and "the one HARD-error surface"; an
// assembly that resolves to nothing is a failed assembly, exactly as
// runResolvedAgent rules for a named profile set that assembles to nothing.
func TestMaterializeProfile_RefusesEmptyAssembledContext(t *testing.T) {
	cfg, target := emptyContextMaterializeFixture(t)

	asm, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{Profiles: []string{"reviewer"}})
	require.NoError(t, err)
	require.Empty(t, asm.Context, "the fixture must genuinely reach an empty assembled context")

	res, err := MaterializeProfile(context.Background(), cfg, MaterializeProfileRequest{
		Profiles: []string{"reviewer"},
		Target:   target,
	})
	require.Error(t, err, "materializing a named profile set that assembles to nothing must fail loudly")
	assert.Nil(t, res)
	assert.Contains(t, err.Error(), "reviewer", "the error must name the profile set that assembled to nothing")
	assert.NoFileExists(t, filepath.Join(target, "CLAUDE.md"))
}

// TestMaterializeProfile_RestoresCallersTrustGate pins that
// MaterializeProfile installs its own executable trust gate on the CALLER's
// shared *config.Config and used to leave it there, so every later consumer of
// that config — in a long-lived process, every subsequent operation — silently
// inherited a gate it never asked for. The gate must be scoped to the call.
func TestMaterializeProfile_RestoresCallersTrustGate(t *testing.T) {
	cfg, target := materializeFixture(t, "MATERIALIZED-CONTENT")

	callersGateConsulted := 0
	cfg.SetExecutableTrustGate(bundles.FilterFunc(func(bundles.Exposure) bundles.Verdict {
		callersGateConsulted++
		return bundles.Verdict{Admit: true, Reason: bundles.ReasonLocal}
	}))

	_, err := MaterializeProfile(context.Background(), cfg, MaterializeProfileRequest{
		Profiles: []string{"reviewer"},
		Target:   target,
	})
	require.NoError(t, err)

	restored := cfg.ExecutableTrustGate()
	require.NotNil(t, restored, "the caller's gate must survive the call")
	restored.Admit(bundles.Exposure{})
	assert.Equal(t, 1, callersGateConsulted,
		"the config must still carry the CALLER's gate, not materialize's own")
}
