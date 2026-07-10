package coord

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ctxloom/ctxloom/internal/agentbus"
	"github.com/ctxloom/ctxloom/internal/config"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// Options configures a Coordinator.
type Options struct {
	// Cfg is the loaded project config (required for the production
	// spawner; tests inject Spawner instead).
	Cfg *config.Config
	// ProjectDir is the project working directory the coordinator serves.
	ProjectDir string
	// ProjectKey is the stable project identity keying the durable state
	// dir ("" falls back to a path-derived key).
	ProjectKey string
	// StateDir overrides the state dir entirely (tests).
	StateDir string
	// Spawner overrides the launch seam (tests). Nil = production.
	Spawner Spawner
	// Factory is the production spawner's plugin-construction test seam.
	Factory pb.ClientFactory
	// Clock overrides command time (tests). Nil = time.Now.
	Clock func() time.Time
}

// Coordinator is the runtime coordinator: durable CQRS stores + credential
// registry + the delegation orchestration, served over gRPC (RunnerChannel)
// and streamable-HTTP MCP by the listeners in httpserver.go.
type Coordinator struct {
	projectDir string
	stateDir   string
	ephemeral  bool // state dir is per-process (owner lock lost); no adoption
	now        func() time.Time

	baseCtx context.Context
	cancel  context.CancelFunc

	releaseOwner func()

	runs     *Store
	runsF    *runsFold
	queueF   *queueFold
	rosterF  *rosterFold
	reportsF *reportsFold
	mail     *Store
	mailF    *mailFold
	auditJ   *Store

	spawner Spawner
	slots   *turnSlots
	hub     *agentbus.TapHub
	// custom maps host-relay tool names ("ctxloom/<tool>") onto the
	// coordinator-side handlers the hosting process injects.
	custom map[string]CustomHandler

	mu        sync.Mutex
	attach    map[string]*childRt // runID → runtime attachment
	byHarp    map[string]*childRt // harp → current attachment
	polls     map[string]*parkedPoll
	delivered map[string][]string       // role → delivered-but-unacked ids
	runners   map[string]*runnerSession // credHash → connected runner
	chans     map[string]*runChan       // role harp → live RunChannel

	srv *coordServing // listeners (httpserver.go); nil until Serve

	busMu      sync.Mutex
	busServers map[string]*agentbus.Server // owner harp → viewer-verb socket

	closeOnce sync.Once
}

// New opens (or adopts) the project's coordinator state and starts the
// orchestration core. Listeners come up separately via Serve (httpserver.go)
// so tests can run the core without ports.
func New(opts Options) (*Coordinator, error) {
	now := opts.Clock
	if now == nil {
		now = time.Now
	}
	stateDir := opts.StateDir
	var release func()
	ephemeral := false
	if stateDir == "" {
		key := opts.ProjectKey
		if key == "" {
			key = filepath.Base(opts.ProjectDir) + "-" + hashToken(opts.ProjectDir)[:12]
		}
		dir, err := stateDirForProject(key)
		if err != nil {
			return nil, err
		}
		rel, err := claimOwner(dir)
		if err != nil {
			// Another live session-owning process holds this project's
			// journals: single writer per journal holds across processes,
			// so this coordinator runs on an ephemeral state dir (no
			// adoption; its own state dies with it). Warned, not fatal —
			// concurrent sessions in one project are legitimate.
			clidiag.Warn("ctxloom", "coordinator state for this project is owned by another live session; running this session's coordinator on ephemeral state (no cross-relaunch adoption)")
			tmp, terr := os.MkdirTemp("", "ctxloom-coord-")
			if terr != nil {
				return nil, fmt.Errorf("coord: ephemeral state dir: %w", terr)
			}
			dir = tmp
			ephemeral = true
		} else {
			release = rel
		}
		stateDir = dir
	}

	c := &Coordinator{
		projectDir:   opts.ProjectDir,
		stateDir:     stateDir,
		ephemeral:    ephemeral,
		now:          now,
		releaseOwner: release,
		spawner:      opts.Spawner,
		slots:        newTurnSlots(agentTurnCap),
		hub:          agentbus.NewTapHub(),
		attach:       make(map[string]*childRt),
		byHarp:       make(map[string]*childRt),
		polls:        make(map[string]*parkedPoll),
		delivered:    make(map[string][]string),
		runners:      make(map[string]*runnerSession),
		chans:        make(map[string]*runChan),
		busServers:   make(map[string]*agentbus.Server),
	}
	c.baseCtx, c.cancel = context.WithCancel(context.Background())
	if c.spawner == nil {
		if opts.Cfg == nil {
			c.closePartial()
			return nil, errors.New("coord: Options.Cfg is required without an injected Spawner")
		}
		c.spawner = newProdSpawner(opts.Cfg, opts.ProjectDir, opts.Factory)
	}

	c.runsF, c.queueF, c.rosterF, c.reportsF = newRunsFold(), newQueueFold(), newRosterFold(), newReportsFold()
	runs, err := openStore(filepath.Join(stateDir, "runs.jsonl"), c.runsF, c.queueF, c.rosterF, c.reportsF)
	if err != nil {
		c.closePartial()
		return nil, err
	}
	c.runs = runs
	c.mailF = newMailFold()
	mail, err := openStore(filepath.Join(stateDir, "mailbox.jsonl"), c.mailF)
	if err != nil {
		c.closePartial()
		return nil, err
	}
	c.mail = mail
	auditJ, err := openStore(filepath.Join(stateDir, "interactions.jsonl"))
	if err != nil {
		c.closePartial()
		return nil, err
	}
	c.auditJ = auditJ

	c.adopt()
	go c.runnerWatchdog()
	return c, nil
}

// adopt reconciles state read from disk with the fresh process (acceptance
// (4)): queued mail is preserved as-is (drainable); non-ended HOST runs died
// with the previous process and are terminated (orphaned); non-ended
// CONTAINER runs may still be alive — their credentials stay valid so their
// RunnerChannels can re-Hello against the re-bound endpoint, and a grace
// timer terminates any that never dial back (runner loss). Live-child
// engine-stream continuity is Wave C.
func (c *Coordinator) adopt() {
	type pending struct {
		runID     string
		credHash  string
		container bool
	}
	var stale []pending
	c.runs.View(func() {
		for id, r := range c.runsF.runs {
			if r.Ended {
				continue
			}
			stale = append(stale, pending{runID: id, credHash: r.CredHash, container: r.Runtime == "container"})
		}
	})
	for _, p := range stale {
		if !p.container {
			c.terminateRun(p.runID, CauseOrphaned, "coordinator relaunched; the child's engine died with the previous process")
			continue
		}
		// Container run: its runner may still be alive. Give it one
		// runner-loss window to re-Hello; terminate as runner loss if it
		// never does.
		go func(runID, credHash string) {
			select {
			case <-time.After(runnerLossTimeout):
			case <-c.baseCtx.Done():
				return
			}
			c.mu.Lock()
			_, connected := c.runners[credHash]
			c.mu.Unlock()
			if !connected {
				c.terminateRun(runID, CauseRunnerLoss, "no runner re-Hello after coordinator relaunch")
			}
		}(p.runID, p.credHash)
	}
}

// Close tears the coordinator down: listeners, bus sockets, journals, owner
// lock. Live children are killed via their launch close (the run process is
// their lifetime).
func (c *Coordinator) Close() {
	c.closeOnce.Do(func() {
		c.cancel()
		c.mu.Lock()
		attachments := make([]*childRt, 0, len(c.attach))
		for _, rt := range c.attach {
			attachments = append(attachments, rt)
		}
		c.mu.Unlock()
		for _, rt := range attachments {
			c.mu.Lock()
			closeFn := rt.close
			c.mu.Unlock()
			if closeFn != nil {
				closeFn()
			}
		}
		c.busMu.Lock()
		for _, srv := range c.busServers {
			_ = srv.Close()
		}
		c.busServers = map[string]*agentbus.Server{}
		c.busMu.Unlock()
		if c.srv != nil {
			c.srv.close()
		}
		c.closePartial()
		if c.ephemeral {
			_ = os.RemoveAll(c.stateDir)
		}
	})
}

// closePartial releases what New acquired so far (also Close's tail).
func (c *Coordinator) closePartial() {
	if c.runs != nil {
		_ = c.runs.Close()
	}
	if c.mail != nil {
		_ = c.mail.Close()
	}
	if c.auditJ != nil {
		_ = c.auditJ.Close()
	}
	if c.releaseOwner != nil {
		c.releaseOwner()
		c.releaseOwner = nil
	}
}

// audit appends one interaction record (best-effort; the audit journal has no
// projection and never gates a verb).
func (c *Coordinator) audit(kind, actor string, detail map[string]string) {
	if err := c.auditJ.Exec(func() ([]Fact, error) {
		return []Fact{factAt("interaction", c.now(), interaction{Kind: kind, Actor: actor, Detail: detail})}, nil
	}); err != nil {
		clidiag.Warn("ctxloom", "coordinator audit (%s): %v", kind, err)
	}
}

// RegisterSessionOwner mints the depth-0 credential identifying a session
// owner (the parent harness `ctxloom run`/`ctxloom acp` launches). The token
// is returned exactly once for the env seam; only its hash is journaled.
func (c *Coordinator) RegisterSessionOwner(harp string) (token string, err error) {
	token, credHash, err := mintToken()
	if err != nil {
		return "", err
	}
	if err := c.runs.Exec(func() ([]Fact, error) {
		return []Fact{factAt(factSessionCred, c.now(), sessionCred{Harp: harp, Project: c.projectDir, CredHash: credHash})}, nil
	}); err != nil {
		return "", err
	}
	return token, nil
}

// RevokeSessionOwner revokes a session-owner credential (session teardown).
func (c *Coordinator) RevokeSessionOwner(token string) {
	credHash := hashToken(token)
	if err := c.runs.Exec(func() ([]Fact, error) {
		if _, ok := c.runsF.identityFor(credHash); !ok {
			return nil, nil
		}
		return []Fact{factAt(factSessionCredRevoked, c.now(), sessionCred{CredHash: credHash})}, nil
	}); err != nil {
		clidiag.Warn("ctxloom", "coordinator: revoke session credential: %v", err)
	}
}

// Identify resolves a presented bearer token to its identity, constant-time
// per candidate. Used per-request by the MCP auth middleware and
// per-stream-establishment + per-request by the gRPC interceptors.
func (c *Coordinator) Identify(token string) (Identity, bool) {
	var (
		id Identity
		ok bool
	)
	c.runs.View(func() {
		id, ok = verifyToken(token, c.runsF.creds)
	})
	if ok && id.Project == "" {
		id.Project = c.projectDir
	}
	return id, ok
}

// Roster lists the coordinator's children — the single state behind BOTH
// transports (MCP and the surviving bus roster verb).
func (c *Coordinator) Roster() []RosterEntry {
	var out []RosterEntry
	c.runs.View(func() { out = c.rosterF.snapshot() })
	return out
}

// AgentSend delivers a message per §6a delivery-by-state. Children address
// only their parent (hub-and-spoke); the session owner addresses its
// children by harp.
func (c *Coordinator) AgentSend(caller Identity, to, kind, body string) (string, error) {
	_, _, disposition, err := c.peerSend(caller, to, kind, body)
	return disposition, err
}

// peerSend is the shared send verb behind AgentSend (bare-mcp local path)
// and the plane-2 PeerSendRequest handler: routing policy, durable queue,
// delivery-by-state. delivered reports a completed waiting receive (a local
// parked poll, or a tentative push into the recipient runner's parked recv).
func (c *Coordinator) peerSend(caller Identity, to, kind, body string) (msgID string, delivered bool, disposition string, err error) {
	if caller.IsChild() {
		parent := ""
		c.runs.View(func() {
			if r := c.runsF.currentRun(caller.Harp); r != nil {
				parent = r.ParentHarp
			}
		})
		if parent == "" {
			return "", false, "", fmt.Errorf("agent_send: unknown sender %q: not a child of this coordinator", caller.Harp)
		}
		if to != ParentAddress && to != parent {
			return "", false, "", ErrPeerRouting
		}
		c.audit("agent_send", caller.Harp, map[string]string{"to": parent, "kind": kind})
		id, completed, qerr := c.queueMail(caller.Harp, parent, kind, body)
		if qerr != nil {
			return "", false, "", qerr
		}
		return id, completed, "sent to the coordinator", nil
	}

	if to == ParentAddress {
		return "", false, "", errors.New("agent_send: this session is the coordinator — it has no parent; address a child by its harp")
	}
	known := false
	c.runs.View(func() { known = c.runsF.currentRun(to) != nil })
	if !known {
		return "", false, "", fmt.Errorf("agent_send: unknown recipient %q: not a child of this session (spawn it with agent_run first)", to)
	}
	c.audit("agent_send", caller.Harp, map[string]string{"to": to, "kind": kind})
	msgID, delivered, err = c.queueMail(caller.Harp, to, kind, body)
	if err != nil {
		return "", false, "", err
	}
	if delivered {
		return msgID, true, "completed the child's waiting agent_recv", nil
	}
	switch c.driveQueued(to) {
	case StateEnded:
		return msgID, false, "child session had ended — resuming it with the message as its next turn", nil
	case StateIdle:
		return msgID, false, "delivering as a new turn", nil
	case StateQueued:
		return msgID, false, "queued: the child has not started yet; it will drain its mailbox after its first turn", nil
	default: // executing / parked race
		return msgID, false, "queued mid-turn: delivered at the child's next turn boundary", nil
	}
}

// AgentRecv drains the caller's own durable mailbox, parking up to wait.
// Delivery is AT-LEAST-ONCE: this call implicitly acknowledges the messages a
// PRIOR recv returned (cursor-ack); unacknowledged deliveries are re-delivered
// after a coordinator relaunch. Dupes are deduped on message id at the store.
func (c *Coordinator) AgentRecv(ctx context.Context, caller Identity, wait time.Duration) ([]Message, error) {
	c.audit("agent_recv", caller.Harp, nil)
	return c.recvMail(ctx, caller.Harp, wait)
}

// AgentStop kills a child run (KillRun semantics): the engine/container dies,
// the slot frees, the terminal is journaled, and the credential is revoked.
func (c *Coordinator) AgentStop(caller Identity, harp string) (string, error) {
	if caller.IsChild() {
		return "", errors.New("agent_stop: only the coordinating session may stop its children")
	}
	var rec *RunRecord
	c.runs.View(func() {
		if r := c.runsF.currentRun(harp); r != nil {
			cp := *r
			rec = &cp
		}
	})
	if rec == nil {
		return "", fmt.Errorf("agent_stop: unknown session %q: not a child of this session", harp)
	}
	if rec.Ended {
		return fmt.Sprintf("child %s had already ended (%s)", harp, rec.Cause), nil
	}
	c.audit("agent_stop", caller.Harp, map[string]string{"harp": harp, "run_id": rec.RunID})
	c.terminateRun(rec.RunID, CauseStopped, fmt.Sprintf("stopped by %s", caller.Harp))
	return fmt.Sprintf("stopped child %s; its execution slot is freed (a later agent_send resumes it as a fresh run)", harp), nil
}

// Inject delivers user-typed text into a child by the same §6a
// delivery-by-state rules as a parent agent_send, with sender identity
// UserSender. INVARIANT (decision O3): the KindUserInjected mirror notice to
// the target's parent fires on EVERY successful injection — a coordinator's
// picture of its child never diverges without a trace.
func (c *Coordinator) Inject(harp, text string) (string, error) {
	var rec *RunRecord
	c.runs.View(func() {
		if r := c.runsF.currentRun(harp); r != nil {
			cp := *r
			rec = &cp
		}
	})
	if rec == nil {
		return "", fmt.Errorf("inject: %q is not a child of this coordinator: %w", harp, agentbus.ErrNotInjectable)
	}
	c.audit("inject", UserSender, map[string]string{"harp": harp})
	_, completed, err := c.queueMail(UserSender, harp, "", text)
	if err != nil {
		return "", err
	}
	if _, _, merr := c.queueMail(harp, rec.ParentHarp, KindUserInjected, injectDigest(text)); merr != nil {
		clidiag.Warn("ctxloom", "inject %s: mirror notice: %v", harp, merr)
	}
	if completed {
		return agentbus.DeliveryCompletedRecv, nil
	}
	switch c.driveQueued(harp) {
	case StateEnded:
		return agentbus.DeliveryResumed, nil
	case StateIdle:
		return agentbus.DeliveryNewTurn, nil
	default:
		return agentbus.DeliveryQueued, nil
	}
}

// injectDigestRunes bounds the mirror notice body: enough for the parent to
// recognize what was said, never the bulk.
const injectDigestRunes = 120

func injectDigest(text string) string {
	r := []rune(text)
	if len(r) <= injectDigestRunes {
		return text
	}
	return fmt.Sprintf("%s… (%d chars total)", string(r[:injectDigestRunes]), len(r))
}

// Hub exposes the live-observation tap hub (the bus observe verb's source).
func (c *Coordinator) Hub() *agentbus.TapHub { return c.hub }

// BindSessionSocket binds the viewer-verb bus socket (observe/roster/inject —
// send/recv died with the shim) under the owner harp's session dir, exactly
// where the pre-coordinator orchestrator bound it, so the feed resolver and
// viewer discovery are unchanged. Idempotent per harp.
func (c *Coordinator) BindSessionSocket(ownerHarp string) error {
	if ownerHarp == "" {
		return nil
	}
	c.busMu.Lock()
	defer c.busMu.Unlock()
	if _, ok := c.busServers[ownerHarp]; ok {
		return nil
	}
	path, err := sessionSocketPath(ownerHarp)
	if err != nil {
		return err
	}
	roster := func() []agentbus.RosterEntry {
		snap := c.Roster()
		out := make([]agentbus.RosterEntry, len(snap))
		for i, e := range snap {
			out[i] = agentbus.RosterEntry{Harp: e.Harp, Agent: e.Agent, State: e.State, Parent: e.Parent, LastActivityUnix: e.LastActivityUnix}
		}
		return out
	}
	srv, err := agentbus.Listen(path, c.hub, roster, c.Inject)
	if err != nil {
		return err
	}
	c.busServers[ownerHarp] = srv
	return nil
}

// sunPathHeadroom keeps socket paths under the portable sun_path limit.
const sunPathHeadroom = 100

func sessionSocketPath(ownerHarp string) (string, error) {
	if dir, err := paths.HarpDir(ownerHarp); err == nil {
		path := filepath.Join(dir, "agent-bus.sock")
		if len(path) <= sunPathHeadroom {
			if merr := os.MkdirAll(dir, 0o755); merr == nil {
				return path, nil
			}
		}
	}
	dir, err := os.MkdirTemp("", "ctxloom-bus-")
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "agent-bus.sock"), nil
}
