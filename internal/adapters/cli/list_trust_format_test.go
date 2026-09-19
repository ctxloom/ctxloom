package cli

import (
	"context"
	"encoding/json"
	"testing"

	toml "github.com/pelletier/go-toml/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	yaml "gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/config"
)

// trustFields is the slice of a listed row that carries the trust verdict —
// the three fields stampItemTrust writes and every structured consumer reads.
type trustFields struct {
	Trusted     any
	TrustSource any
	State       any
}

func trustFieldsOf(t *testing.T, row map[string]any) trustFields {
	t.Helper()
	return trustFields{Trusted: row["trusted"], TrustSource: row["trust_source"], State: row["state"]}
}

// TestListItems_EveryStructuredFormatCarriesTheSameTrustStamp proves that a
// machine consumer asking for yaml or toml is told the SAME trust verdict as
// one asking for json. The row's trust fields carry no omitempty, so a
// listing that skips the stamp does not omit them — it emits trusted:false /
// trust_source:"" / state:"" as if the content had been judged and failed.
// Stamping must therefore key on "is this a structured format", never on
// "is this exactly json".
func TestListItems_EveryStructuredFormatCarriesTheSameTrustStamp(t *testing.T) {
	newIsolatedFlowProject(t)
	cfg, err := config.LoadFresh()
	require.NoError(t, err)
	_, err = operations.CreateBundle(context.Background(), cfg, operations.CreateBundleRequest{
		Name: "demo",
		Fragments: map[string]operations.BundleFragmentInput{
			"x": {Content: "always-local body", NoDistill: true},
		},
	})
	require.NoError(t, err)

	var jsonRows []map[string]any
	require.NoError(t, json.Unmarshal([]byte(runOutputFlowCommand(t, "json", "fragment", "list", "--bundle", "demo")), &jsonRows))
	require.Len(t, jsonRows, 1)
	want := trustFieldsOf(t, jsonRows[0])
	require.Equal(t, true, want.Trusted, "control: the json surface stamps a local fragment trusted")
	require.Equal(t, "local", want.TrustSource, "control: the json surface names the local exemption")

	t.Run("yaml", func(t *testing.T) {
		var rows []map[string]any
		require.NoError(t, yaml.Unmarshal([]byte(runOutputFlowCommand(t, "yaml", "fragment", "list", "--bundle", "demo")), &rows))
		require.Len(t, rows, 1)
		assert.Equal(t, want, trustFieldsOf(t, rows[0]), "yaml must carry the json trust verdict, not an unstamped zero value")
	})

	t.Run("toml", func(t *testing.T) {
		var doc struct {
			Items []map[string]any `toml:"items"`
		}
		require.NoError(t, toml.Unmarshal([]byte(runOutputFlowCommand(t, "toml", "fragment", "list", "--bundle", "demo")), &doc))
		require.Len(t, doc.Items, 1)
		assert.Equal(t, want, trustFieldsOf(t, doc.Items[0]), "toml must carry the json trust verdict, not an unstamped zero value")
	})
}
