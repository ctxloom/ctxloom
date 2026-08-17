//go:build integration || acceptance

package testenv

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/crypto/ssh/agent"
)

// StartSSHAgent stands up a REAL ssh-agent-protocol server — golang.org/x/
// crypto/ssh/agent's in-memory keyring, served over a unix socket — holding
// exactly the given TestSigners' identities, and returns the socket path to
// point SSH_AUTH_SOCK at.
//
// WHY THIS EXISTS. `ctxloom bundle sign` resolves WHO signs through
// internal/signing/agentkey, whose every branch ends at a live ssh-agent
// connection (agentkey's package doc: "every Discovered.Signer returned here
// is backed by a live ssh-agent connection ... never a file on disk"). Without
// an agent the acceptance suite can only ever sign IN GO, with
// signing.Sign + TestSigner (SeedSignedRemote above), which bypasses key
// discovery, the ssh-agent transport and the `.sig` writer entirely — so the
// production publishing path had never executed in an acceptance run at all.
// internal/signing/agent_signer_test.go already proves the same wiring over a
// net.Pipe; the only thing that pipe cannot do is be dialled by a SUBPROCESS,
// which is exactly what an acceptance scenario runs. Hence a real socket.
//
// It deliberately does NOT need the `ssh-agent` binary, a real key file, or
// the developer's own agent: everything is in-process and hermetic, and the
// socket lives under dir (pass TestEnvironment.Root, so it is removed with the
// rest of the scenario's temp tree even if stop is somehow never called).
//
// It also deliberately does NOT set SSH_AUTH_SOCK itself. Pointing the world
// at the agent is one line at the call site
// (w.env.SetEnv("SSH_AUTH_SOCK", sock)) and keeping it there keeps the
// interaction with steps_j001500.go's DELIBERATE blanking of the same variable
// visible: J001500 sets it to "" to force agentkey's no-key-anywhere branch, J001600
// sets it to this socket to force the key-found branch, and neither can
// silently change the other's meaning because both writes are explicit,
// per-scenario, and restored by TestEnvironment.Cleanup (SetEnv's
// first-write-wins bookkeeping).
//
// The comment attached to each identity is the ssh-agent key COMMENT — the
// string `ctxloom bundle sign --key <name>` matches case-insensitively as the
// last resort of its explicit-key chain (agentkey.resolveByComment), so a
// scenario can drive that branch by naming it.
//
// stop closes the listener and waits for every in-flight connection goroutine
// to finish, then removes the socket file: after it returns there is no
// listening socket, no leaked goroutine, and no stray file. It is safe to call
// more than once, so a caller can defer it unconditionally.
//
// An earlier sketch had `StartSSHAgent(signer) (sockPath string, stop
// func())`. This signature departs from it in three ways, each load-bearing:
// dir, because a unix socket needs a filesystem home and the
// scenario's temp root is the only one that gets cleaned up; an error return,
// because net.Listen genuinely fails (a too-long socket path is the classic);
// and variadic identities, because "exactly one key in the agent" versus "two"
// is itself a key-discovery branch (agentkey's sole-identity rule vs.
// AmbiguousKeyError) that a scenario must be able to choose.
func StartSSHAgent(dir string, identities ...SSHAgentIdentity) (sockPath string, stop func() error, err error) {
	keyring := agent.NewKeyring()
	for _, id := range identities {
		if id.Signer == nil {
			return "", nil, fmt.Errorf("StartSSHAgent: identity %q has no signer", id.Comment)
		}
		if err := keyring.Add(agent.AddedKey{PrivateKey: id.Signer.Private, Comment: id.Comment}); err != nil {
			return "", nil, fmt.Errorf("add %q to keyring: %w", id.Comment, err)
		}
	}

	sockPath = filepath.Join(dir, fmt.Sprintf("ssh-agent-%d.sock", os.Getpid()))
	// A socket left over from an earlier StartSSHAgent in the same dir would
	// make net.Listen fail with "address already in use"; the caller's dir is
	// this harness's own temp tree, so removing it is safe and never touches
	// anything a developer owns.
	_ = os.Remove(sockPath)
	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		return "", nil, fmt.Errorf("listen on %s: %w", sockPath, err)
	}

	var conns sync.WaitGroup
	var accepting sync.WaitGroup
	accepting.Add(1)
	go func() {
		defer accepting.Done()
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				// The only expected error is the Close in stop.
				return
			}
			conns.Add(1)
			go func() {
				defer conns.Done()
				defer func() { _ = conn.Close() }()
				// ServeAgent returns io.EOF on a clean client
				// disconnect, which is the normal end of every
				// `ctxloom bundle sign` run.
				_ = agent.ServeAgent(keyring, conn)
			}()
		}
	}()

	var once sync.Once
	stop = func() error {
		var closeErr error
		once.Do(func() {
			closeErr = listener.Close()
			accepting.Wait()
			conns.Wait()
			// net's unix listener already unlinks the socket on Close;
			// this is belt-and-braces for the case where it did not.
			_ = os.Remove(sockPath)
		})
		return closeErr
	}
	return sockPath, stop, nil
}

// SSHAgentIdentity is one key StartSSHAgent loads into its keyring: the
// generated identity, plus the ssh-agent COMMENT it is listed under.
type SSHAgentIdentity struct {
	Signer  *TestSigner
	Comment string
}
