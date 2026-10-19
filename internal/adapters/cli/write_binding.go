package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/core/config"
)

// writeConfigEntry is the shared spine of `agent create`/`agent edit` and
// `llm create`/`llm edit`: load the config, enforce the create-vs-edit
// precondition, build and apply the write, then emit the written entry. What
// differs per entity stays with the caller: check (agents and llm labels
// resolve "exists" against different views), set (builds its own request and
// calls its own operation), and the optional afterWrite advisory that only
// an agent write has (nil for none; it runs before the entry is emitted).
func writeConfigEntry[E any](
	cmd *cobra.Command,
	mustExist bool,
	check func(cfg *config.Config) error,
	set func(cfg *config.Config) (*E, error),
	afterWrite func(),
	render func(out io.Writer, entry *E, edited bool) error,
) error {
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	if err := check(cfg); err != nil {
		return err
	}
	entry, err := set(cfg)
	if err != nil {
		return err
	}
	if afterWrite != nil {
		afterWrite()
	}
	return emit(cmd, entry, func() error {
		return render(cmd.OutOrStdout(), entry, mustExist)
	})
}
