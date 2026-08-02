package acptest

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
)

// vendoredSchemaSHA256 is the content address of acp-schema-v1.json as
// vendored at the commit named in the package doc comment's provenance block
// (Commit / Version / Vendored). It is the tripwire the package doc's
// re-vendor recipe points at.
//
// It is here, in a test, on purpose: the schema file itself carries no
// version marker of any kind (no $id, no version field — only $schema, title
// and anyOf), so NOTHING in the bytes can be compared against the prose that
// claims to describe them. An assertion over the bytes is the only mechanism
// left that makes the provenance block falsifiable.
const vendoredSchemaSHA256 = "92c1dfcda10dd47e99127500a3763da2b471f9ac61e12b9bf0430c32cf953796"

// TestVendoredSchema_ProvenanceIsCurrent fails the moment acp-schema-v1.json
// changes, which is exactly when the package doc's Commit / Version /
// Vendored lines stop being true.
//
// The concern: re-vendoring fetches `main`, so it picks up whatever
// the upstream branch holds today, while the provenance block keeps naming
// the commit that was fetched months ago. Nothing forced the two to move
// together, so the recorded commit could silently describe bytes that are no
// longer present — and a conformance harness whose whole output is "how far
// ctxloom is from schema X" is worthless once X is a guess.
//
// The pin does not verify that the block is CORRECT (no offline check can —
// the upstream commit id is not derivable from the bytes). It verifies that
// the block was VISITED: you cannot land new schema bytes without coming
// here, and this comment is what tells you what else to update.
func TestVendoredSchema_ProvenanceIsCurrent(t *testing.T) {
	sum := sha256.Sum256(schemaJSON)
	assert.Equal(t, vendoredSchemaSHA256, hex.EncodeToString(sum[:]),
		"acp-schema-v1.json changed. Re-vendoring is not finished until the "+
			"package doc comment's provenance block (Commit / Version / "+
			"Vendored) names the newly fetched upstream commit, the version "+
			"from schema/v1/CHANGELOG.md, and today's date — and this constant "+
			"is updated to the new content hash.")
}
