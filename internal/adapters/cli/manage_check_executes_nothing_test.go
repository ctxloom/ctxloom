package cli

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestManageCheck_SurfaceCurrencyExecutesNothing pins that the wiring report
// `manage check` renders — operations.HarnessStatus, the one computation its
// text and json forms both emit — runs no companion, even for a context file
// the project writer's record owns: the case where currency is actually
// computed.
//
// The companion is SIGNED, so admission says yes, and the test then proves
// that composing the project's context DOES run it: without that guard the
// sentinel assertion would pass just as well against a companion nothing
// would ever execute.
func TestManageCheck_SurfaceCurrencyExecutesNothing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the sentinel companion is an sh script")
	}
	const bin = "ctxloom-companion-acme"
	root, _ := setupProject(t, "claude-code")
	testsupport.ChangeDir(t, root)
	sentinel, binPath := plantRealCompanion(t, bin)
	testsupport.SignCompanionForTesting(t, binPath, filepath.Join(root, ".ctxloom", "allowed_signers"))

	// An owned context file: delivered by the project writer, so the check
	// reports its currency rather than skipping it.
	kind, ok := engines.Registry().Lookup(engine.Name("claude-code"))
	require.True(t, ok)
	pkg := composite.Package{
		Context:   composite.Context{Text: "DELIVERED"},
		Fragments: []composite.Item[composite.Fragment]{{Ref: "t#fragment/f", Value: composite.Fragment{Name: "f", Body: "DELIVERED"}}},
	}
	_, _, err := operations.DeliverProject(context.Background(), afero.NewOsFs(), kind, pkg, root)
	require.NoError(t, err)

	cfg, err := GetConfig()
	require.NoError(t, err)
	res, err := operations.HarnessStatus(context.Background(), engines.Registry(), cfg, operations.HarnessStatusRequest{WorkDir: root})
	require.NoError(t, err)
	assert.NoFileExists(t, sentinel, "a status report must execute no companion")
	require.Len(t, res.Surfaces, 1, "the owned context file is reported")
	assert.Equal(t, "CLAUDE.md", res.Surfaces[0].Route)
	assert.Equal(t, string(agent.StatusDelivered), res.Surfaces[0].Status, "detail was %q", res.Surfaces[0].Detail)

	// Guard, AFTER the report: composing this project's context from the same
	// config does run the companion. The reader probes once per config
	// generation, so run first it would have spent the probe and left the
	// report nothing to execute.
	_, err = operations.AssembleContext(context.Background(), cfg, operations.AssembleContextRequest{
		Profiles: cfg.DefaultAgentProfiles(),
		Consumer: operations.MaterializedFor(engines.Registry(), "claude-code"),
	})
	require.NoError(t, err)
	assert.FileExists(t, sentinel, "precondition: composing the context must run the admitted companion")
}
