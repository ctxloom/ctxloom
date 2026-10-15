package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/runner"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/core/spool"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
)

// The mail-peek hook fires after every tool call. It hands the engine mail
// that arrived DURING its turn — for the session owner straight from its own
// spool (claim, write, acknowledge, as mail-drain does), for a runner-hosted
// child by asking its runner, the child spool's one reader. Every test reads
// stdout as the envelope the engine parses, and the spool or the runner's
// record of what was confirmed.

const postToolPayload = `{"session_id":"s","hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"ls"},"tool_response":{"stdout":"x"}}`

// mailPeekCmd wires stdin to a PostToolUse payload and stdout to out.
func mailPeekCmd(out *bytes.Buffer) *cobra.Command {
	c := &cobra.Command{}
	c.SetIn(bytes.NewBufferString(postToolPayload))
	c.SetOut(out)
	c.SetErr(&bytes.Buffer{})
	c.SetContext(context.Background())
	return c
}

// peekEnvOf is a getenv over a fixed map.
func peekEnvOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func peekedContext(t *testing.T, out *bytes.Buffer) string {
	t.Helper()
	env := drainedEnvelope(t, out)
	assert.Equal(t, "PostToolUse", env.HookSpecificOutput.HookEventName)
	return env.HookSpecificOutput.AdditionalContext
}

// TestPeekMail_TheOwnerIsHandedMailThatArrivedMidTurn: the owner's mail is
// claimed, written as the tool call's context under the coordinator's frame,
// and acknowledged — the same claim-and-ack as the turn-start drain.
func TestPeekMail_TheOwnerIsHandedMailThatArrivedMidTurn(t *testing.T) {
	testsupport.Isolate(t)
	name := seedOwnerMail(t, "child-one", "result", "the answer is 42\n")

	var out bytes.Buffer
	require.NoError(t, peekMail(mailPeekCmd(&out), claudeCodec(t), mailPeekEnv{
		fs: afero.NewOsFs(), getenv: peekEnvOf(map[string]string{sessions.EnvHarp: mailDrainOwner}), owner: true,
	}))

	ctx := peekedContext(t, &out)
	assert.Contains(t, ctx, "during this turn", "the engine is told the mail arrived mid-turn")
	assert.Contains(t, ctx, "[coordinator-delivered message from=child-one kind=result id="+stem(name)+"]\nthe answer is 42")
	assert.Empty(t, spoolNames(t, spool.DirIn))
	assert.Empty(t, spoolNames(t, spool.ClaimedDirName), "acknowledged: nothing left in flight")
	assert.Equal(t, []string{stem(name)}, deliveredIDs(t))
}

// TestPeekMail_TheOwnerGetsWhatFitsAndTheRestStaysClaimed: one hook's context
// is capped. What fits is delivered and acknowledged; the rest stays claimed
// for the next tool call (or the next turn's drain), and the context says it
// waits.
func TestPeekMail_TheOwnerGetsWhatFitsAndTheRestStaysClaimed(t *testing.T) {
	testsupport.Isolate(t)
	first := seedOwnerMail(t, "child-one", "result", "FIRST "+strings.Repeat("a", 5000)+"\n")
	second := seedOwnerMail(t, "child-two", "result", "SECOND "+strings.Repeat("b", 5000)+"\n")
	env := mailPeekEnv{fs: afero.NewOsFs(), getenv: peekEnvOf(map[string]string{sessions.EnvHarp: mailDrainOwner}), owner: true}

	var out bytes.Buffer
	require.NoError(t, peekMail(mailPeekCmd(&out), claudeCodec(t), env))
	ctx := peekedContext(t, &out)
	assert.LessOrEqual(t, len(ctx), claudeCodec(t).ContextLimit())
	assert.Contains(t, ctx, "FIRST")
	assert.NotContains(t, ctx, "SECOND")
	assert.Contains(t, ctx, "1 more message(s) wait")
	assert.Equal(t, []string{stem(first)}, deliveredIDs(t), "only what was written is acknowledged")
	assert.Equal(t, []string{second}, spoolNames(t, spool.ClaimedDirName), "the rest stays claimed, not lost")

	out.Reset()
	require.NoError(t, peekMail(mailPeekCmd(&out), claudeCodec(t), env))
	assert.Contains(t, peekedContext(t, &out), "SECOND", "the next tool call carries what did not fit")
	assert.ElementsMatch(t, []string{stem(first), stem(second)}, deliveredIDs(t))
}

// fakeRunnerMail is the runner's mail endpoint as the hook sees it: it
// answers one fetch with peek and records every request.
type fakeRunnerMail struct {
	mu    sync.Mutex
	peek  runner.MailPeek
	reqs  []runner.MailPeekRequest
	token string
	// confirmed, when set, is how many ids a confirm answers as confirmed;
	// unset, all of them.
	confirmed *int
}

func (f *fakeRunnerMail) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != runner.MailPath || r.Header.Get("Authorization") != "Bearer "+f.token {
			http.Error(w, "wrong path or bearer", http.StatusUnauthorized)
			return
		}
		var req runner.MailPeekRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		f.reqs = append(f.reqs, req)
		f.mu.Unlock()
		if req.Op == runner.MailOpConfirm {
			n := len(req.IDs)
			if f.confirmed != nil {
				n = *f.confirmed
			}
			_ = json.NewEncoder(w).Encode(runner.MailConfirmed{Confirmed: n})
			return
		}
		_ = json.NewEncoder(w).Encode(f.peek)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (f *fakeRunnerMail) requests() []runner.MailPeekRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]runner.MailPeekRequest(nil), f.reqs...)
}

// childEnv is a runner-hosted child's hook environment: its harp and its
// runner's hook address.
func childEnv(srv *httptest.Server, token string) func(string) string {
	m := sessions.EncodeHookReach(sessions.Endpoint{URL: srv.URL + runner.HookPath, Credential: token})
	m[sessions.EnvHarp] = mailDrainOwner
	return peekEnvOf(m)
}

// TestPeekMail_AChildAsksItsRunnerAndConfirmsWhatItWrote: a child's spool is
// its runner's to read. The hook fetches over the runner's endpoint — at
// the engine's own context cap — writes what it was handed, and only then
// confirms that lease's ids.
func TestPeekMail_AChildAsksItsRunnerAndConfirmsWhatItWrote(t *testing.T) {
	testsupport.Isolate(t)
	seedOwnerMail(t, "parent", "message", "binding reply\n") // in/ is not empty: the cheap check passes
	f := &fakeRunnerMail{token: "tok", peek: runner.MailPeek{Lease: "L1", Context: "ctxloom: mail arrived for you during this turn.\n\nbinding reply", IDs: []string{"m-1"}}}
	srv := f.serve(t)

	var out bytes.Buffer
	require.NoError(t, peekMail(mailPeekCmd(&out), claudeCodec(t), mailPeekEnv{fs: afero.NewOsFs(), getenv: childEnv(srv, "tok"), client: srv.Client()}))

	assert.Contains(t, peekedContext(t, &out), "binding reply")
	reqs := f.requests()
	require.Len(t, reqs, 2)
	assert.Equal(t, runner.MailPeekRequest{Op: runner.MailOpFetch, MaxChars: claudeCodec(t).ContextLimit()}, reqs[0])
	assert.Equal(t, runner.MailPeekRequest{Op: runner.MailOpConfirm, Lease: "L1", IDs: []string{"m-1"}}, reqs[1])
	assert.NotEmpty(t, spoolNames(t, spool.DirIn), "the hook never reads or moves a child's spool itself")
}

// TestPeekMail_AChildThatCouldNotWriteConfirmsNothing: the confirm is the
// delivery, so a hook that could not write the context must not send it —
// the message stays queued for its turn.
func TestPeekMail_AChildThatCouldNotWriteConfirmsNothing(t *testing.T) {
	testsupport.Isolate(t)
	seedOwnerMail(t, "parent", "message", "binding reply\n")
	f := &fakeRunnerMail{token: "tok", peek: runner.MailPeek{Lease: "L1", Context: "binding reply", IDs: []string{"m-1"}}}
	srv := f.serve(t)
	cmd := mailPeekCmd(&bytes.Buffer{})
	cmd.SetOut(&failingWriter{err: errors.New("broken pipe")})

	require.Error(t, peekMail(cmd, claudeCodec(t), mailPeekEnv{fs: afero.NewOsFs(), getenv: childEnv(srv, "tok"), client: srv.Client()}))
	reqs := f.requests()
	require.Len(t, reqs, 1, "fetched, never confirmed")
	assert.Equal(t, runner.MailOpFetch, reqs[0].Op)
}

// TestPeekMail_NothingWaitingAsksNobody: the hook fires on EVERY tool call,
// so with an empty inbox it must cost a directory listing and nothing else —
// no request to the runner, nothing on stdout.
func TestPeekMail_NothingWaitingAsksNobody(t *testing.T) {
	testsupport.Isolate(t)
	f := &fakeRunnerMail{token: "tok"}
	srv := f.serve(t)

	var out bytes.Buffer
	require.NoError(t, peekMail(mailPeekCmd(&out), claudeCodec(t), mailPeekEnv{fs: afero.NewOsFs(), getenv: childEnv(srv, "tok"), client: srv.Client()}))
	assert.Empty(t, f.requests(), "an empty inbox asks the runner nothing")
	assert.Empty(t, out.String())
}

// TestPeekMail_AnEngineCtxloomDidNotLaunchIsSilent: no harp, no spool, nothing
// to say — and no runner address means a non-owner has nobody to ask.
func TestPeekMail_AnEngineCtxloomDidNotLaunchIsSilent(t *testing.T) {
	testsupport.Isolate(t)
	seedOwnerMail(t, "parent", "message", "not for a stranger\n")

	var out bytes.Buffer
	require.NoError(t, peekMail(mailPeekCmd(&out), claudeCodec(t), mailPeekEnv{fs: afero.NewOsFs(), getenv: peekEnvOf(nil)}))
	assert.Empty(t, out.String())
	require.NoError(t, peekMail(mailPeekCmd(&out), claudeCodec(t), mailPeekEnv{fs: afero.NewOsFs(), getenv: peekEnvOf(map[string]string{sessions.EnvHarp: mailDrainOwner})}))
	assert.Empty(t, out.String(), "a child with no runner address has nobody to ask")
	assert.NotEmpty(t, spoolNames(t, spool.DirIn), "and claims nothing")
}

// TestPeekMail_TheOwnersDeliveryAnswersItsWakes: a wake armed for mail the
// peek has now delivered would wake the idle owner later for nothing; the
// delivery answers it, as the turn-start drain's does.
func TestPeekMail_TheOwnersDeliveryAnswersItsWakes(t *testing.T) {
	testsupport.Isolate(t)
	seedOwnerMail(t, "child-one", "result", "done\n")
	_, err := spool.ArmWake(afero.NewOsFs(), spool.NewHomeMapper(), mailDrainOwner)
	require.NoError(t, err)

	var out bytes.Buffer
	require.NoError(t, peekMail(mailPeekCmd(&out), claudeCodec(t), mailPeekEnv{
		fs: afero.NewOsFs(), getenv: peekEnvOf(map[string]string{sessions.EnvHarp: mailDrainOwner}), owner: true,
	}))
	outstanding, err := spool.OutstandingWake(afero.NewOsFs(), spool.NewHomeMapper(), mailDrainOwner)
	require.NoError(t, err)
	assert.Empty(t, outstanding)
}

// TestPeekMail_AChildWhoseTurnTookSomeMailSaysHowMany: ids the runner did not
// confirm (its turn boundary delivered them first) may be read twice, and the
// hook says how many of how many.
func TestPeekMail_AChildWhoseTurnTookSomeMailSaysHowMany(t *testing.T) {
	testsupport.Isolate(t)
	seedOwnerMail(t, "parent", "message", "binding reply\n")
	one := 1
	f := &fakeRunnerMail{token: "tok", confirmed: &one, peek: runner.MailPeek{Lease: "L1", Context: "binding reply", IDs: []string{"m-1", "m-2", "m-3"}}}
	srv := f.serve(t)

	var out bytes.Buffer
	err := peekMail(mailPeekCmd(&out), claudeCodec(t), mailPeekEnv{fs: afero.NewOsFs(), getenv: childEnv(srv, "tok"), client: srv.Client()})
	require.ErrorContains(t, err, "2 of 3 message(s) written mid-turn were not confirmed")
	assert.Contains(t, peekedContext(t, &out), "binding reply", "what was written stays written")
}

// runMailPeekHook runs the hook verb as the engine does, with --engine, and
// returns the warnings it said.
func runMailPeekHook(t *testing.T, engineName string) string {
	t.Helper()
	var sink strings.Builder
	restore := clidiag.SetSink(&sink)
	t.Cleanup(restore)
	require.NoError(t, runHookMailPeek(firedBy(t, mailPeekCmd(&bytes.Buffer{}), engineName), nil), "a hook never fails the tool call")
	return sink.String()
}

// TestRunHookMailPeek_SaysWhyAndNothingElse: an engine the hook cannot name
// delivers nothing and says why; an engine with nothing waiting says nothing.
func TestRunHookMailPeek_SaysWhyAndNothingElse(t *testing.T) {
	testsupport.Isolate(t)
	assert.Contains(t, runMailPeekHook(t, "no-such-engine"), "no mail delivered")
	assert.Empty(t, runMailPeekHook(t, "claude-code"), "nothing waiting, nothing said")
}

// TestRunHookMailPeek_ARunnerThatDoesNotAnswerCostsTheTimeout: the hook sits
// between a tool call and the model, so a runner that never answers costs
// mailPeekTimeout and no more — then the hook says so and lets the call go.
func TestRunHookMailPeek_ARunnerThatDoesNotAnswerCostsTheTimeout(t *testing.T) {
	testsupport.Isolate(t)
	seedOwnerMail(t, "parent", "message", "binding reply\n")
	hang := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-hang }))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(hang) })
	for k, v := range sessions.EncodeHookReach(sessions.Endpoint{URL: srv.URL + runner.HookPath, Credential: "tok"}) {
		t.Setenv(k, v)
	}
	t.Setenv(sessions.EnvHarp, mailDrainOwner)

	var sink strings.Builder
	restore := clidiag.SetSink(&sink)
	t.Cleanup(restore)
	cmd := firedBy(t, mailPeekCmd(&bytes.Buffer{}), "claude-code")
	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- runHookMailPeek(cmd, nil) }()
	select {
	case err := <-done:
		require.NoError(t, err, "a hook never fails the tool call")
		assert.Contains(t, sink.String(), "no mail delivered")
		assert.GreaterOrEqual(t, time.Since(start), mailPeekTimeout/2, "it waited for the runner")
	case <-time.After(mailPeekTimeout + 10*time.Second):
		t.Fatal("the hook waited past mailPeekTimeout for a runner that never answers")
	}
}
