package remote

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newResolveTestRegistry builds a registry with two remotes whose short names
// differ from their URL-derived local names, mirroring a real install.
func newResolveTestRegistry(t *testing.T) *Registry {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "remotes.yaml")
	require.NoError(t, os.WriteFile(path, []byte(
		"remotes:\n"+
			"  personal:\n"+
			"    url: https://github.com/benjaminabbitt/ctxloom-personal\n"+
			"  ctxloom-default:\n"+
			"    url: https://github.com/ctxloom/ctxloom-default\n"), 0o644))
	reg, err := NewRegistry(path)
	require.NoError(t, err)
	return reg
}

func TestResolveItemRemote(t *testing.T) {
	reg := newResolveTestRegistry(t)

	tests := []struct {
		name      string
		localName string
		want      string
		wantOK    bool
	}{
		{"short-form name", "personal/go-developer", "personal", true},
		{"url-derived name", "github.com/benjaminabbitt/ctxloom-personal/go-developer", "personal", true},
		{"url-derived default", "github.com/ctxloom/ctxloom-default/go-developer", "ctxloom-default", true},
		{"local profile, no remote", "go-developer", "", false},
		{"prefix collision is not a match", "personalish/x", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := reg.ResolveItemRemote(tt.localName)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}
