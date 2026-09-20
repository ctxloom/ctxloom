package operations

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/composite/compositetest"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/trust"
)

// commandsOf is the package's commands for a profile set (nil ⇒ the
// configured defaults).
func commandsOf(t *testing.T, cfg *config.Config, profileNames []string) []composite.Command {
	t.Helper()
	pkg, err := AssemblePackage(context.Background(), cfg, PackageRequest{Profiles: profileNames})
	require.NoError(t, err)
	out := make([]composite.Command, 0, len(pkg.Commands))
	for _, c := range pkg.Commands {
		out = append(out, c.Value)
	}
	return out
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

// claudeExportsOf is claude-code's command exports for the configured
// defaults, in the writers' shape.
func claudeExportsOf(t *testing.T, cfg *config.Config) []agent.CommandExport {
	t.Helper()
	pkg, err := AssemblePackage(context.Background(), cfg, PackageRequest{})
	require.NoError(t, err)
	exports, err := ExportsFor(pkg, "claude-code")
	require.NoError(t, err)
	return CommandExportsOf(exports)
}

// claudeSkillExportsOf is claude-code's skill exports for a profile set.
func claudeSkillExportsOf(t *testing.T, cfg *config.Config, profileNames []string) []agent.SkillExport {
	t.Helper()
	pkg, err := AssemblePackage(context.Background(), cfg, PackageRequest{Profiles: profileNames})
	require.NoError(t, err)
	exports, err := ExportsFor(pkg, "claude-code")
	require.NoError(t, err)
	return SkillExportsOf(exports)
}

// The exec bit on a skill's script survives the projection into the
// writers' shape: bundles keeps a file's mode as plain permission bits, the
// package carries them, and the os.FileMode conversion is here.
func TestSkillExportsOf_KeepsTheExecBit(t *testing.T) {
	ex := SkillExportsOf(engine.Exports{Skills: []engine.SkillExport{{
		Name: "humanize", Enabled: true,
		Files: []engine.SkillFile{{Path: "scripts/run.sh", Mode: 0o755, Bytes: []byte("#!/bin/sh\n")}, {Path: "SKILL.md", Mode: 0o644}},
	}}})
	require.Len(t, ex, 1)
	byPath := map[string]uint32{}
	for _, f := range ex[0].Files {
		byPath[f.RelPath] = uint32(f.Mode)
	}
	assert.Equal(t, uint32(0o755), byPath["scripts/run.sh"], "the exec bit survives the export projection")
	assert.Equal(t, uint32(0o644), byPath["SKILL.md"])
}
