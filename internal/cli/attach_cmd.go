package cli

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"

	"github.com/spf13/cobra"
	"golang.org/x/term"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/agentcoord/coord"
	"github.com/ctxloom/ctxloom/internal/attach"
)

// attachReadOnly backs --read-only: watch a run without being able to type
// into it.
var attachReadOnly bool

var attachCmd = &cobra.Command{
	Use:   "attach <harp>",
	Short: "Attach your terminal to a run's live pane",
	Long: `Attach your terminal to a run's live pane.

The pane keeps running when you leave: press Ctrl-] to detach. Only the pane
ending detaches you with its exit status.

Attaching relays your terminal over the coordinator rather than running
` + "`tmux attach`" + ` locally, because a run's tmux server lives inside that
run's container and a local socket cannot reach it.`,
	Args: cobra.ExactArgs(1),
	RunE: runAttach,
}

func init() {
	attachCmd.Flags().BoolVar(&attachReadOnly, "read-only", false,
		"Watch without transmitting keystrokes (enforced here, not just at the far end)")
	rootCmd.AddCommand(attachCmd)
}

func runAttach(cmd *cobra.Command, args []string) error {
	host, cred, err := resolveAttachEndpoint(os.Getenv)
	if err != nil {
		return err
	}
	conn, err := grpc.NewClient(host,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithPerRPCCredentials(coordBearer(cred)))
	if err != nil {
		return fmt.Errorf("attach: dial coordinator at %s: %w", host, err)
	}
	defer func() { _ = conn.Close() }()

	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()

	tty, err := newStdTty(ctx)
	if err != nil {
		return err
	}
	client := agentcoordpb.NewCoordinatorServiceClient(conn)
	return attachOutcome(attach.Run(ctx, paneConn{client}, args[0], tty,
		attach.Options{ReadOnly: attachReadOnly}))
}

// attachOutcome maps the relay's terminal reason onto a process exit.
//
// A detach is a SUCCESS — the human chose to leave and the run is untouched —
// while a pane that ended must exit with the pane's own status. Collapsing the
// two would report a crashed agent as a clean exit, which is precisely the
// "exit 0 with nothing done" failure this codebase keeps having.
func attachOutcome(err error) error {
	switch {
	case err == nil, errors.Is(err, attach.ErrDetached):
		return nil
	case errors.Is(err, context.Canceled):
		return nil
	}
	var closed *attach.PaneClosedError
	if errors.As(err, &closed) {
		fmt.Fprintf(os.Stderr, "\r\n%s\r\n", closed.Error())
		if closed.ExitCode != 0 {
			return &ExitError{Code: int(closed.ExitCode)}
		}
		return nil
	}
	return err
}

// resolveAttachEndpoint finds the coordinator to attach through.
//
// It reads the coordinator trio from the environment and NOTHING else. The
// discoverable endpoint file (~/.ctxloom/coord/*/endpoint.json) carries a
// read-only CONSUMER credential, and the gRPC interceptor refuses that class
// on every CoordinatorService method by construction — attaching with it
// could only ever fail with PermissionDenied. Offering it as a fallback would
// be a route that looks like it works and cannot.
func resolveAttachEndpoint(getenv func(string) string) (host, cred string, err error) {
	raw := getenv(coord.EnvCoordURL)
	cred = getenv(coord.EnvCoordCred)
	if raw == "" || cred == "" {
		return "", "", fmt.Errorf(
			"attach: no coordinator credential in the environment (%s and %s); run this from a ctxloom session that owns the run",
			coord.EnvCoordURL, coord.EnvCoordCred)
	}
	u, perr := url.Parse(raw)
	if perr != nil {
		return "", "", fmt.Errorf("attach: parse %s %q: %w", coord.EnvCoordURL, raw, perr)
	}
	if u.Host == "" {
		return "", "", fmt.Errorf("attach: %s %q has no host", coord.EnvCoordURL, raw)
	}
	return u.Host, cred, nil
}

// coordBearer carries the coordinator credential on every RPC.
type coordBearer string

func (b coordBearer) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{"authorization": "Bearer " + string(b)}, nil
}
func (coordBearer) RequireTransportSecurity() bool { return false }

// paneConn adapts the generated client to attach.Conn, which exists so the
// relay can be driven without a server.
type paneConn struct {
	client agentcoordpb.CoordinatorServiceClient
}

func (p paneConn) AttachPane(ctx context.Context) (attach.Stream, error) {
	return p.client.AttachPane(ctx)
}

// stdTty is attach.Tty over the real terminal.
//
// Resize signalling reuses watchResize (run_resize_unix.go / _windows.go)
// rather than registering SIGWINCH a second time: two independent notions of
// "the terminal changed size" in one binary is the kind of duplicate this
// project keeps deleting.
type stdTty struct {
	in      *os.File
	out     *os.File
	resized chan struct{}
}

func newStdTty(ctx context.Context) (*stdTty, error) {
	in, out := os.Stdin, os.Stdout
	if !term.IsTerminal(int(in.Fd())) {
		return nil, errors.New("attach: stdin is not a terminal; attaching needs one")
	}
	t := &stdTty{in: in, out: out, resized: make(chan struct{}, 1)}
	sizes := watchResize(ctx, out)
	go func() {
		defer close(t.resized)
		for range sizes {
			select {
			case t.resized <- struct{}{}:
			default: // a pending signal already says "re-read the size"
			}
		}
	}()
	return t, nil
}

func (t *stdTty) Read(p []byte) (int, error)  { return t.in.Read(p) }
func (t *stdTty) Write(p []byte) (int, error) { return t.out.Write(p) }

func (t *stdTty) Size() (int, int, error) { return term.GetSize(int(t.out.Fd())) }

func (t *stdTty) MakeRaw() (func() error, error) {
	fd := int(t.in.Fd())
	state, err := term.MakeRaw(fd)
	if err != nil {
		return nil, err
	}
	return func() error { return term.Restore(fd, state) }, nil
}

func (t *stdTty) Resized() <-chan struct{} { return t.resized }
