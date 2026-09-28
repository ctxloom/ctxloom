//go:build acceptance

// A SESSION OWNER STANDING FOR A SCENARIO.
//
// A coordinator is hosted only by a RUNNER: `ctxloom run` stands it up and
// its runner serves the session's ONE MCP endpoint — the URL and bearer the
// launch minted into the session record (sessions.Endpoint, session.yaml)
// — over Streamable HTTP (runner/mcp.Endpoint). A scenario that drives the
// agent_* tools needs a real owner standing first, and this fixture is that
// owner: a mock-engine `ctxloom run` held open on a pty for the scenario's
// whole life, torn down with the scenario.
//
// HOW THE HARNESS REACHES IT. The same way the owner's engine does: World.agent
// dials the endpoint the session record names, with its bearer
// (testenv.ConnectMCPEndpoint). No shim process, no discovery path.
//
// WHY THE OWNER'S HARP IS EXPORTED TO EVERY LATER PROCESS. A later `ctxloom
// hook mail-drain` reads the spool the harp on its environment names, and
// those scenarios assert on the owner's own spool.
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
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/core/sessions"
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
	// proves the owner is fully up: the runner binds the session's endpoint
	// and delivers the launch BEFORE the engine reads stdin, so a mock parked
	// in its echo loop implies the endpoint is served.
	sessionOwnerSentinel = "session-owner-standing"

	// sessionOwnerReadyTimeout bounds the wait for that echo. Generous for
	// CI: the runner spawn is a real self-exec + dial-home + StartRun.
	sessionOwnerReadyTimeout = 30 * time.Second

	// sessionFileName is the session record beside a harp's directory in
	// the session store — where the launch bound the session's endpoint.
	sessionFileName = "session.yaml"
)

// sessionOwner is the standing owner: its pty session, the harp it minted,
// and the endpoint its runner serves.
type sessionOwner struct {
	sess     *testenv.PTYSession
	harp     string
	endpoint sessions.Endpoint
}

// standSessionOwner starts the owner from bin (AppBinary, or a copy a probe
// controls) with extraEnv on top of the scenario's isolated environment, and
// waits until it is standing. It must run BEFORE the first agent tool call:
// the agent session is dialed once per scenario, to the owner's endpoint.
func (w *World) standSessionOwner(bin string, extraEnv ...string) error {
	if err := testenv.WriteBundleTree(w.env.ProjectDir, sessionOwnerFragment, fmt.Sprintf("version: \"1.0.0\"\nfragments:\n  %s:\n    content: %q\n", sessionOwnerFragment, "the session owner's own context")); err != nil {
		return fmt.Errorf("session owner: author its fragment: %w", err)
	}
	return w.standSessionOwnerSelecting(bin, []string{"-f", sessionOwnerFragment}, extraEnv...)
}

// standSessionOwnerSelecting is standSessionOwner with the launch's
// selection named by the caller — a profile the scenario authored, so the
// owner's package (what its endpoint serves) carries the scenario's items.
func (w *World) standSessionOwnerSelecting(bin string, selection []string, extraEnv ...string) error {
	if w.owner != nil {
		return errors.New("a session owner is already standing for this scenario")
	}
	if w.mcp != nil {
		return errors.New("the agent MCP session is already open; the session owner must stand before the first tool call, or there is no owner to dial")
	}
	if err := w.env.EnsureHomeMockLabel(sessionOwnerLabel); err != nil {
		return fmt.Errorf("session owner: %w", err)
	}

	before, err := harpDirs(w.env.HomeDir)
	if err != nil {
		return err
	}

	sess, err := w.env.RunPTYFrom(bin, 100, 30, append([]string{"CTXLOOM_MOCK_ECHO_STDIN=1"}, extraEnv...), append([]string{"run", "--llm", sessionOwnerLabel}, selection...)...)
	if err != nil {
		return fmt.Errorf("session owner: start `ctxloom run`: %w", err)
	}
	owner := &sessionOwner{sess: sess}
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

	owner.harp, err = mintedHarp(w.env.HomeDir, before, sess)
	if err != nil {
		return err
	}
	owner.endpoint, err = readSessionEndpoint(w.env.HomeDir, owner.harp)
	if err != nil {
		return fmt.Errorf("session owner: %w; output:\n%s", err, sess.Output())
	}
	w.env.SetChildEnv("CTXLOOM_SESSION_HARP", owner.harp)
	return nil
}

// mintedHarp returns the one harp directory under home that is not in
// before — the session the owner's launch minted — or an error naming what
// was found instead.
func mintedHarp(home string, before map[string]bool, sess *testenv.PTYSession) (string, error) {
	after, err := harpDirs(home)
	if err != nil {
		return "", err
	}
	var minted []string
	for h := range after {
		if !before[h] {
			minted = append(minted, h)
		}
	}
	if len(minted) != 1 {
		return "", fmt.Errorf("session owner: expected the run to mint exactly one session, found %v; output:\n%s", minted, sess.Output())
	}
	return minted[0], nil
}

// readSessionEndpoint reads the endpoint the launch bound on the session
// record (sessions.Store.BindMCP) — what the owner's runner serves and the
// owner's engine dials.
func readSessionEndpoint(home, harp string) (sessions.Endpoint, error) {
	path := filepath.Join(home, filepath.FromSlash(harpSessionsRel), harp, sessionFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		return sessions.Endpoint{}, fmt.Errorf("read the session record %s: %w", path, err)
	}
	var rec struct {
		MCP sessions.Endpoint `yaml:"mcp"`
	}
	if err := yaml.Unmarshal(data, &rec); err != nil {
		return sessions.Endpoint{}, fmt.Errorf("decode the session record %s: %w", path, err)
	}
	if rec.MCP.URL == "" || rec.MCP.Credential == "" {
		return sessions.Endpoint{}, fmt.Errorf("the session record %s binds no MCP endpoint", path)
	}
	return rec.MCP, nil
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
// run's designed shutdown path, which reaps its runner). Nil-safe, so the
// scenario teardown can call it unconditionally.
func (o *sessionOwner) stop() {
	if o == nil {
		return
	}
	o.sess.Close()
}

func registerSessionOwnerSteps(ctx *godog.ScenarioContext) {
	// The owner reads the project's config at start, so this step goes AFTER
	// the fixture that writes it and BEFORE the first tool call.
	ctx.Step(`^a session owner is standing$`, func(c context.Context) error {
		w := worldFrom(c)
		return w.standSessionOwner(w.env.AppBinary)
	})

	// The owner launched on a profile the scenario authored: its package —
	// what its endpoint serves as ctxloom://fragments/{name} and the like —
	// carries that profile's items rather than the fixture's own fragment.
	ctx.Step(`^a session owner is standing on the profile "([^"]*)"$`, func(c context.Context, profile string) error {
		w := worldFrom(c)
		return w.standSessionOwnerSelecting(w.env.AppBinary, []string{"--profile", profile})
	})
}
