package sessions

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestStore_BindMCP_RecordsTheEndpoint_EveryAdapter: the session's MCP
// endpoint is bound on the entry by the launch resolver and read back by a
// resume; the sidecar round-trips it (session.yaml records the endpoint).
func TestStore_BindMCP_RecordsTheEndpoint_EveryAdapter(t *testing.T) {
	adapters := []struct {
		name string
		make func(t *testing.T) Store
	}{
		{"MemStore", func(t *testing.T) Store { return NewMemStore() }},
		{"Manager", func(t *testing.T) Store {
			requireIsolatedSessionRoot(t)
			m, err := Open(nil)
			require.NoError(t, err)
			return m
		}},
	}
	for _, a := range adapters {
		t.Run(a.name, func(t *testing.T) {
			s := a.make(t)
			e, err := s.AssignHarp("/proj", "")
			require.NoError(t, err)

			ep := Endpoint{URL: "http://127.0.0.1:41234/mcp", Credential: "bearer-1"}
			require.NoError(t, s.BindMCP(e.HarpName, ep))
			require.NoError(t, s.BindEngine(e.HarpName, "mock"))

			got, err := s.Find(e.HarpName)
			require.NoError(t, err)
			require.Equal(t, ep, got.MCP)
			require.Equal(t, "mock", got.Backend)

			require.ErrorIs(t, s.BindMCP("no-such-harp", ep), ErrNotFound)
		})
	}
}

// TestEntry_MCP_RoundTripsThroughTheSidecar: the endpoint survives a
// write and a read of session.yaml, credential included, and is omitted
// when unbound.
func TestEntry_MCP_RoundTripsThroughTheSidecar(t *testing.T) {
	requireIsolatedSessionRoot(t)
	m, err := Open(nil)
	require.NoError(t, err)
	e, err := m.AssignHarp("/proj", "mock")
	require.NoError(t, err)

	unbound, err := m.readSidecar(e.HarpName)
	require.NoError(t, err)
	require.Zero(t, unbound.MCP)

	ep := Endpoint{URL: "http://127.0.0.1:41234/mcp", Credential: "bearer-1"}
	require.NoError(t, m.BindMCP(e.HarpName, ep))
	bound, err := m.readSidecar(e.HarpName)
	require.NoError(t, err)
	require.Equal(t, ep, bound.MCP)
}
