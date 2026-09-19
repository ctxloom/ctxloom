package operations

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/composite/compositetest"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/trust"
)

// commandsOf is the package's commands for a profile set (nil ⇒ the
// configured defaults), in the loaded shape the engine mappers decide over.
func commandsOf(t *testing.T, cfg *config.Config, profileNames []string) []*bundles.LoadedContent {
	t.Helper()
	pkg, err := AssemblePackage(context.Background(), cfg, PackageRequest{Profiles: profileNames})
	require.NoError(t, err)
	return loadedCommands(pkg)
}

// skillsOf is the package's skills for a profile set.
func skillsOf(t *testing.T, cfg *config.Config, profileNames []string) []*bundles.LoadedSkill {
	t.Helper()
	pkg, err := AssemblePackage(context.Background(), cfg, PackageRequest{Profiles: profileNames})
	require.NoError(t, err)
	return LoadedSkills(pkg)
}

// hashTrust admits exactly the payloads whose hash is in want — the shape a
// countersignature has: one approval covers one set of bytes — by REJECTING
// every other payload.
func hashTrust(want ...string) composite.Trust {
	granted := make(map[string]bool, len(want))
	for _, w := range want {
		granted[w] = true
	}
	return compositetest.Trust(compositetest.RejectWhen(func(_ trust.Ref, payload []byte) bool {
		return !granted[bundles.HashPayload(payload)]
	}))
}
