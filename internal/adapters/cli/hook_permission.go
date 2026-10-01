package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/adapters/runner"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// hookPermissionProg names this hook on the clidiag warning channel.
const hookPermissionProg = "ctxloom hook permission"

// errHookRefused is the runner's refusal of an ask: its route decided
// nothing for it.
var errHookRefused = errors.New("the runner's approval route refused the ask")

var hookPermissionCmd = &cobra.Command{
	Use:    "permission",
	Hidden: true, // Machine callback (the approval hooks) — not for direct use
	Short:  "Carry the engine's permission ask to the human at the root (internal — used by the approval hooks)",
	Long: `Hands the engine's hook payload for --event, verbatim, to the runner hosting
this run (the session endpoint's approval hook, named by CTXLOOM_HOOK_URL,
under CTXLOOM_HOOK_TOKEN), which parks it for the human at the root and
answers with the engine's native decision; that answer is written to stdout
untouched.

Any failure — no endpoint, an unreachable runner, a refusal — writes nothing
and exits 0: no decision, so the engine's held permission host denies the
call. The reason is named on stderr.`,
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, _ []string) error {
		event, _ := cmd.Flags().GetString("event")
		if err := runHookPermission(cmd.Context(), event, cmd.InOrStdin(), cmd.OutOrStdout(), os.Getenv, http.DefaultClient); err != nil {
			clidiag.Warn(hookPermissionProg, "%v", err)
		}
		return nil
	},
}

// runHookPermission posts the hook's stdin to the runner's approval hook for
// event and copies the answer to stdout. It writes stdout only from a
// complete, successful answer; on any error stdout is untouched.
func runHookPermission(ctx context.Context, event string, stdin io.Reader, stdout io.Writer, getenv func(string) string, client *http.Client) error {
	if ctx == nil {
		ctx = context.Background()
	}
	hook, err := sessions.DecodeHookReach(getenv)
	if err != nil {
		return err
	}
	target, err := url.Parse(hook.URL)
	if err != nil {
		return fmt.Errorf("the approval hook address %q: %w", hook.URL, err)
	}
	target.RawQuery = url.Values{runner.HookEventParam: {event}}.Encode()
	payload, err := io.ReadAll(stdin)
	if err != nil {
		return fmt.Errorf("read the engine's payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+hook.Credential)
	req.Header.Set("Content-Type", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("reach the runner's approval hook: %w", err)
	}
	defer res.Body.Close()
	answer, err := io.ReadAll(res.Body)
	if err != nil {
		return fmt.Errorf("read the runner's answer: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("%w (%s): %s", errHookRefused, res.Status, strings.TrimSpace(string(answer)))
	}
	_, err = stdout.Write(answer)
	return err
}

func init() {
	hookPermissionCmd.Flags().String("event", "", "the engine's hook event whose payload stdin carries")
	hookCmd.AddCommand(hookPermissionCmd)
}
