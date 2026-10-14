package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/afero"
	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/adapters/runner"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/core/spool"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
)

// mailPeekProg names this hook on the clidiag warning channel.
const mailPeekProg = "ctxloom hook mail-peek"

// mailPeekTimeout bounds one exchange with the runner: the hook sits between
// a tool call and the model, so a runner that does not answer costs this and
// no more.
const mailPeekTimeout = 3 * time.Second

var hookMailPeekCmd = &cobra.Command{
	Use:    "mail-peek",
	Hidden: true, // Machine callback (post_tool hook) — not for direct use
	Short:  "Hand the engine mail that arrived during its turn, at a tool call (internal — used by the post_tool hook)",
	Long: `Fires after each tool call and, when mail waits for this session, writes it
as context of the turn in flight — so a message that arrives during a long
turn is read before the turn ends rather than after it.

With no mail waiting it costs two directory listings and writes nothing.

The session owner's mail is claimed from its own spool, written, and
acknowledged, as the turn-start drain does. A runner-hosted run's mail is its
runner's to read: the hook asks the runner (CTXLOOM_HOOK_URL's origin, under
CTXLOOM_HOOK_TOKEN) for what waits, writes it, and only then confirms; mail
it did not confirm is delivered at the run's next turn boundary as before.

What does not fit the engine's per-hook context cap waits for the next tool
call. The hook never fails the tool call: every problem is named on stderr.`,
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runHookMailPeek,
}

// mailPeekEnv is what peekMail reads beyond the command: the filesystem the
// spool is on, the process environment, whether this engine is the session
// owner's, and the client that reaches the runner.
type mailPeekEnv struct {
	fs     afero.Fs
	getenv func(string) string
	owner  bool
	client *http.Client
}

func runHookMailPeek(cmd *cobra.Command, _ []string) error {
	kind, err := firingEngine(cmd)
	if err != nil {
		_, _ = io.Copy(io.Discard, cmd.InOrStdin())
		clidiag.Warn(mailPeekProg, "no mail delivered: %v", err)
		return nil
	}
	env := mailPeekEnv{fs: afero.NewOsFs(), getenv: os.Getenv, owner: sessionOwnerEnv(), client: &http.Client{Timeout: mailPeekTimeout}}
	if err := peekMail(cmd, kind.Hooks(), env); err != nil {
		clidiag.Warn(mailPeekProg, "%v", err)
	}
	return nil
}

// peekMail does the work and returns its failure rather than warning, so a
// test can assert which reason fired. Silence — no stdout at all — is the
// answer whenever nothing waits or nothing may be read.
func peekMail(cmd *cobra.Command, codec engine.HookCodec, env mailPeekEnv) error {
	// Read to EOF before anything can return: closing stdin early is reported
	// by some engines as a failed hook.
	_, _ = io.Copy(io.Discard, cmd.InOrStdin())
	harp := env.getenv(sessions.EnvHarp)
	if harp == "" {
		return nil // an engine ctxloom did not launch: no spool is ours to name
	}
	mapper := spool.NewHomeMapper()
	// THE CHEAP CHECK: two directory listings, nothing parsed, no request.
	// Everything below runs only when mail actually waits.
	has, err := spool.HasMail(env.fs, mapper, harp)
	if err != nil {
		return fmt.Errorf("no mail delivered: %w", err)
	}
	if !has {
		return nil
	}
	if env.owner {
		return peekOwnerMail(env.fs, cmd, codec, mapper, harp)
	}
	hook, err := sessions.DecodeHookReach(env.getenv)
	if err != nil {
		// Not the owner and not runner-hosted (an engine loading a trusted
		// repository's own settings): its spool is nobody's this hook may
		// read, and there is no runner to ask.
		return nil
	}
	return peekRunnerMail(cmd, codec, hook, env.client)
}

// peekOwnerMail is the session owner's mid-turn delivery: drainMail's
// claim-and-ack, capped at one hook's context. What fits is written and then
// acknowledged; the rest stays claimed, where the next tool call's (or the
// next turn's) Claim hands it out again, first.
func peekOwnerMail(fs afero.Fs, cmd *cobra.Command, codec engine.HookCodec, mapper spool.PathMapper, harp string) error {
	res, err := spool.Claim(fs, mapper, harp)
	if err != nil {
		return fmt.Errorf("no mail delivered: %w", err)
	}
	var problems []string
	for _, p := range res.Problems {
		problems = append(problems, p.Error())
	}
	if len(res.Entries) == 0 {
		return joinProblems(problems)
	}
	frames := make([]string, len(res.Entries))
	for i, e := range res.Entries {
		frames[i] = runner.FrameCoordinatorDelivery(coord.Message{
			From: e.Message.FromHarp, Kind: e.Message.Kind, ID: e.Identity(), InReplyTo: e.Message.InReplyTo, Body: e.Message.Body,
		})
	}
	text, n := runner.MidTurnMailContext(frames, codec.ContextLimit())
	if text == "" {
		return joinProblems(problems)
	}
	if err := writeHookResponse(cmd, codec, wire.HookEventPostTool, engine.HookResponse{Context: text}); err != nil {
		return fmt.Errorf("%d message(s) left claimed, not delivered: %w", len(res.Entries), err)
	}
	if n == 0 {
		return joinProblems(problems)
	}
	problems = append(problems, ackDelivered(fs, mapper, harp, res.Entries[:n])...)
	// This delivery answers whatever wake announced the same mail; an armed
	// wake left behind would later wake the owner for mail already read.
	if _, err := spool.ClearWakes(fs, mapper, harp); err != nil {
		problems = append(problems, fmt.Sprintf("the wakes this delivery answered could not be cleared, and may refuse the next wake: %v", err))
	}
	return joinProblems(problems)
}

// peekRunnerMail is a runner-hosted run's mid-turn delivery. The runner reads
// the spool; the hook only asks it (runner.MailPath on the hook address's
// origin), writes what it was handed, and THEN confirms. A failed write
// confirms nothing, and the runner delivers the mail at the turn boundary.
func peekRunnerMail(cmd *cobra.Command, codec engine.HookCodec, hook sessions.Endpoint, client *http.Client) error {
	target, err := url.Parse(hook.URL)
	if err != nil || target.Host == "" {
		return fmt.Errorf("no mail delivered: the hook address %q names no runner", hook.URL)
	}
	target.Path, target.RawQuery = runner.MailPath, ""
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	var peek runner.MailPeek
	if err := postMail(ctx, client, target.String(), hook.Credential, runner.MailPeekRequest{Op: runner.MailOpFetch, MaxChars: codec.ContextLimit()}, &peek); err != nil {
		return fmt.Errorf("no mail delivered: %w", err)
	}
	if peek.Context == "" {
		return nil
	}
	if err := writeHookResponse(cmd, codec, wire.HookEventPostTool, engine.HookResponse{Context: peek.Context}); err != nil {
		return fmt.Errorf("%d message(s) not delivered mid-turn, left for the turn boundary: %w", len(peek.IDs), err)
	}
	if len(peek.IDs) == 0 {
		return nil
	}
	var confirmed runner.MailConfirmed
	if err := postMail(ctx, client, target.String(), hook.Credential, runner.MailPeekRequest{Op: runner.MailOpConfirm, Lease: peek.Lease, IDs: peek.IDs}, &confirmed); err != nil {
		return fmt.Errorf("%d message(s) were written but not confirmed, and will be delivered again at the turn boundary: %w", len(peek.IDs), err)
	}
	if confirmed.Confirmed < len(peek.IDs) {
		return fmt.Errorf("%d of %d message(s) written mid-turn were not confirmed (their turn took them first) and may be read twice", len(peek.IDs)-confirmed.Confirmed, len(peek.IDs))
	}
	return nil
}

// postMail is one exchange with the runner's mail endpoint.
func postMail(ctx context.Context, client *http.Client, target, bearer string, req runner.MailPeekRequest, out any) error {
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return err
	}
	r.Header.Set("Authorization", "Bearer "+bearer)
	r.Header.Set("Content-Type", "application/json")
	if client == nil {
		client = &http.Client{Timeout: mailPeekTimeout}
	}
	res, err := client.Do(r)
	if err != nil {
		return fmt.Errorf("reach the runner's mail endpoint: %w", err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("read the runner's answer: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("the runner's mail endpoint answered %s: %s", res.Status, strings.TrimSpace(string(raw)))
	}
	return json.Unmarshal(raw, out)
}

func init() {
	hookCmd.AddCommand(hookMailPeekCmd)
}
