package attest

import (
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/content"
	"github.com/ctxloom/ctxloom/internal/adapters/signing"
	"github.com/ctxloom/ctxloom/internal/core/trust"
)

// A remote MCP server's headers and a hook's tags are signed bundle content:
// editing either in a signed tree leaves the manifest's signature intact but
// the TREE unverified.
func TestVerifyBundle_EditedMCPHeaderOrHookTagInASignedTreeIsCaught(t *testing.T) {
	for _, tc := range []struct{ name, path, from, to string }{
		{"mcp header", "mcp/remote.yaml", "Bearer t0ken", "Bearer attacker"},
		{"hook tag", "hooks/session_start/.warmup.meta.yaml", "link_id=remote", "link_id=other"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, _, fsys := fixture(t)
			put := func(kind trust.ItemKind, name string, s content.Surface) {
				require.NoError(t, store.Put(ctx, trust.Ref{Bundle: "code-quality", Kind: kind, Name: name, IsLocal: true}, signing.FormRaw, s))
			}
			put(trust.KindMCP, "remote", content.MCP{Name: "remote", URL: "https://mcp.example.com/mcp",
				Headers: map[string]string{"Authorization": "Bearer t0ken"}, Tags: []string{"ctxloom:link_id=remote"}})
			put(trust.KindHook, "session_start/warmup", content.Hook{Event: "session_start", Name: "warmup",
				Type: "command", Command: "warm", Tags: []string{"ctxloom:link_id=remote"}})
			b, err := store.Open(ctx, "code-quality")
			require.NoError(t, err)

			signer, pub := testSigner(t)
			require.NoError(t, SignBundle(ctx, store, b, fixtureRel(t), signer))
			v, err := VerifyBundle(ctx, b, rootTrusting(publisher("pub@example.test", pub)), now)
			require.NoError(t, err)
			require.True(t, v.OK(), "the signed tree verifies before the edit")

			p := storeRoot + "/code-quality/" + tc.path
			raw, err := afero.ReadFile(fsys, p)
			require.NoError(t, err)
			require.Contains(t, string(raw), tc.from)
			require.NoError(t, afero.WriteFile(fsys, p, []byte(strings.ReplaceAll(string(raw), tc.from, tc.to)), 0o644))

			v, err = VerifyBundle(ctx, b, rootTrusting(publisher("pub@example.test", pub)), now)
			require.NoError(t, err)
			require.Error(t, v.Contents, "an edited %s must not verify", tc.name)
			require.False(t, v.OK())
		})
	}
}
