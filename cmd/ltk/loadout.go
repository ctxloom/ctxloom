package main

import (
	_ "embed"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/shared/companionloadout"
)

// loadoutYAML is ltk's own ctxloom loadout — the bundle content ltk
// contributes to a ctxloom session (signature-envelope spec §4.3). It is the
// single source of truth for what ltk tells ctxloom about itself: the ltk
// fragment, the pre-tool hook registration, and the task-runner setup skill.
// It replaces the formerly-embedded resources/builtin_bundles/ltk.yaml on
// the ctxloom side (now deleted) — ctxloom discovers this binary on PATH and
// execs `ltk loadout --format json` instead of vendoring this content.
//
//go:embed loadout.yaml
var loadoutYAML []byte

// loadoutSig is an OPTIONAL detached publish signature over loadoutYAML,
// produced at RELEASE build time and embedded via go:embed — never held or
// computed at runtime (spec §4.3, §7A.5). No release signing pipeline exists
// yet (build-time/goreleaser loadout signing is deliberately deferred, not
// this slice), so there is no loadout.yaml.sig to embed and this stays nil.
// When one is added, embedding it here is the only change needed —
// companionloadout.NewCommand already reads through this seam.
var loadoutSig []byte

func newLoadoutCmd() *cobra.Command {
	return companionloadout.NewCommand(progName, loadoutYAML, loadoutSig)
}
