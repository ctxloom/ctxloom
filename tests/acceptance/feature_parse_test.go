package acceptance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	gherkin "github.com/cucumber/gherkin/go/v26"
	messages "github.com/cucumber/messages/go/v21"
	"github.com/stretchr/testify/require"
)

// TestFeatureFilesParse reads every .feature file with the SAME parser godog
// uses, and fails naming the file and position when one will not parse.
//
// It exists because a parse error is invisible until the whole suite runs, and
// then it is not obviously a parse error: godog reports the file and aborts
// before a single scenario executes, so the run ends in seconds with a failure
// that looks like a harness fault. `go vet` and `just test-pkg` cannot see it
// at all — a .feature file is data, not code.
//
// Two ways this has actually bitten:
//
//   - a header sentence wrapped so a line began "@container tag (...)", which
//     the parser reads as a TAG LINE and rejects with "a tag may not contain
//     whitespace" — silently taking all twelve of that file's scenarios with it;
//   - a merge left conflict markers in a feature file, and the suite spent its
//     startup discovering that instead of running.
//
// Both are the corpus-level form of this project's characteristic bug: work
// that never runs is indistinguishable from work that passed. This test turns
// a 180-second mystery into a one-second message.
func TestFeatureFilesParse(t *testing.T) {
	var files []string
	require.NoError(t, filepath.WalkDir("features", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".feature") {
			files = append(files, path)
		}
		return nil
	}))
	require.NotEmpty(t, files, "the corpus is discovered by walking features/; an empty walk means this test is asserting nothing")

	for _, path := range files {
		t.Run(path, func(t *testing.T) {
			f, err := os.Open(path)
			require.NoError(t, err)
			defer f.Close()

			_, err = gherkin.ParseGherkinDocument(f, (&messages.Incrementing{}).NewId)
			require.NoError(t, err, "%s does not parse; godog aborts the whole suite on this, before any scenario runs", path)
		})
	}
}
