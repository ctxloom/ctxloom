package main

import (
	"embed"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/shared/companionloadout"
)

// loadoutYAML is taskloom's own ctxloom loadout — the bundle content
// taskloom contributes to a ctxloom session (signature-envelope spec §4.3):
// the taskloom fragment, the session-bind and plan-stamping hooks, and the
// taskloom MCP server registration. It replaces the formerly-embedded
// resources/builtin_bundles/taskloom.yaml on the ctxloom side (now deleted)
// — ctxloom discovers this binary on PATH and execs
// `taskloom loadout --format json` instead of vendoring this content.
//
//go:embed loadout.yaml
var loadoutYAML []byte

// loadoutSigFiles embeds loadout.yaml's OPTIONAL detached publish-signature
// sibling (loadout.yaml.sig) via a WILDCARD pattern rather than a literal
// `//go:embed loadout.yaml.sig`: a literal directive fails the build outright
// when the named file is absent, but the .sig is meant to stay optional
// forever (spec §10.1 — unsigned is legal, ordinary, and routes to review),
// so a build must keep working whether or not one has been generated yet.
//
//go:embed loadout.yaml*
var loadoutSigFiles embed.FS

// loadoutSig is the detached publish signature over loadoutYAML (namespace
// signing.NamespacePublish), read from the embedded loadout.yaml.sig sibling
// when `just sign-loadouts` has produced and committed one — never held or
// computed at runtime (spec §4.3, §7A.5). Empty when no .sig is committed,
// which companionloadout.NewCommand already treats as "emit unsigned".
var loadoutSig = companionloadout.ReadEmbeddedSig(loadoutSigFiles)

// newLoadoutCmd is a factory (rather than a package-level *cobra.Command
// wired via this file's own init() convention) so registration has no
// hidden ordering dependency; main.go adds it explicitly.
func newLoadoutCmd() *cobra.Command {
	return companionloadout.NewCommand("taskloom", loadoutYAML, loadoutSig)
}
