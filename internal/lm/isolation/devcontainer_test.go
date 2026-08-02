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

// TestStripJSONC_Characterization exercises every arm of the two JSONC
// strippers at the ONE public seam above them (stripJSONC), including the
// string-literal states — escapes, an escaped backslash before a closing
// quote, delimiters and commas that appear INSIDE a string — and the
// degenerate inputs (an unterminated string, an unterminated block comment,
// a comment running to EOF).
//
// It is the behaviour-preservation pin for a de-duplication: the two
// strippers hand-rolled the same string-aware scanner, and both exceeded the
// CCN gate. A pure de-duplication cannot make any test go red, so this covers
// the arms first and must stay green on both sides of the collapse.
func TestStripJSONC_Characterization(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain json untouched", `{"a":1}`, `{"a":1}`},
		{"line comment collapses to a newline", "{\"a\":1} // tail", "{\"a\":1} \n"},
		{"line comment at EOF with no newline", "// only", "\n"},
		{"block comment vanishes", "{/* x */\"a\":1}", `{"a":1}`},
		{"unterminated block comment eats the rest", "{\"a\":1/* x", `{"a":1`},
		{"delimiters inside a string survive", `{"a":"// /* */ ,"}`, `{"a":"// /* */ ,"}`},
		{"escaped quote does not end the string", `{"a":"x\"// y","b":1}`, `{"a":"x\"// y","b":1}`},
		{"escaped backslash DOES end the string", `{"a":"x\\","b":1}`, `{"a":"x\\","b":1}`},
		{"unterminated string is copied verbatim", `{"a":"x`, `{"a":"x`},
		{"trailing comma before brace", `{"a":1,}`, `{"a":1}`},
		{"trailing comma before bracket across whitespace", "[1, \n\t ]", "[1 \n\t ]"},
		{"comma inside a string is kept", `{"a":"1,}"}`, `{"a":"1,}"}`},
		{"non-trailing comma is kept", `{"a":1,"b":2}`, `{"a":1,"b":2}`},
		// Comments strip FIRST, so a comma the comment separated from the
		// brace becomes a trailing one and is dropped on the second pass.
		{"comma separated from the brace by a comment still trails", "{\"a\":1, /* c */ }", "{\"a\":1  }"},
		{"empty input", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, string(stripJSONC([]byte(tc.in))))
		})
	}
}

// TestResolveDevcontainerBase_UnparseableComposeFileNamesTheCause pins that
// decodeComposeFileList discarded the array-decode error, so a
// dockerComposeFile of an unsupported JSON shape surfaced only as the generic
// "could not parse dockerComposeFile" — the human was told the file is wrong
// but never what about it was wrong.
func TestResolveDevcontainerBase_UnparseableComposeFileNamesTheCause(t *testing.T) {
	root := t.TempDir()
	writeDevcontainer(t, root, `{"dockerComposeFile": 42, "service": "app"}`)

	_, err := resolveDevcontainerBase(root, "")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not parse dockerComposeFile")
	assert.Contains(t, err.Error(), "cannot unmarshal", "the decode cause must survive into the message")
}

// TestResolveDevcontainerBase_EmptyComposeFileNamesTheShape: a syntactically
// valid but empty dockerComposeFile decodes without error, so there is no
// decode cause to report — the message must still say what shape was needed
// rather than trailing off after "could not parse".
func TestResolveDevcontainerBase_EmptyComposeFileNamesTheShape(t *testing.T) {
	root := t.TempDir()
	writeDevcontainer(t, root, `{"dockerComposeFile": "", "service": "app"}`)

	_, err := resolveDevcontainerBase(root, "")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not parse dockerComposeFile")
	assert.Contains(t, err.Error(), "array of strings", "the message names the shapes the spec allows")
}
