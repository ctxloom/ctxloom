package main

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// `taskloom watch` accepted every --format value the root's persistent flag
// admits and then emitted JSONL regardless, so `--format yaml` got a
// confident, wrong answer with no diagnostic — the shape this project's
// silent-no-op family takes on a read. The stream is a JSONL wire contract a
// GUI subscribes to; there is no yaml or toml rendering of it to give, so the
// honest answer is to say so.
func TestWatch_RejectsAFormatItCannotProduce(t *testing.T) {
	for _, format := range []string{"yaml", "toml", "markdown"} {
		t.Run(format, func(t *testing.T) {
			c := &cobra.Command{}
			c.Flags().String("format", format, "")
			err := checkWatchFormat(c)
			require.Error(t, err, "answering a %s request with JSONL is a silent wrong answer", format)
			assert.Contains(t, err.Error(), format, "the rejection must name the format asked for")
			assert.Contains(t, err.Error(), "JSONL", "and say what the stream actually is")
		})
	}
}

// The two values that DO map onto the stream keep working untouched — text is
// the default every existing subscriber invokes with, and its bytes are the
// JSONL contract, so rejecting it would break the very consumer this command
// exists for.
func TestWatch_AcceptsTheFormatsThatMapOntoTheStream(t *testing.T) {
	for _, format := range []string{"", "text", "json"} {
		c := &cobra.Command{}
		c.Flags().String("format", "text", "")
		if format != "" {
			require.NoError(t, c.Flags().Set("format", format))
		}
		assert.NoError(t, checkWatchFormat(c), "--format %q must stay accepted", format)
	}
}
