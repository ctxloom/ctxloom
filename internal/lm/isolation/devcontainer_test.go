package isolation

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeDevcontainer writes appRoot/.devcontainer/devcontainer.json.
func writeDevcontainer(t *testing.T, appRoot, content string) {
	t.Helper()
	dir := filepath.Join(appRoot, ".devcontainer")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "devcontainer.json"), []byte(content), 0o644))
}

// TestFindDevcontainerJSON pins the two canonical single-config paths and
// their precedence (.devcontainer/devcontainer.json over .devcontainer.json).
func TestFindDevcontainerJSON(t *testing.T) {
	root := t.TempDir()
	assert.Empty(t, findDevcontainerJSON(root), "absent → empty")

	require.NoError(t, os.WriteFile(filepath.Join(root, ".devcontainer.json"), []byte(`{}`), 0o644))
	assert.Equal(t, filepath.Join(root, ".devcontainer.json"), findDevcontainerJSON(root))

	writeDevcontainer(t, root, `{}`)
	assert.Equal(t, filepath.Join(root, ".devcontainer", "devcontainer.json"), findDevcontainerJSON(root),
		".devcontainer/devcontainer.json wins over the single-file form")
}

// TestStripJSONC pins the comment/trailing-comma stripping, string-aware so
// it never mangles content that merely LOOKS like a comment or a comma.
func TestStripJSONC(t *testing.T) {
	in := `{
		// a comment
		"image": "debian:13", // trailing comment
		/* block
		   comment */
		"build": {
			"dockerfile": "Dockerfile",
			"args": {"A": "1",},
		},
		"note": "not // a comment, not /* either */",
	}`
	var out struct {
		Image string `json:"image"`
		Build struct {
			Dockerfile string            `json:"dockerfile"`
			Args       map[string]string `json:"args"`
		} `json:"build"`
		Note string `json:"note"`
	}
	require.NoError(t, json.Unmarshal(stripJSONC([]byte(in)), &out))
	assert.Equal(t, "debian:13", out.Image)
	assert.Equal(t, "Dockerfile", out.Build.Dockerfile)
	assert.Equal(t, "1", out.Build.Args["A"])
	assert.Equal(t, "not // a comment, not /* either */", out.Note, "content inside a string is never stripped")
}

// TestResolveDevcontainerBase_Absent: no devcontainer.json → (nil, nil), the
// caller falls through to the next source.
func TestResolveDevcontainerBase_Absent(t *testing.T) {
	stage, err := resolveDevcontainerBase(t.TempDir(), "")
	require.NoError(t, err)
	assert.Nil(t, stage)
}

// TestResolveDevcontainerBase_Image pins the "image:" shape: a synthetic FROM
// base (a BASE, not a finished agent image — engine fragments still layer on
// top, unlike the --base-image overlay escape hatch).
func TestResolveDevcontainerBase_Image(t *testing.T) {
	root := t.TempDir()
	writeDevcontainer(t, root, `{"image": "mcr.microsoft.com/devcontainers/go:1"}`)
	stage, err := resolveDevcontainerBase(root, "")
	require.NoError(t, err)
	require.NotNil(t, stage)
	assert.Equal(t, baseStageKindDevcontainer, stage.kind)
	assert.Empty(t, stage.path)
	assert.Equal(t, "FROM mcr.microsoft.com/devcontainers/go:1\n", string(stage.containerfile))
}

// TestResolveDevcontainerBase_Build pins the "build:" shape: the named
// Dockerfile becomes the base, with the devcontainer's own context dir and
// build args threaded through.
func TestResolveDevcontainerBase_Build(t *testing.T) {
	root := t.TempDir()
	writeDevcontainer(t, root, `{
		"build": {
			"dockerfile": "Dockerfile",
			"context": "..",
			"args": {"VARIANT": "bullseye"}
		}
	}`)
	require.NoError(t, os.WriteFile(filepath.Join(root, "Dockerfile"), []byte("FROM debian\n"), 0o644))

	stage, err := resolveDevcontainerBase(root, "")
	require.NoError(t, err)
	require.NotNil(t, stage)
	assert.Equal(t, baseStageKindDevcontainer, stage.kind)
	assert.Equal(t, filepath.Join(root, ".devcontainer", "Dockerfile"), stage.path)
	assert.Equal(t, root, stage.context, "build.context resolves relative to the devcontainer dir")
	assert.Contains(t, stage.buildArgs, "VARIANT=bullseye")
}

// TestResolveDevcontainerBase_BuildDefaultContext: build.context defaults to
// the devcontainer.json's own directory when unset.
func TestResolveDevcontainerBase_BuildDefaultContext(t *testing.T) {
	root := t.TempDir()
	writeDevcontainer(t, root, `{"build": {"dockerfile": "Dockerfile"}}`)
	dcDir := filepath.Join(root, ".devcontainer")
	require.NoError(t, os.WriteFile(filepath.Join(dcDir, "Dockerfile"), []byte("FROM debian\n"), 0o644))

	stage, err := resolveDevcontainerBase(root, "")
	require.NoError(t, err)
	require.NotNil(t, stage)
	assert.Equal(t, dcDir, stage.context)
}

// TestResolveDevcontainerBase_ComposeWithoutService: dockerComposeFile with NO
// resolvable service (neither the caller's service arg nor the devcontainer's
// own "service" key) FAILS LOUD — a multi-service compose project does not
// map to one agent container.
func TestResolveDevcontainerBase_ComposeWithoutService(t *testing.T) {
	root := t.TempDir()
	writeDevcontainer(t, root, `{"dockerComposeFile": "docker-compose.yml"}`)
	_, err := resolveDevcontainerBase(root, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dockerComposeFile")
	assert.Contains(t, err.Error(), "isolation_devcontainer_service")
}

// TestResolveDevcontainerBase_ComposeWithService resolves the named service's
// image or build from the compose file, via the caller's explicit service
// pick OR the devcontainer.json's own "service" key.
func TestResolveDevcontainerBase_ComposeWithService(t *testing.T) {
	root := t.TempDir()
	writeDevcontainer(t, root, `{"dockerComposeFile": "docker-compose.yml", "service": "app"}`)
	compose := `
services:
  app:
    image: debian:13
  db:
    image: postgres:16
`
	require.NoError(t, os.WriteFile(filepath.Join(root, ".devcontainer", "docker-compose.yml"), []byte(compose), 0o644))

	stage, err := resolveDevcontainerBase(root, "")
	require.NoError(t, err)
	require.NotNil(t, stage)
	assert.Equal(t, "FROM debian:13\n", string(stage.containerfile))

	// An explicit --devcontainer-service / isolation_devcontainer_service
	// overrides the devcontainer.json's own "service" key.
	stage2, err := resolveDevcontainerBase(root, "db")
	require.NoError(t, err)
	require.NotNil(t, stage2)
	assert.Equal(t, "FROM postgres:16\n", string(stage2.containerfile))

	// An unknown service name errors rather than silently building nothing.
	_, err = resolveDevcontainerBase(root, "nope")
	require.Error(t, err)
}

// TestResolveDevcontainerBase_ComposeServiceBuild resolves a compose
// service's "build" shape (both the bare-string and the object forms).
func TestResolveDevcontainerBase_ComposeServiceBuild(t *testing.T) {
	root := t.TempDir()
	writeDevcontainer(t, root, `{"dockerComposeFile": "docker-compose.yml", "service": "app"}`)
	compose := `
services:
  app:
    build:
      context: .
      dockerfile: Dockerfile.app
      args:
        FOO: bar
`
	require.NoError(t, os.WriteFile(filepath.Join(root, ".devcontainer", "docker-compose.yml"), []byte(compose), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".devcontainer", "Dockerfile.app"), []byte("FROM debian\n"), 0o644))

	stage, err := resolveDevcontainerBase(root, "")
	require.NoError(t, err)
	require.NotNil(t, stage)
	assert.Equal(t, filepath.Join(root, ".devcontainer", "Dockerfile.app"), stage.path)
	assert.Contains(t, stage.buildArgs, "FOO=bar")
}

// TestResolveDevcontainerBase_MalformedJSON: malformed devcontainer.json is a
// hard error — never a silent fallback to the default base.
func TestResolveDevcontainerBase_MalformedJSON(t *testing.T) {
	root := t.TempDir()
	writeDevcontainer(t, root, `{ this is not json `)
	_, err := resolveDevcontainerBase(root, "")
	require.Error(t, err)
}

// TestResolveDevcontainerBase_UnrecognizedShape: a devcontainer.json with
// none of image/build/dockerComposeFile is an error, not a silent no-op.
func TestResolveDevcontainerBase_UnrecognizedShape(t *testing.T) {
	root := t.TempDir()
	writeDevcontainer(t, root, `{"name": "just a name, no base shape"}`)
	_, err := resolveDevcontainerBase(root, "")
	require.Error(t, err)
}

// TestResolveDevcontainerBase_FeaturesWarnedNotFatal pins the pre1 D1
// decision: "features" present is WARNED (never silently honored) but never
// blocks the build by itself — the resolved base (from image/build) still
// applies.
func TestResolveDevcontainerBase_FeaturesWarnedNotFatal(t *testing.T) {
	root := t.TempDir()
	writeDevcontainer(t, root, `{
		"image": "debian:13",
		"features": {"ghcr.io/devcontainers/features/git:1": {}}
	}`)
	stage, err := resolveDevcontainerBase(root, "")
	require.NoError(t, err, "features present must NOT fail the build by itself")
	require.NotNil(t, stage)
	assert.Equal(t, "FROM debian:13\n", string(stage.containerfile))
}

// TestResolveDevBase_OptOut pins the opt-out gate: NoDevcontainerBase or an
// empty appRoot means "no auto-detect", never an error — even over a present
// devcontainer.json that would otherwise error.
func TestResolveDevBase_OptOut(t *testing.T) {
	root := t.TempDir()
	writeDevcontainer(t, root, `{ malformed`)

	stage, err := resolveDevBase(root, true, "")
	require.NoError(t, err)
	assert.Nil(t, stage)

	stage, err = resolveDevBase("", false, "")
	require.NoError(t, err)
	assert.Nil(t, stage)

	// Without the opt-out, the same malformed file DOES error.
	_, err = resolveDevBase(root, false, "")
	require.Error(t, err)
}
