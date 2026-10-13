package kit

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/wire"
)

func TestMCPTable_Claims(t *testing.T) {
	tbl := MCPTable{Key: "servers", Entry: func(name string, s wire.MCPServer) (any, error) {
		return map[string]any{"cmd": s.Command, "as": name}, nil
	}}
	servers := map[string]wire.MCPServer{"b": {Command: "bb", SCM: "bundle-b"}, "a": {Command: "aa"}}

	claims, err := tbl.Claims(servers)
	require.NoError(t, err)
	assert.Equal(t, []present.Claim{
		{Pointer: "/servers/a", Value: map[string]any{"cmd": "aa", "as": "a"}},
		{Pointer: "/servers/b", Value: map[string]any{"cmd": "bb", "as": "b"}, Via: "bundle-b"},
	}, claims, "one claim per server, in name order, through the bundle that shipped it")

	claims, err = tbl.Claims(nil)
	require.NoError(t, err)
	assert.Empty(t, claims, "no server and no Empty table: nothing is claimed")

	tbl.Empty = map[string]any{}
	claims, err = tbl.Claims(nil)
	require.NoError(t, err)
	assert.Equal(t, []present.Claim{{Pointer: "/servers", Value: map[string]any{}}}, claims)

	claims, err = tbl.Claims(servers)
	require.NoError(t, err)
	assert.Len(t, claims, 2, "the Empty table is claimed only when there is no server")
}

func TestMCPTable_EntryErrorRefusesTheTable(t *testing.T) {
	boom := errors.New("no dialect")
	tbl := MCPTable{Key: "servers", Entry: func(string, wire.MCPServer) (any, error) { return nil, boom }}
	_, err := tbl.Claims(map[string]wire.MCPServer{"a": {}})
	require.ErrorIs(t, err, boom)
}
