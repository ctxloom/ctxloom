package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/runner"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/core/spool"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// Every way the mail-peek hook can fail part-way, and what each leaves
// behind: the hook never fails the tool call, so the returned error (one
// stderr line) and the spool or the runner's requests are all there is to
// see.

// ownerPeekEnv is the session owner's hook environment over fs.
func ownerPeekEnv(fs afero.Fs) mailPeekEnv {
	return mailPeekEnv{fs: fs, getenv: peekEnvOf(map[string]string{sessions.EnvHarp: mailDrainOwner}), owner: true}
}

// ownerSpoolDir is one of the owner's spool directories by its path relative
// to the spool root.
func ownerSpoolDir(t *testing.T, rel string) string {
	t.Helper()
	root, err := spool.Root(spool.NewHomeMapper(), mailDrainOwner)
	require.NoError(t, err)
	return filepath.Join(root, filepath.FromSlash(rel))
}

// faultFs fails MkdirAll and Open of exactly one path, and passes everything
// else to the real filesystem.
type faultFs struct {
	afero.Fs
	path string
}

var errInjected = errors.New("injected fault")

func (f faultFs) MkdirAll(name string, perm os.FileMode) error {
	if filepath.Clean(name) == f.path {
		return errInjected
	}
	return f.Fs.MkdirAll(name, perm)
}

func (f faultFs) Open(name string) (afero.File, error) {
	if filepath.Clean(name) == f.path {
		return nil, errInjected
	}
	return f.Fs.Open(name)
}

// TestPeekMail_AnUnreadableInboxSaysSoAndWritesNothing: the cheap check
// failing is a failure, named — not an empty inbox.
func TestPeekMail_AnUnreadableInboxSaysSoAndWritesNothing(t *testing.T) {
	testsupport.Isolate(t)
	in := ownerSpoolDir(t, "in")
	require.NoError(t, os.MkdirAll(filepath.Dir(in), 0o700))
	require.NoError(t, os.WriteFile(in, []byte("not a directory"), 0o600))

	var out bytes.Buffer
	err := peekMail(mailPeekCmd(&out), claudeCodec(t), ownerPeekEnv(afero.NewOsFs()))
	require.ErrorContains(t, err, "no mail delivered")
	assert.Empty(t, out.String())
}

// TestPeekMail_AClaimThatFailsDeliversNothing: mail waits, but it cannot be
// moved into the in-flight set — nothing is written, and the failure is named.
func TestPeekMail_AClaimThatFailsDeliversNothing(t *testing.T) {
	testsupport.Isolate(t)
	seedOwnerMail(t, "child-one", "result", "the answer\n")
	claimed := ownerSpoolDir(t, string(spool.ClaimedDirName))
	require.NoError(t, os.RemoveAll(claimed))
	require.NoError(t, os.WriteFile(claimed, []byte("not a directory"), 0o600))

	var out bytes.Buffer
	err := peekMail(mailPeekCmd(&out), claudeCodec(t), ownerPeekEnv(afero.NewOsFs()))
	require.ErrorContains(t, err, "no mail delivered")
	assert.Empty(t, out.String())
}

// TestPeekMail_MalformedSpoolFilesAreNamedEveryOne: files in in/ that are not
// messages are not mail, but each is a problem the hook names — all of them.
func TestPeekMail_MalformedSpoolFilesAreNamedEveryOne(t *testing.T) {
	testsupport.Isolate(t)
	in := ownerSpoolDir(t, "in")
	require.NoError(t, os.MkdirAll(in, 0o700))
	for _, name := range []string{"bad-one" + spool.MessageFileExt, "bad-two" + spool.MessageFileExt} {
		require.NoError(t, os.WriteFile(filepath.Join(in, name), []byte("no frontmatter here"), 0o600))
	}

	var out bytes.Buffer
	err := peekMail(mailPeekCmd(&out), claudeCodec(t), ownerPeekEnv(afero.NewOsFs()))
	require.ErrorContains(t, err, "2 problem(s) in the owner's spool")
	assert.Empty(t, out.String(), "no mail, nothing written")
}

// TestPeekMail_TheOwnerWhoseWriteFailedLeavesItsMailClaimed: a write that
// failed acknowledges nothing; the claimed mail is the next call's.
func TestPeekMail_TheOwnerWhoseWriteFailedLeavesItsMailClaimed(t *testing.T) {
	testsupport.Isolate(t)
	name := seedOwnerMail(t, "child-one", "result", "the answer\n")
	cmd := mailPeekCmd(&bytes.Buffer{})
	cmd.SetOut(&failingWriter{err: errors.New("broken pipe")})

	err := peekMail(cmd, claudeCodec(t), ownerPeekEnv(afero.NewOsFs()))
	require.ErrorContains(t, err, "1 message(s) left claimed, not delivered")
	assert.Equal(t, []string{name}, spoolNames(t, spool.ClaimedDirName))
	assert.Empty(t, deliveredIDs(t))
}

// TestPeekMail_AMessageTooBigForOneCallAnswersNoWake: when not even the first
// message fits, the context only says mail waits — nothing was delivered, so
// nothing is acknowledged and an armed wake still stands.
func TestPeekMail_AMessageTooBigForOneCallAnswersNoWake(t *testing.T) {
	testsupport.Isolate(t)
	name := seedOwnerMail(t, "child-one", "result", strings.Repeat("x", 4*claudeCodec(t).ContextLimit())+"\n")
	nonce, err := spool.ArmWake(afero.NewOsFs(), spool.NewHomeMapper(), mailDrainOwner)
	require.NoError(t, err)

	var out bytes.Buffer
	require.NoError(t, peekMail(mailPeekCmd(&out), claudeCodec(t), ownerPeekEnv(afero.NewOsFs())))
	assert.Contains(t, peekedContext(t, &out), "1 more message(s) wait")
	assert.Equal(t, []string{name}, spoolNames(t, spool.ClaimedDirName))
	assert.Empty(t, deliveredIDs(t))
	outstanding, err := spool.OutstandingWake(afero.NewOsFs(), spool.NewHomeMapper(), mailDrainOwner)
	require.NoError(t, err)
	assert.Equal(t, []string{nonce}, outstanding, "a wake for mail not yet read still stands")
}

// tinyCodec is an engine whose per-hook context cap cannot hold even the
// notice that mail waits.
type tinyCodec struct{ engine.HookCodec }

func (tinyCodec) ContextLimit() int { return 10 }

// TestPeekMail_AnEngineWithNoRoomIsWrittenNothing: with no room for even the
// notice, the hook writes nothing at all and leaves the mail claimed.
func TestPeekMail_AnEngineWithNoRoomIsWrittenNothing(t *testing.T) {
	testsupport.Isolate(t)
	name := seedOwnerMail(t, "child-one", "result", "the answer\n")

	var out bytes.Buffer
	require.NoError(t, peekMail(mailPeekCmd(&out), tinyCodec{claudeCodec(t)}, ownerPeekEnv(afero.NewOsFs())))
	assert.Empty(t, out.String())
	assert.Equal(t, []string{name}, spoolNames(t, spool.ClaimedDirName))
}

// TestPeekMail_SpoolProblemsAreNamedWhenNothingIsDelivered: a malformed file
// beside mail that cannot be delivered this call — the engine has no room,
// or the message is too big — is still named.
func TestPeekMail_SpoolProblemsAreNamedWhenNothingIsDelivered(t *testing.T) {
	for name, c := range map[string]struct {
		body  string
		codec func(t *testing.T) engine.HookCodec
	}{
		"no room for anything":   {"the answer\n", func(t *testing.T) engine.HookCodec { return tinyCodec{claudeCodec(t)} }},
		"the message is too big": {strings.Repeat("x", 4*claudeCodec(t).ContextLimit()) + "\n", claudeCodec},
	} {
		t.Run(name, func(t *testing.T) {
			testsupport.Isolate(t)
			seedOwnerMail(t, "child-one", "result", c.body)
			require.NoError(t, os.WriteFile(filepath.Join(ownerSpoolDir(t, "in"), "bad"+spool.MessageFileExt), []byte("no frontmatter here"), 0o600))

			err := peekMail(mailPeekCmd(&bytes.Buffer{}), c.codec(t), ownerPeekEnv(afero.NewOsFs()))
			require.ErrorContains(t, err, "1 problem(s) in the owner's spool")
		})
	}
}

// TestPeekMail_AnAcknowledgementThatFailsIsNamed: written but not recorded as
// delivered means it will be delivered again — the hook says so.
func TestPeekMail_AnAcknowledgementThatFailsIsNamed(t *testing.T) {
	testsupport.Isolate(t)
	seedOwnerMail(t, "child-one", "result", "the answer\n")
	fs := faultFs{Fs: afero.NewOsFs(), path: ownerSpoolDir(t, "in/delivered")}

	var out bytes.Buffer
	err := peekMail(mailPeekCmd(&out), claudeCodec(t), ownerPeekEnv(fs))
	require.ErrorContains(t, err, "could not be acknowledged and will be delivered again")
	assert.Contains(t, peekedContext(t, &out), "the answer", "what was written stays written")
}

// TestPeekMail_WakesThatCannotBeClearedAreNamed: the delivery is good, but a
// wake it answered could not be cleared — the hook says so.
func TestPeekMail_WakesThatCannotBeClearedAreNamed(t *testing.T) {
	testsupport.Isolate(t)
	seedOwnerMail(t, "child-one", "result", "the answer\n")
	fs := faultFs{Fs: afero.NewOsFs(), path: ownerSpoolDir(t, "in/wake")}

	var out bytes.Buffer
	err := peekMail(mailPeekCmd(&out), claudeCodec(t), ownerPeekEnv(fs))
	require.ErrorContains(t, err, "the wakes this delivery answered could not be cleared")
	assert.Len(t, deliveredIDs(t), 1, "the delivery itself was acknowledged")
}

// TestPeekRunnerMail_AnAddressThatNamesNoRunnerAsksNobody: an unparseable
// hook address, or one with no host, is refused by name before any request.
func TestPeekRunnerMail_AnAddressThatNamesNoRunnerAsksNobody(t *testing.T) {
	for _, addr := range []string{"http://[::1", "relative/hook/path"} {
		t.Run(addr, func(t *testing.T) {
			var out bytes.Buffer
			err := peekRunnerMail(mailPeekCmd(&out), claudeCodec(t), sessions.Endpoint{URL: addr, Credential: "tok"}, http.DefaultClient)
			require.ErrorContains(t, err, "names no runner")
			assert.Empty(t, out.String())
		})
	}
}

// TestPeekRunnerMail_TheCallersContextBoundsTheExchange: a cancelled hook
// context reaches the runner with nothing; a command with no context at all
// still asks.
func TestPeekRunnerMail_TheCallersContextBoundsTheExchange(t *testing.T) {
	f := &fakeRunnerMail{token: "tok", peek: runner.MailPeek{Lease: "L1", Context: "binding reply", IDs: []string{"m-1"}}}
	srv := f.serve(t)
	hook := sessions.Endpoint{URL: srv.URL + runner.HookPath, Credential: "tok"}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelled := mailPeekCmd(&bytes.Buffer{})
	cancelled.SetContext(ctx)
	require.ErrorContains(t, peekRunnerMail(cancelled, claudeCodec(t), hook, srv.Client()), "no mail delivered")
	assert.Empty(t, f.requests(), "a cancelled hook asks the runner nothing")

	var out bytes.Buffer
	bare := &cobra.Command{}
	bare.SetOut(&out)
	require.NoError(t, peekRunnerMail(bare, claudeCodec(t), hook, srv.Client()))
	assert.Contains(t, peekedContext(t, &out), "binding reply")
}

// TestPeekRunnerMail_NothingHandedOutWritesNothing: an empty answer is no
// mail — no envelope, no confirm.
func TestPeekRunnerMail_NothingHandedOutWritesNothing(t *testing.T) {
	f := &fakeRunnerMail{token: "tok"}
	srv := f.serve(t)

	var out bytes.Buffer
	require.NoError(t, peekRunnerMail(mailPeekCmd(&out), claudeCodec(t), sessions.Endpoint{URL: srv.URL, Credential: "tok"}, srv.Client()))
	assert.Empty(t, out.String())
	assert.Len(t, f.requests(), 1, "fetched, nothing to confirm")
}

// TestPeekRunnerMail_ContextWithNoIDsConfirmsNothing: a context that carries
// no message (only the notice that mail waits) has nothing to confirm.
func TestPeekRunnerMail_ContextWithNoIDsConfirmsNothing(t *testing.T) {
	f := &fakeRunnerMail{token: "tok", peek: runner.MailPeek{Lease: "L1", Context: "ctxloom: 1 more message(s) wait"}}
	srv := f.serve(t)

	var out bytes.Buffer
	require.NoError(t, peekRunnerMail(mailPeekCmd(&out), claudeCodec(t), sessions.Endpoint{URL: srv.URL, Credential: "tok"}, srv.Client()))
	assert.Contains(t, peekedContext(t, &out), "1 more message(s) wait")
	assert.Len(t, f.requests(), 1, "no ids, no confirm")
}

// TestPeekRunnerMail_AConfirmThatFailsSaysTheMailComesAgain: written, but
// the runner never heard the confirm — the mail is delivered again at the
// turn boundary, and the hook says so.
func TestPeekRunnerMail_AConfirmThatFailsSaysTheMailComesAgain(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req runner.MailPeekRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Op == runner.MailOpConfirm {
			http.Error(w, "runner gone", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(runner.MailPeek{Lease: "L1", Context: "binding reply", IDs: []string{"m-1", "m-2"}})
	}))
	t.Cleanup(srv.Close)

	var out bytes.Buffer
	err := peekRunnerMail(mailPeekCmd(&out), claudeCodec(t), sessions.Endpoint{URL: srv.URL, Credential: "tok"}, srv.Client())
	require.ErrorContains(t, err, "2 message(s) were written but not confirmed")
	assert.Contains(t, peekedContext(t, &out), "binding reply")
}

// TestPeekRunnerMail_NoClientUsesItsOwn: a nil client is the hook's own,
// bounded one; a given client is the one used (a TLS runner the default
// client does not trust answers only through it).
func TestPeekRunnerMail_NoClientUsesItsOwn(t *testing.T) {
	f := &fakeRunnerMail{token: "tok", peek: runner.MailPeek{Lease: "L1", Context: "binding reply", IDs: []string{"m-1"}}}
	plain := f.serve(t)
	var out bytes.Buffer
	require.NoError(t, peekRunnerMail(mailPeekCmd(&out), claudeCodec(t), sessions.Endpoint{URL: plain.URL, Credential: "tok"}, nil))
	assert.Contains(t, peekedContext(t, &out), "binding reply")

	tls := httptest.NewTLSServer(plain.Config.Handler)
	t.Cleanup(tls.Close)
	out.Reset()
	require.NoError(t, peekRunnerMail(mailPeekCmd(&out), claudeCodec(t), sessions.Endpoint{URL: tls.URL, Credential: "tok"}, tls.Client()))
	assert.Contains(t, peekedContext(t, &out), "binding reply")
}

// TestPostMail_TheAnswerIsBoundedAtOneMiB: the runner's answer is read to at
// most 1 MiB. An answer under the bound is read whole; one over it is cut and
// refused as unreadable.
func TestPostMail_TheAnswerIsBoundedAtOneMiB(t *testing.T) {
	answer := func(size int) *httptest.Server {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			ctx := strings.Repeat("a", size)
			_ = json.NewEncoder(w).Encode(runner.MailPeek{Context: ctx})
		}))
		t.Cleanup(srv.Close)
		return srv
	}
	var peek runner.MailPeek
	under := answer(3 << 18) // 768 KiB
	require.NoError(t, postMail(context.Background(), under.Client(), under.URL, "tok", runner.MailPeekRequest{Op: runner.MailOpFetch}, &peek))
	assert.Len(t, peek.Context, 3<<18)

	over := answer(3 << 19) // 1.5 MiB
	require.Error(t, postMail(context.Background(), over.Client(), over.URL, "tok", runner.MailPeekRequest{Op: runner.MailOpFetch}, &peek))
}

// TestPostMail_AnAnswerCutShortIsRefused: a body that ends before its
// declared length is not an answer.
func TestPostMail_AnAnswerCutShortIsRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			return
		}
		conn, buf, err := hj.Hijack()
		if err != nil {
			return
		}
		_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\n{\"context\":")
		_ = buf.Flush()
		_ = conn.(*net.TCPConn).CloseWrite()
		_ = conn.Close()
	}))
	t.Cleanup(srv.Close)

	var peek runner.MailPeek
	err := postMail(context.Background(), srv.Client(), srv.URL, "tok", runner.MailPeekRequest{Op: runner.MailOpFetch}, &peek)
	require.ErrorContains(t, err, "read the runner's answer")
}
