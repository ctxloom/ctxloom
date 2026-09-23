package operations

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ctxloom/ctxloom/internal/testsupport/bundletree"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// MCP separates the phases explicitly: resources/list returns DESCRIPTORS,
// resources/read returns CONTENTS. ctxloom had both URIs, but the list
// projection DROPPED the premise — so the evaluation phase was missing the one
// field a consumer evaluates on, and the menu had to be bolted onto a tool
// response instead. That bespoke channel is what put a domain struct on the wire.
//
// This drives ListFragments and asserts the MARSHALLED payload. An earlier
// version of this test built a FragmentEntry literal and marshalled that, which
// tested encoding/json rather than the projection: both mutations — dropping the
// premise, and handing back a bare ref — survived it.
func listPremiseFixture(t *testing.T) *bundles.Loader {
	t.Helper()
	fs := afero.NewMemMapFs()
	doc := `version: "1.0"
fragments:
  general:
    tags: ["review"]
    premise: You are reviewing a change for structural fit.
    content: structural review guidance
  always:
    tags: ["core"]
    content: unconditional guidance
`
	bundletree.Write(t, fs, authoredV1(testBaseDir), "premised", doc)
	return bundles.NewLoader(bundles.NewProjectReader(fs, []string{paths.LocalBundlesPath(testBaseDir)}))
}

func TestListFragments_PayloadCarriesPremiseAndQualifiedRef(t *testing.T) {
	res, err := ListFragments(context.Background(), nil, ListFragmentsRequest{Loader: listPremiseFixture(t)})
	require.NoError(t, err)

	raw, err := json.Marshal(res.Fragments)
	require.NoError(t, err)
	var got []map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))

	byName := map[string]map[string]any{}
	for _, e := range got {
		byName[e["name"].(string)] = e
	}
	require.Contains(t, byName, "general")
	require.Contains(t, byName, "always")

	require.Equal(t, "You are reviewing a change for structural fit.", byName["general"]["premise"],
		"the descriptor must carry the premise, or the evaluation phase has nothing to evaluate")

	// A selection quotes the QUALIFIED ref back. `general` alone is defined in
	// seventeen code-review bundles in the default corpus, and a bare ask
	// resolves to the first in List order with only a warning — so a bare ref
	// here silently delivers a different lens than the one chosen.
	require.Equal(t, "premised#fragments/general", byName["general"]["ref"])

	// Absence asserts unconditional; an empty string would read as a premise.
	require.NotContains(t, byName["always"], "premise")

	// Descriptions are for choosing. Bodies come from ctxloom://fragments/{name},
	// for the chosen ones only.
	require.NotContains(t, byName["general"], "content")
}
