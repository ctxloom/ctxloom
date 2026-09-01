package acp

import (
	"context"
	"errors"
	"sync"
	"testing"

	api "github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/tmuxhost"
)

// fakeTmuxRunner is a scriptable tmuxhost.Runner, so the terminal/* wire tests
// in this package can drive create -> output -> wait -> kill -> release without
// a real tmux binary.
//
// It is a LOCAL COPY of the fake tmuxhost's own tests use, deliberately rather
// than a helper exported from tmuxhost. Exporting a testing seam would put a
// test-only type on the surviving package's public surface purely to serve this
// one — and this package is scheduled for deletion, at which point the export
// would have no caller left and no obvious reason to go. Thirty duplicated
// lines in the dying half is the cheaper of the two.
type fakeTmuxRunner struct {
	mu    sync.Mutex
	calls [][]string
	// failAll, if set, makes every tmux call fail with it — the "tmux is not
	// installed" case.
	failAll error
}

func newFakeTmuxRunner() *fakeTmuxRunner { return &fakeTmuxRunner{} }

func (f *fakeTmuxRunner) Run(_ context.Context, args ...string) (string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, append([]string(nil), args...))
	f.mu.Unlock()
	return "", f.failAll
}

// calledWith reports whether any tmux invocation used sub as its subcommand.
func (f *fakeTmuxRunner) calledWith(sub string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if len(c) > 0 && c[0] == sub {
			return true
		}
	}
	return false
}

// recordingHosts is a tmuxTerminals that records what the translation handed
// it and returns whatever a test scripts. It exists so the SDK-to-tmuxhost
// mapping can be asserted on BOTH sides — the Spec that goes down, and the
// SDK response built from what comes back — without a tmux server.
type recordingHosts struct {
	gotSpec tmuxhost.Spec
	gotID   tmuxhost.TerminalID

	retID   tmuxhost.TerminalID
	retOut  tmuxhost.Output
	retWait *tmuxhost.ExitStatus
	err     error
}

func (r *recordingHosts) Create(_ context.Context, spec tmuxhost.Spec) (tmuxhost.TerminalID, error) {
	r.gotSpec = spec
	return r.retID, r.err
}

func (r *recordingHosts) Output(id tmuxhost.TerminalID) (tmuxhost.Output, error) {
	r.gotID = id
	return r.retOut, r.err
}

func (r *recordingHosts) Wait(_ context.Context, id tmuxhost.TerminalID) (*tmuxhost.ExitStatus, error) {
	r.gotID = id
	return r.retWait, r.err
}

func (r *recordingHosts) Kill(_ context.Context, id tmuxhost.TerminalID) error {
	r.gotID = id
	return r.err
}

func (r *recordingHosts) Release(_ context.Context, id tmuxhost.TerminalID) error {
	r.gotID = id
	return r.err
}

func intp(i int) *int       { return &i }
func strp(s string) *string { return &s }

// TestLocalTerminals_Create_CarriesEveryRequestFieldIntoTheSpec pins the
// DOWNWARD half of the translation, field by field.
//
// Dropping any one of these is silent: tmux is handed a spec missing a cwd or
// an env var, the window opens, terminal/create returns a terminalId, and the
// command simply runs somewhere else or without its environment. Asserting the
// whole Spec in one Equal (rather than field by field) also makes a field ADDED
// to Spec and forgotten here fail, which a per-field assertion would not.
func TestLocalTerminals_Create_CarriesEveryRequestFieldIntoTheSpec(t *testing.T) {
	h := &recordingHosts{retID: "term-1"}
	l := &localTerminals{hosts: h}

	cwd, limit := "/work", 4096
	resp, err := l.create(context.Background(), api.CreateTerminalRequest{
		Command:         "echo",
		Args:            []string{"hi", "there"},
		Cwd:             &cwd,
		Env:             []api.EnvVariable{{Name: "FOO", Value: "bar"}, {Name: "BAZ", Value: "qux"}},
		OutputByteLimit: &limit,
	})
	require.NoError(t, err)
	assert.Equal(t, api.TerminalId("term-1"), resp.TerminalId)

	assert.Equal(t, tmuxhost.Spec{
		Command:     "echo",
		Args:        []string{"hi", "there"},
		Cwd:         "/work",
		Env:         []tmuxhost.EnvVar{{Name: "FOO", Value: "bar"}, {Name: "BAZ", Value: "qux"}},
		OutputLimit: &limit,
	}, h.gotSpec)
}

// TestLocalTerminals_Create_AbsentCwdMeansInherit: the SDK distinguishes an
// absent cwd from an empty one and tmuxhost does not, so the nil case must
// collapse to "" rather than panic on the dereference.
func TestLocalTerminals_Create_AbsentCwdMeansInherit(t *testing.T) {
	h := &recordingHosts{retID: "term-1"}
	l := &localTerminals{hosts: h}

	_, err := l.create(context.Background(), api.CreateTerminalRequest{Command: "true"})
	require.NoError(t, err)
	assert.Equal(t, "", h.gotSpec.Cwd)
	assert.Nil(t, h.gotSpec.Env, "no env in the request must not invent an empty one")
}

// TestLocalTerminals_Output_CarriesTextTruncationAndExit pins the UPWARD half.
//
// Truncated is the field most worth pinning: a client that is told its output
// is complete when it is not has no way to discover otherwise, and the failure
// looks exactly like a command that printed less than it did.
func TestLocalTerminals_Output_CarriesTextTruncationAndExit(t *testing.T) {
	h := &recordingHosts{retOut: tmuxhost.Output{
		Text:      "hello world\n",
		Truncated: true,
		Exit:      &tmuxhost.ExitStatus{ExitCode: intp(7)},
	}}
	l := &localTerminals{hosts: h}

	resp, err := l.output(context.Background(), api.TerminalOutputRequest{TerminalId: "term-1"})
	require.NoError(t, err)
	assert.Equal(t, tmuxhost.TerminalID("term-1"), h.gotID)
	assert.Equal(t, "hello world\n", resp.Output)
	assert.True(t, resp.Truncated, "a truncated capture must be reported as truncated")
	require.NotNil(t, resp.ExitStatus)
	require.NotNil(t, resp.ExitStatus.ExitCode)
	assert.Equal(t, 7, *resp.ExitStatus.ExitCode)
}

// TestExitStatusToAPI_StillRunningStaysNil is the sharpest assertion in this
// file. A nil status means NOT FINISHED; the zero-valued api.TerminalExitStatus
// that a careless mapping produces is indistinguishable from a clean exit, so
// the bug reports a still-running command as having succeeded.
func TestExitStatusToAPI_StillRunningStaysNil(t *testing.T) {
	assert.Nil(t, exitStatusToAPI(nil),
		"a running command has no exit status; a zero-valued one reads as a clean exit 0")

	got := exitStatusToAPI(&tmuxhost.ExitStatus{ExitCode: intp(0)})
	require.NotNil(t, got)
	require.NotNil(t, got.ExitCode)
	assert.Equal(t, 0, *got.ExitCode)
}

// TestLocalTerminals_Output_StillRunningReportsNoExitStatus is the same
// property observed through the response the client actually receives.
func TestLocalTerminals_Output_StillRunningReportsNoExitStatus(t *testing.T) {
	h := &recordingHosts{retOut: tmuxhost.Output{Text: "partial"}}
	l := &localTerminals{hosts: h}

	resp, err := l.output(context.Background(), api.TerminalOutputRequest{TerminalId: "term-1"})
	require.NoError(t, err)
	assert.Nil(t, resp.ExitStatus, "a still-running terminal must report no exit status at all")
}

// TestLocalTerminals_Wait_CarriesBothExitCodeAndSignal: a killed process has a
// signal and no code, a normal exit has a code and no signal, and each field is
// dropped independently by a careless mapping — so both are pinned.
func TestLocalTerminals_Wait_CarriesBothExitCodeAndSignal(t *testing.T) {
	t.Run("a normal exit carries its code", func(t *testing.T) {
		h := &recordingHosts{retWait: &tmuxhost.ExitStatus{ExitCode: intp(7)}}
		l := &localTerminals{hosts: h}
		resp, err := l.wait(context.Background(), api.WaitForTerminalExitRequest{TerminalId: "term-1"})
		require.NoError(t, err)
		require.NotNil(t, resp.ExitCode)
		assert.Equal(t, 7, *resp.ExitCode)
		assert.Nil(t, resp.Signal)
	})

	t.Run("a killed process carries its signal", func(t *testing.T) {
		h := &recordingHosts{retWait: &tmuxhost.ExitStatus{Signal: strp("SIGHUP")}}
		l := &localTerminals{hosts: h}
		resp, err := l.wait(context.Background(), api.WaitForTerminalExitRequest{TerminalId: "term-1"})
		require.NoError(t, err)
		require.NotNil(t, resp.Signal)
		assert.Equal(t, "SIGHUP", *resp.Signal)
		assert.Nil(t, resp.ExitCode, "a signalled process has no exit CODE")
	})
}

// TestLocalTerminals_HostingFailuresPropagate: every method must surface the
// hosting error rather than returning a zero response and nil. This package's
// characteristic bug is a successful-looking answer over a failed operation,
// and terminal/kill in particular has no other way to be noticed: its response
// is empty even when it works, so a swallowed error is invisible at the wire.
func TestLocalTerminals_HostingFailuresPropagate(t *testing.T) {
	boom := errors.New("tmux exploded")
	for _, tc := range []struct {
		name string
		call func(*localTerminals) error
	}{
		{"create", func(l *localTerminals) error {
			_, err := l.create(context.Background(), api.CreateTerminalRequest{Command: "true"})
			return err
		}},
		{"output", func(l *localTerminals) error {
			_, err := l.output(context.Background(), api.TerminalOutputRequest{TerminalId: "t"})
			return err
		}},
		{"wait", func(l *localTerminals) error {
			_, err := l.wait(context.Background(), api.WaitForTerminalExitRequest{TerminalId: "t"})
			return err
		}},
		{"kill", func(l *localTerminals) error {
			_, err := l.kill(context.Background(), api.KillTerminalRequest{TerminalId: "t"})
			return err
		}},
		{"release", func(l *localTerminals) error {
			_, err := l.release(context.Background(), api.ReleaseTerminalRequest{TerminalId: "t"})
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := &localTerminals{hosts: &recordingHosts{err: boom}}
			assert.ErrorIs(t, tc.call(l), boom,
				"terminal/%s must not answer successfully over a failed tmux operation", tc.name)
		})
	}
}

// TestLocalTerminals_KillAndRelease_AddressTheRequestedTerminal: both take an
// id and return an empty response, so nothing about their result would reveal
// that the wrong terminal — or none — was addressed.
func TestLocalTerminals_KillAndRelease_AddressTheRequestedTerminal(t *testing.T) {
	h := &recordingHosts{}
	l := &localTerminals{hosts: h}

	_, err := l.kill(context.Background(), api.KillTerminalRequest{TerminalId: "term-kill"})
	require.NoError(t, err)
	assert.Equal(t, tmuxhost.TerminalID("term-kill"), h.gotID)

	_, err = l.release(context.Background(), api.ReleaseTerminalRequest{TerminalId: "term-rel"})
	require.NoError(t, err)
	assert.Equal(t, tmuxhost.TerminalID("term-rel"), h.gotID)
}
