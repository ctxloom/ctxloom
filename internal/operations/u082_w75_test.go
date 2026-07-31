package operations

import (
	"context"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/paths"
)

// setupHeadingTestFS builds a bundle whose commands lead with each of the
// shapes GetCommand's title-stripping has to tell apart: a real ATX H1, an H2
// sub-heading, a shebang, and a bare "#tag" word. Only the H1 is a title.
func setupHeadingTestFS(t *testing.T) *bundles.Loader {
	t.Helper()
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll(paths.LocalBundlesPath(testBaseDir), 0755))

	bundleContent := `version: "1.0"
description: Heading-stripping fixtures
commands:
  h1-title:
    description: leads with a real H1
    content: |
      # Code Review
      body of the h1 command
  h2-first:
    description: leads with an H2 sub-heading
    content: |
      ## Deep Dive
      body of the h2 command
  shebang-first:
    description: leads with a shebang
    content: |
      #!/usr/bin/env bash
      echo hello
  hashtag-first:
    description: leads with a bare hashtag word
    content: |
      #urgent
      body of the hashtag command
`
	require.NoError(t, afero.WriteFile(fs,
		paths.LocalBundlesPath(testBaseDir)+"/headings.yaml", []byte(bundleContent), 0644))

	return bundles.NewLoader([]string{paths.LocalBundlesPath(testBaseDir)}, false, bundles.WithFS(fs))
}

// TestGetCommand_StripsOnlyAnATXH1 pins U082-F10: the "drop a single leading H1
// title line" cleanup must recognise an ATX H1 (a run of exactly one '#'
// followed by space/tab or end of line) and nothing else. A prefix test on "#"
// alone also eats an H2 sub-heading, a shebang, and a "#tag" word — silently
// deleting the first real line of the command body.
func TestGetCommand_StripsOnlyAnATXH1(t *testing.T) {
	loader := setupHeadingTestFS(t)

	cases := []struct {
		name    string
		cmd     string
		rawLead string // §11k: what the loader hands the code under test
		want    string
	}{
		{
			name:    "real H1 title is dropped",
			cmd:     "headings#commands/h1-title",
			rawLead: "# Code Review",
			want:    "body of the h1 command",
		},
		{
			name:    "H2 sub-heading is body, not a title",
			cmd:     "headings#commands/h2-first",
			rawLead: "## Deep Dive",
			want:    "## Deep Dive\nbody of the h2 command",
		},
		{
			name:    "shebang is body, not a title",
			cmd:     "headings#commands/shebang-first",
			rawLead: "#!/usr/bin/env bash",
			want:    "#!/usr/bin/env bash\necho hello",
		},
		{
			name:    "bare hashtag word is body, not a title",
			cmd:     "headings#commands/hashtag-first",
			rawLead: "#urgent",
			want:    "#urgent\nbody of the hashtag command",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// §11k: prove the fixture is hostile from GetCommand's vantage —
			// the loader really does deliver the leading line under test.
			raw, err := loader.GetCommand(tc.cmd)
			require.NoError(t, err)
			require.True(t, strings.HasPrefix(raw.Content, tc.rawLead),
				"fixture never reached the stripper: raw content = %q", raw.Content)

			res, err := GetCommand(context.Background(), nil, GetCommandRequest{
				Name:   tc.cmd,
				Loader: loader,
			})
			require.NoError(t, err)
			assert.Equal(t, tc.want, res.Content)
		})
	}
}
