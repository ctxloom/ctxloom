package main

import (
	"embed"

	"github.com/ctxloom/ctxloom/internal/adapters/cli"
	"github.com/ctxloom/ctxloom/internal/adapters/companions/loadout"
)

// loadoutYAML is ctxloom's own loadout — what ctxloom delivers into an engine
// on its own behalf (signature-envelope spec §4.3): its MCP server entry and
// its always-on guidance. ctxloom is its own companion: this content reaches
// a session through the same self-probe, reader and gate as every other
// companion's (see cli.EmbeddedLoadout), so a profile can exclude it and a
// human can reject it.
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
// computed at runtime (spec §4.3, §7A.5). For ctxloom's OWN loadout this
// signature is circular — the trust root vouching for the key ships in this
// same binary — and the companion reader verifies it but never presents it
// as trust (bundles.CompanionLoadout.Self).
var loadoutSig = loadout.ReadEmbeddedSig(loadoutSigFiles)

// embeddedLoadout is what the composition root hands the CLI's `loadout`
// command.
func embeddedLoadout() cli.EmbeddedLoadout {
	return cli.EmbeddedLoadout{YAML: loadoutYAML, Sig: loadoutSig}
}
