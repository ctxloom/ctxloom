package claude

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecordSpan(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.jsonl")
	content := `{"type":"summary"}
{"type":"user","timestamp":"2026-01-01T00:00:00Z"}
not json at all
{"type":"assistant","timestamp":"2026-01-01T02:00:00.500Z"}
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	start, end, n, err := Adapter{}.RecordSpan(afero.NewOsFs(), path)
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	assert.True(t, start.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)))
	assert.True(t, end.Equal(time.Date(2026, 1, 1, 2, 0, 0, 500000000, time.UTC)))
}

func TestRecordSpan_NoTimestampsIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(`{"type":"summary"}`+"\n"), 0o644))
	_, _, n, err := Adapter{}.RecordSpan(afero.NewOsFs(), path)
	require.NoError(t, err)
	assert.Equal(t, 0, n)
}

func TestRecordSpan_MissingFileErrors(t *testing.T) {
	_, _, _, err := Adapter{}.RecordSpan(afero.NewOsFs(), filepath.Join(t.TempDir(), "does-not-exist.jsonl"))
	require.Error(t, err)
}
