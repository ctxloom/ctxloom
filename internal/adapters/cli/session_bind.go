package cli

import (
	"bytes"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/shared/harpmarker"
	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
)

// The `ctxloom hook session-bind` machinery: a machine callback fired by an
// engine's SessionStart hook. It is NOT part
// of the user-facing `session` command tree — it registers under the hidden
// `hook` namespace — and it answers to a different contract: stdout carries
// hook output only. A bind that cannot happen — a payload the firing engine's
// codec cannot read, an index that refuses the bind — is an error the process
// exits non-zero on: the engine shows a failed hook, and a session that never
// bound is not mistaken for one that did.

func init() {
	// session-bind is a machine callback (SessionStart hook target), so it lives
	// under the hidden `hook` namespace, not the user-facing `session` one.
	hookCmd.AddCommand(sessionBindCmd)
}

// sessionBindCmd is the session-bind hook target and the sole path for
// recording the harp → session_id mapping in the index. Claude Code (and
// other backends with SessionStart hooks) fire this once per TRANSCRIPT, not
// once per session: /clear rotates the transcript to a new UUID under the same
// live process and the hook fires again with the new one. The payload carries
// the backend's session ID and transcript path, so a repeat firing that names
// a new transcript re-points the binding and one that names the same
// transcript is a no-op. The compactor also forward-binds at compact time as a
// backstop.
var sessionBindCmd = &cobra.Command{
	Use:    "session-bind",
	Short:  "Bind the current backend session to the active harp (internal — used by the SessionStart hook)",
	Hidden: true,
	RunE:   runSessionBind,
}

func runSessionBind(cmd *cobra.Command, args []string) error {
	harp := os.Getenv(sessions.EnvHarp)
	kind, err := firingEngine(cmd)
	if err != nil {
		return err
	}
	codec := kind.Hooks()
	// Read the hook payload once: the marker doesn't need it, the bind does.
	raw, err := io.ReadAll(cmd.InOrStdin())
	if err != nil {
		return fmt.Errorf("session-bind: read the hook payload: %w", err)
	}
	// Emit the deterministic harp self-id marker as session-start context so
	// the transcript carries a greppable owner tag, independent of the index,
	// the binding, or PID bookkeeping. This is the session_start hook
	// installed for every ctxloom session, so it identifies the harp whatever
	// else the session starts with. It is written before the bind, so a bind
	// that fails below still leaves the marker on stdout.
	emitHarpMarker(cmd.OutOrStdout(), codec, harp)
	return bindSessionFromPayload(bytes.NewReader(raw), codec, harp)
}

// emitHarpMarker writes the harp self-id marker as the firing engine's
// session-start context, so the engine injects it into the session and it
// lands in the transcript.
//
// The marker is the only index-independent statement of which harp owns a
// transcript, so a run that emits none produces a transcript nothing can
// attribute. Every way of emitting nothing is REPORTED on the diagnostic
// channel; stdout stays the hook's contract channel and never carries a
// diagnostic.
func emitHarpMarker(w io.Writer, codec engine.HookCodec, harp string) {
	marker := harpmarker.Format(harp)
	if marker == "" {
		clidiag.Warn("ctxloom", "session-bind: no usable harp (CTXLOOM_SESSION_HARP=%q) — this session's transcript carries no harp self-id marker and cannot be attributed to a harp by content", harp)
		return
	}
	reply, err := codec.Encode(wire.HookEventSessionStart, engine.HookResponse{Context: marker})
	if err == nil {
		_, err = w.Write(reply.Stdout)
	}
	if err != nil {
		clidiag.Warn("ctxloom", "session-bind: harp %q: could not write the harp self-id marker: %v — the transcript carries no owner tag", harp, err)
	}
}

// bindSessionFromPayload reads a session_start payload from in, decodes it
// through the firing engine's codec, and binds the native session and
// transcript it names to harp. Idempotent: re-running with the same payload
// is a no-op; operations.BindSession no-ops a harp that is absent from the
// index, and re-points one whose engine has rotated to a new transcript. A
// payload the codec cannot decode is an error naming the harp: the bind did
// not happen, and saying so is how an operator learns why a harp never got
// captured ("no canonical transcript captured for harp ...").
func bindSessionFromPayload(in io.Reader, codec engine.HookCodec, harp string) error {
	if harp == "" {
		return nil
	}
	raw, err := io.ReadAll(in)
	if err != nil {
		return fmt.Errorf("read payload: %w", err)
	}
	ev, err := codec.Decode(wire.HookEventSessionStart, raw)
	if err != nil {
		return fmt.Errorf("session-bind: harp %q: the session_start payload did not decode, so the harp<->session bind did not happen: %w", harp, err)
	}
	return operations.BindSession(harp, ev.NativeSession, ev.Transcript)
}
