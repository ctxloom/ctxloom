//go:build acceptance

// A SESSION OWNER STANDING FOR A SCENARIO.
//
// A coordinator is hosted only by a RUNNER: `ctxloom run` stands it up, its
// runner serves the owner's MCP surface on a unix socket, and every MCP shim
// is a pure client that forwards there — a shim with no runner refuses the
// agent tools (internal/adapters/mcp's errNoRunner) and never becomes a
// coordinator. So a scenario that drives agent_* tools through this
// harness's stdio `ctxloom mcp` shim needs a real owner standing first, and
// this fixture is that owner: a mock-engine `ctxloom run` held open on a pty
// for the scenario's whole life, torn down with the scenario.
//
// HOW THE SHIM FINDS IT. The forward tier reads exactly one thing:
// CTXLOOM_MCP_SOCKET in its environment (mcp.ServeStdio). The runner binds
// its socket under $XDG_RUNTIME_DIR/ctxloom (mcp.runnerSocketPath), so the
// fixture gives the scenario a private, short runtime dir, starts the owner,
// and reads the ONE socket the runner bound there. World.agent hands that
// path to the shim it starts. No third discovery path is added.
//
// WHY THE OWNER'S HARP IS EXPORTED TO EVERY LATER PROCESS. The forward tier
// verifies identity: a shim whose CTXLOOM_SESSION_HARP names a different
// session than the runner's is REFUSED and falls back to its local surface
// (mcp.verifyForwardTarget). Setting the owner's harp on the scenario's child
// env makes the shim's identity the owner's — and makes a later `ctxloom hook
// mail-drain` read the owner's own spool, which is what those scenarios
// assert on.
package acceptance

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cucumber/godog"

	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/tests/integration/testenv"
)

const (
	// sessionOwnerLabel is the LLM label the owner launches under: a mock
	// engine merged into the isolated HOME's config (the machine layer), so
	// the owner is the mock whatever the scenario's project config makes
	// primary — the live tiers make a real engine primary there, and the
	// owner must never be a paid, undrivable session.
	sessionOwnerLabel = "session-owner"

	// sessionOwnerFragment is the fragment the owner's run assembles. A
	// `ctxloom run` needs SOMETHING to launch with (a bare launch resolves the
	// project's default agent, which these fixtures do not declare), and a
	// fragment of its own keeps the owner from wearing a child's binding.
	sessionOwnerFragment = "session-owner"

	// sessionOwnerSentinel is the line typed into the owner's pty whose echo
	// proves the owner is fully up: `ctxloom run` hosts its coordinator and
	// stands its runner (socket included) BEFORE the engine reads stdin, so a
	// mock parked in its echo loop implies both exist.
	sessionOwnerSentinel = "session-owner-standing"

	// sessionOwnerReadyTimeout bounds the wait for that echo. Generous for
	// CI: the runner spawn is a real self-exec + go-plugin handshake.
	sessionOwnerReadyTimeout = 30 * time.Second

	// runnerSocketGlob is the runner-bound socket under the scenario's
	// runtime dir (mcp.runnerSocketPath's host tier: ctxloom/mcp-<pid>.sock).
	runnerSocketGlob = "ctxloom/mcp-*.sock"
)

// sessionOwner is the standing owner: its pty session, the harp it minted,
// and the runner socket the shim forwards to.
type sessionOwner struct {
	sess   *testenv.PTYSession
	harp   string
	socket string
	// removeRuntimeDir releases the private runtime dir the socket lives in.
	removeRuntimeDir func()
}

// standSessionOwner starts the owner from bin (AppBinary, or a copy a probe
// controls) with extraEnv on top of the scenario's isolated environment, and
// waits until it is standing. It must run BEFORE the shim's first tool call:
// the shim is started once per scenario and reads the socket at startup.
func (w *World) standSessionOwner(bin string, extraEnv ...string) error {
	if w.owner != nil {
		return errors.New("a session owner is already standing for this scenario")
	}
	if w.mcp != nil {
		return errors.New("the MCP shim is already running; the session owner must stand before the shim's first tool call, or the shim has nothing to forward to")
	}
	if err := w.env.WriteFile(bundleFilePath(sessionOwnerFragment), fmt.Sprintf("version: \"1.0.0\"\nfragments:\n  %s:\n    content: %q\n", sessionOwnerFragment, "the session owner's own context")); err != nil {
		return fmt.Errorf("session owner: author its fragment: %w", err)
	}
	if err := w.env.EnsureHomeMockLabel(sessionOwnerLabel); err != nil {
		return fmt.Errorf("session owner: %w", err)
	}

	// A private runtime dir, short enough for the socket path to bind, so the
	// runner's socket is the ONLY one under it.
	runtimeDir, removeRuntimeDir, err := testsupport.PickSocketDir(strings.Replace(runnerSocketGlob, "*", "9999999", 1), testsupport.SocketTiers())
	if err != nil {
		return fmt.Errorf("session owner: runtime dir: %w", err)
	}
	w.env.SetChildEnv("XDG_RUNTIME_DIR", runtimeDir)

	before, err := harpDirs(w.env.HomeDir)
	if err != nil {
		removeRuntimeDir()
		return err
	}

	sess, err := w.env.RunPTYFrom(bin, 100, 30, append([]string{"CTXLOOM_MOCK_ECHO_STDIN=1"}, extraEnv...), "run", "--llm", sessionOwnerLabel, "-f", sessionOwnerFragment)
	if err != nil {
		removeRuntimeDir()
		return fmt.Errorf("session owner: start `ctxloom run`: %w", err)
	}
	owner := &sessionOwner{sess: sess, removeRuntimeDir: removeRuntimeDir}
	// Registered the moment the process exists: a readiness failure below
	// must still tear the session down.
	w.owner = owner

	if _, err := sess.Write([]byte(sessionOwnerSentinel + "\n")); err != nil {
		return fmt.Errorf("session owner: type the readiness sentinel: %w", err)
	}
	if !sess.WaitForOutput(sessionOwnerReadyTimeout, func(out string) bool {
		return strings.Contains(out, "mock echo: "+sessionOwnerSentinel)
	}) {
		return fmt.Errorf("session owner: never echoed %q within %s — the owner is not standing; output:\n%s", sessionOwnerSentinel, sessionOwnerReadyTimeout, sess.Output())
	}

	sockets, err := filepath.Glob(filepath.Join(runtimeDir, runnerSocketGlob))
	if err != nil {
		return err
	}
	if len(sockets) != 1 {
		return fmt.Errorf("session owner: expected exactly one runner socket under %s, found %v; output:\n%s", runtimeDir, sockets, sess.Output())
	}
	owner.socket = sockets[0]

	after, err := harpDirs(w.env.HomeDir)
	if err != nil {
		return err
	}
	var minted []string
	for h := range after {
		if !before[h] {
			minted = append(minted, h)
		}
	}
	if len(minted) != 1 {
		return fmt.Errorf("session owner: expected the run to mint exactly one session, found %v; output:\n%s", minted, sess.Output())
	}
	owner.harp = minted[0]
	w.env.SetChildEnv("CTXLOOM_SESSION_HARP", owner.harp)
	return nil
}

// harpDirs lists the session directories under the isolated HOME's harp
// store (lock files beside them are not sessions).
func harpDirs(home string) (map[string]bool, error) {
	root := filepath.Join(home, filepath.FromSlash(harpSessionsRel))
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return map[string]bool{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the session store at %s: %w", root, err)
	}
	dirs := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() {
			dirs[e.Name()] = true
		}
	}
	return dirs, nil
}

// stop ends the owner: SIGTERM through the pty session's own close (the
// run's designed shutdown path, which reaps its runner), then the runtime
// dir. Nil-safe, so the scenario teardown can call it unconditionally.
func (o *sessionOwner) stop() {
	if o == nil {
		return
	}
	o.sess.Close()
	if o.removeRuntimeDir != nil {
		o.removeRuntimeDir()
	}
}

func registerSessionOwnerSteps(ctx *godog.ScenarioContext) {
	// The owner reads the project's config at start, so this step goes AFTER
	// the fixture that writes it and BEFORE the first tool call.
	ctx.Step(`^a session owner is standing$`, func(c context.Context) error {
		w := worldFrom(c)
		return w.standSessionOwner(w.env.AppBinary)
	})
}
