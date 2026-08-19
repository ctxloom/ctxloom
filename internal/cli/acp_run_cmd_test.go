package cli

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// resetACPRunFlags restores every acp-run package flag var (and the
// Factory test seam) to its zero value, undone via t.Cleanup — these are
// cobra-bound package vars (see acpRunCmd's init()), so a test that sets
// one must not leak it into the next.
func resetACPRunFlags(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		acpRunLLM = ""
		acpRunProfile = ""
		acpRunWorkDir = ""
		acpRunVerbosity = 0
		acpRunOneShot = false
		acpRunFactory = nil
	})
}

// acpRunStubClient is a minimal pb.Client for testing `acp run` without
// spawning a real ACP subprocess — mirrors internal/operations/oneshot_test.go's
// stubClient (same seam, one door).
//
// It counts BOTH doors an invocation can take: Run (the single turn) and Chat
// (the session), each recording what it actually carried. A test that asserted
// only success would pass on either door, which is precisely the confusion the
// two forms exist to keep apart — a session that quietly took one turn and a
// one-shot that quietly opened a conversation both look like exit 0.
type acpRunStubClient struct {
	// out is the single turn's answer on the Run door.
	out    string
	gotReq *pb.RunStart

	mu sync.Mutex
	// runCalls/chatCalls count each door's use.
	runCalls  int
	chatCalls int
	// chatReq is the last Chat door's request, and turns every message that
	// crossed it — one entry per turn, in order.
	chatReq agent.ChatRequest
	turns   []string
}

func (s *acpRunStubClient) Run(_ context.Context, req *pb.RunStart, _ io.Reader, stdout, _ io.Writer, _ <-chan *pb.WindowSize) (int32, error) {
	s.mu.Lock()
	s.runCalls++
	s.gotReq = req
	s.mu.Unlock()
	_, _ = io.WriteString(stdout, s.out)
	return 0, nil
}
func (s *acpRunStubClient) Info(context.Context) (*pb.LLMInfo, error) { return &pb.LLMInfo{}, nil }
func (s *acpRunStubClient) RunWithModelInfo(context.Context, *pb.RunStart, io.Reader, io.Writer, io.Writer, <-chan *pb.WindowSize) (*pb.RunResult, error) {
	return &pb.RunResult{}, nil
}
func (s *acpRunStubClient) GetSession(context.Context, string) (*agent.Session, error) {
	return nil, nil
}
func (s *acpRunStubClient) WatchSession(context.Context, string) (<-chan *pb.WatchEvent, <-chan error, error) {
	return nil, nil, nil
}

// Chat is a working conversation, not a stub that returns nils: each message
// in is recorded as one turn and answered with one assistant event, and the
// event stream ends when the driver closes its input. Only a chat that
// actually completes turns can tell a loop that took two of them from one that
// took one and stopped.
func (s *acpRunStubClient) Chat(_ context.Context, req agent.ChatRequest) (chan<- agent.ChatMessage, <-chan agent.ChatEvent, <-chan error, error) {
	s.mu.Lock()
	s.chatCalls++
	s.chatReq = req
	s.mu.Unlock()

	in := make(chan agent.ChatMessage)
	events := make(chan agent.ChatEvent)
	errs := make(chan error, 1)
	go func() {
		defer close(events)
		defer close(errs)
		for msg := range in {
			s.mu.Lock()
			s.turns = append(s.turns, msg.Text)
			s.mu.Unlock()
			events <- agent.ChatEvent{Entry: &agent.SessionEntry{
				Type:    agent.EntryTypeAssistant,
				Content: "answering: " + msg.Text,
			}}
		}
	}()
	return in, events, errs, nil
}
func (s *acpRunStubClient) ListSessions(context.Context) ([]agent.SessionMeta, error) {
	return nil, nil
}
func (s *acpRunStubClient) GetPlans(context.Context, string) ([]agent.PlanFile, error) {
	return nil, nil
}
func (s *acpRunStubClient) Kill() {}

// takenTurns returns the turns the chat door carried, under the lock the
// chat goroutine writes them with.
func (s *acpRunStubClient) takenTurns() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.turns...)
}

func (s *acpRunStubClient) doorCounts() (run, chat int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runCalls, s.chatCalls
}

func acpRunTestConfig() *config.Config {
	return config.NewFixture(config.Fixture{
		LM: config.LMConfig{
			Configs: map[string]config.LLMConfig{
				// bypass: this config backs the door-counting/turn-loop/backend
				// tests below, not permission-resolution tests — headless-safe
				// so the unroasted-spinning refusals don't collide with
				// unrelated coverage (see acp_run_cmd_test.go's dedicated
				// permission-pin tests for the unset/unenforceable cases).
				"acp-kiro":    {Type: "acp", Permissions: "bypass"},
				"claude-fast": {Type: "claude-code"},
			},
			Defaults: config.RoleDefaults{Primary: "claude-fast"},
		},
	})
}

// acpRunStubFactory points the leaf's plugin door at stub and returns it.
func acpRunStubFactory(stub *acpRunStubClient) pb.ClientFactory {
	return func(string, string, int) (pb.Client, error) { return stub, nil }
}

// runACPSessionInBackground drives the session form with a deadline: a loop
// defect that never returns would otherwise hang the whole package's test
// binary instead of failing this one test.
func runACPSessionInBackground(t *testing.T, cmd *cobra.Command, cfg *config.Config, opening string, stdin io.Reader) error {
	t.Helper()
	// cobra sets a command's context in Execute; a command built directly for
	// a test has none, and the session's turn loop derives its cancellation
	// from it.
	cmd.SetContext(t.Context())
	done := make(chan error, 1)
	go func() { done <- runACPSessionWithConfig(cmd, cfg, opening, stdin) }()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("the session never ended: stdin reached EOF and the turn loop did not return")
		return nil
	}
}

// TestRunACPRun_RequiresLLMFlag proves the real RunE — not a stand-in —
// refuses to run (and never touches config or the plugin door) when --llm is
// unset: there is no "the" ACP-client label to default to.
func TestRunACPRun_RequiresLLMFlag(t *testing.T) {
	resetACPRunFlags(t)
	cmd, _ := formatCmd(formatText)
	err := runACPRun(cmd, []string{"hello"})
	require.ErrorIs(t, err, errACPRunLLMRequired)
}

// TestRunACPOneshotWithConfig_RejectsNonACPBackend proves --llm is validated
// against the resolved backend type: a label configured for a different
// engine (e.g. claude-code) is refused with a named fix, never silently
// spawned as if it were ACP.
func TestRunACPOneshotWithConfig_RejectsNonACPBackend(t *testing.T) {
	resetACPRunFlags(t)
	acpRunLLM = "claude-fast"

	cmd, _ := formatCmd(formatText)
	err := runACPOneshotWithConfig(cmd, acpRunTestConfig(), "hello")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"claude-code"`)
	assert.Contains(t, err.Error(), "ctxloom llm list")
}

// TestRunACPSessionWithConfig_RejectsNonACPBackend holds the same gate on the
// session form. Both forms reach the same engine surface, so a label that is
// not an ACP-client entry is as wrong for one as for the other — and a gate
// written on only the form it was first noticed in is a gate the other form
// walks around.
func TestRunACPSessionWithConfig_RejectsNonACPBackend(t *testing.T) {
	resetACPRunFlags(t)
	acpRunLLM = "claude-fast"

	cmd, _ := formatCmd(formatText)
	err := runACPSessionWithConfig(cmd, acpRunTestConfig(), "", strings.NewReader(""))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"claude-code"`)
}

// TestRunACPOneshotWithConfig_DrivesConfiguredACPLabel is the end-to-end proof
// (the stub-Factory seam, same shape as operations.TestRunOneshot_*): an
// acp-type label resolves, the prompt rides RunStart.Prompt, and the stub's
// captured output reaches stdout — all through operations.RunOneshot, never a
// CLI-local internal/acp construction.
func TestRunACPOneshotWithConfig_DrivesConfiguredACPLabel(t *testing.T) {
	resetACPRunFlags(t)
	acpRunLLM = "acp-kiro"

	stub := &acpRunStubClient{out: "  hello from kiro  \n"}
	var gotBackend string
	acpRunFactory = func(backendName, _ string, _ int) (pb.Client, error) {
		gotBackend = backendName
		return stub, nil
	}

	cmd, buf := formatCmd(formatText)
	require.NoError(t, runACPOneshotWithConfig(cmd, acpRunTestConfig(), "ping"))

	assert.Equal(t, "acp", gotBackend)
	require.NotNil(t, stub.gotReq.Prompt)
	assert.Equal(t, "ping", stub.gotReq.Prompt.Content)
	assert.Contains(t, buf.String(), "hello from kiro")
}

// TestRunACPOneshot_TakesExactlyOneTurnAndExits is the one-shot half of the
// pair of assertions that keep the two forms honest.
//
// It counts the TURN, not the exit code: --one-shot asks once, at the engine's
// single-turn surface, and leaves. A one-shot that opened a conversation and
// drove a turn in it would still print an answer and still exit 0, so success
// alone proves nothing — the door taken and the number of turns are what
// distinguish the mode from its sibling.
func TestRunACPOneshot_TakesExactlyOneTurnAndExits(t *testing.T) {
	resetACPRunFlags(t)
	acpRunLLM = "acp-kiro"
	stub := &acpRunStubClient{out: "one answer\n"}
	acpRunFactory = acpRunStubFactory(stub)

	cmd, _ := formatCmd(formatText)
	require.NoError(t, runACPOneshotWithConfig(cmd, acpRunTestConfig(), "ping"))

	runs, chats := stub.doorCounts()
	assert.Equal(t, 1, runs, "--one-shot drives exactly one turn")
	assert.Zero(t, chats, "--one-shot never opens a conversation: that is the other form")
	require.NotNil(t, stub.gotReq.Options)
	assert.Equal(t, pb.ExecutionMode_ONESHOT, stub.gotReq.Options.Mode,
		"the single turn runs in the engine's one-shot mode")
}

// TestRunACPSession_TakesEveryTypedTurnUntilStdinEnds is the session half.
//
// Bare `acp run` opens a conversation: every line typed at it is another turn
// against the SAME agent process, and EOF ends it. The assertion is the count
// and the order of the turns the agent actually received — a loop that took
// the first line, answered it and exited would print an answer, exit 0, and
// satisfy any success-only test while having silently been a one-shot.
func TestRunACPSession_TakesEveryTypedTurnUntilStdinEnds(t *testing.T) {
	resetACPRunFlags(t)
	acpRunLLM = "acp-kiro"
	stub := &acpRunStubClient{}
	acpRunFactory = acpRunStubFactory(stub)

	cmd, buf := formatCmd(formatText)
	err := runACPSessionInBackground(t, cmd, acpRunTestConfig(), "", strings.NewReader("first\nsecond\nthird\n"))
	require.NoError(t, err)

	turns := stub.takenTurns()
	require.Len(t, turns, 3, "every typed line is its own turn in one continuing conversation")
	// The first turn carries the assembled context as its lead block, so it is
	// asserted by what it ENDS with; the turns after it are the typed lines
	// verbatim.
	assert.True(t, strings.HasSuffix(turns[0], "first"), "the first typed line opens the conversation, got %q", turns[0])
	assert.Equal(t, []string{"second", "third"}, turns[1:])
	runs, chats := stub.doorCounts()
	assert.Equal(t, 1, chats, "the session opens ONE conversation and keeps it")
	assert.Zero(t, runs, "the session form never reaches the single-turn door")
	assert.Contains(t, buf.String(), "answering: third", "the last turn's answer reaches the caller")
}

// TestRunACPRun_TakesTheFormTheInvocationAsked is the assertion that keeps the
// two forms from collapsing into each other.
//
// Each case runs the SAME invocation apart from --one-shot and asserts which
// engine door it reached: the single-turn door, or the conversation. Both
// forms exit 0 and both print an answer, so nothing else distinguishes them —
// a session that quietly took one turn, or a one-shot that quietly opened a
// conversation, is invisible to every assertion except this one.
func TestRunACPRun_TakesTheFormTheInvocationAsked(t *testing.T) {
	for _, tc := range []struct {
		name          string
		oneShot       bool
		wantRunCalls  int
		wantChatCalls int
	}{
		{name: "--one-shot drives the single turn", oneShot: true, wantRunCalls: 1, wantChatCalls: 0},
		{name: "bare opens the conversation", oneShot: false, wantRunCalls: 0, wantChatCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetACPRunFlags(t)
			acpRunLLM = "acp-kiro"
			acpRunOneShot = tc.oneShot
			stub := &acpRunStubClient{out: "an answer\n"}
			acpRunFactory = acpRunStubFactory(stub)

			cmd, _ := formatCmd(formatText)
			cmd.SetContext(t.Context())
			done := make(chan error, 1)
			go func() {
				done <- runACPRunWithConfig(cmd, acpRunTestConfig(), "ask something", strings.NewReader(""), false)
			}()
			select {
			case err := <-done:
				require.NoError(t, err)
			case <-time.After(10 * time.Second):
				t.Fatal("the invocation never returned")
			}

			runs, chats := stub.doorCounts()
			assert.Equal(t, tc.wantRunCalls, runs, "single-turn door")
			assert.Equal(t, tc.wantChatCalls, chats, "conversation door")
		})
	}
}

// TestRunACPSession_OpensWithTheCommandLinePrompt pins how a prompt given on
// the command line relates to the session: it is the FIRST turn, not a
// different mode. `acp run --llm x "hello"` says hello and then keeps
// listening, which is what makes bare-with-a-prompt and --one-shot two
// different things rather than two spellings of one.
func TestRunACPSession_OpensWithTheCommandLinePrompt(t *testing.T) {
	resetACPRunFlags(t)
	acpRunLLM = "acp-kiro"
	stub := &acpRunStubClient{}
	acpRunFactory = acpRunStubFactory(stub)

	cmd, _ := formatCmd(formatText)
	err := runACPSessionInBackground(t, cmd, acpRunTestConfig(), "opening question", strings.NewReader("follow-up\n"))
	require.NoError(t, err)

	turns := stub.takenTurns()
	require.Len(t, turns, 2, "the command-line prompt opens the conversation and typed lines continue it")
	assert.True(t, strings.HasSuffix(turns[0], "opening question"),
		"the command-line prompt is the first turn (behind the assembled lead block), got %q", turns[0])
	assert.Equal(t, "follow-up", turns[1])
}

// TestRunACPSession_CarriesTheProjectRuntimeAxis proves the session form does
// not quietly answer from the host in a container project.
//
// The single-turn form resolves the runtime axis through
// operations.RunOneshot, so a session that ignored it would isolate one form
// of one verb and not the other — a difference nobody would think to look for,
// and one that only shows up as an engine touching the host it was configured
// to stay off.
func TestRunACPSession_CarriesTheProjectRuntimeAxis(t *testing.T) {
	resetACPRunFlags(t)
	acpRunLLM = "acp-kiro"
	stub := &acpRunStubClient{}
	acpRunFactory = acpRunStubFactory(stub)

	cfg := config.NewFixture(config.Fixture{
		Runtime: string(agent.RuntimeContainerRootless),
		LM: config.LMConfig{
			Configs:  map[string]config.LLMConfig{"acp-kiro": {Type: "acp", Permissions: "bypass"}}, // headless-safe: this test is about the runtime axis, not permission resolution
			Defaults: config.RoleDefaults{Primary: "acp-kiro"},
		},
	})

	cmd, _ := formatCmd(formatText)
	require.NoError(t, runACPSessionInBackground(t, cmd, cfg, "hello", nil))

	stub.mu.Lock()
	defer stub.mu.Unlock()
	assert.Equal(t, agent.RuntimeContainerRootless, stub.chatReq.Runtime,
		"the project's runtime axis rides the conversation")
}

// TestWarnACPSessionWorkspaceAxis_NamesTheAxisItCannotHonour: a conversation
// has no carrier for the workspace axis, so a project that asked for a
// worktree must be TOLD its session runs in the project directory. Silence
// there is the shape of isolation loss that is only discovered by finding
// edits in a tree that was supposed to be untouched.
func TestWarnACPSessionWorkspaceAxis_NamesTheAxisItCannotHonour(t *testing.T) {
	for _, tc := range []struct {
		name      string
		workspace string
		warn      bool
	}{
		{name: "a worktree project is told", workspace: "worktree", warn: true},
		{name: "a shared project is not", workspace: "none", warn: false},
		{name: "an unset axis is not", workspace: "", warn: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var sink strings.Builder
			restore := clidiag.SetSink(&sink)
			t.Cleanup(restore)

			warnACPSessionWorkspaceAxis(tc.workspace)

			if tc.warn {
				assert.Contains(t, sink.String(), "workspace axis")
				assert.Contains(t, sink.String(), "--workspace worktree")
			} else {
				assert.Empty(t, sink.String())
			}
		})
	}
}

// TestRunACPSessionWithConfig_RefusesSilentlyRejectingPosture is
// unroasted-spinning's SESSION ARM: `acp run` registers no
// --permissions/--agent flag, so posture comes solely from the llm label. An
// unset label posture resolves (via resolvePermissionMode) to
// PermissionDefault, which is not SafeHeadless — every gated call in the
// conversation that follows would get reject_once with nobody asked, since
// this session has no channel to render or answer a permission prompt yet.
// It must refuse loudly, up front, instead of opening a session that would
// silently reject everything.
func TestRunACPSessionWithConfig_RefusesSilentlyRejectingPosture(t *testing.T) {
	resetACPRunFlags(t)
	acpRunLLM = "acp-kiro"
	cfg := config.NewFixture(config.Fixture{
		LM: config.LMConfig{
			Configs:  map[string]config.LLMConfig{"acp-kiro": {Type: "acp"}}, // no permissions declared
			Defaults: config.RoleDefaults{Primary: "acp-kiro"},
		},
	})
	stub := &acpRunStubClient{}
	acpRunFactory = acpRunStubFactory(stub)

	cmd, _ := formatCmd(formatText)
	err := runACPSessionInBackground(t, cmd, cfg, "hello", strings.NewReader(""))
	require.Error(t, err, "an unsafe-headless posture must refuse to open the session")
	assert.Contains(t, err.Error(), `"default"`, "the error names the resolved posture")
	assert.Contains(t, err.Error(), "acp-kiro", "the error names the llm label")

	_, chats := stub.doorCounts()
	assert.Zero(t, chats, "the chat door must never be reached when the posture is refused")
}

// TestRunACPOneshotWithConfig_RefusesUnenforceablePlanInsteadOfElevating is
// unroasted-spinning's ONE-SHOT ARM: a user who configures the acp label
// with permissions: plan is asking for read-only. acp cannot enforce a
// read-only plan (backends.EnforcesReadOnlyPlan("acp") is false), so
// CollapsePlanIfUnenforced turns it into default, which is not SafeHeadless.
// A headless oneshot has no human to answer the engine's prompt, so it must
// refuse rather than silently elevate to bypass — the user asked for
// read-only and must never get full bypass instead with no warning.
func TestRunACPOneshotWithConfig_RefusesUnenforceablePlanInsteadOfElevating(t *testing.T) {
	resetACPRunFlags(t)
	acpRunLLM = "acp-kiro"
	cfg := config.NewFixture(config.Fixture{
		LM: config.LMConfig{
			Configs:  map[string]config.LLMConfig{"acp-kiro": {Type: "acp", Permissions: "plan"}},
			Defaults: config.RoleDefaults{Primary: "acp-kiro"},
		},
	})
	stub := &acpRunStubClient{out: "ok"}
	acpRunFactory = acpRunStubFactory(stub)

	cmd, _ := formatCmd(formatText)
	err := runACPOneshotWithConfig(cmd, cfg, "ping")
	require.Error(t, err, "an unenforceable plan posture must refuse, not silently elevate to bypass")
	assert.Contains(t, err.Error(), `"default"`, "the error names the collapsed posture")
	assert.Contains(t, err.Error(), "acp-kiro", "the error names the member")
	assert.Nil(t, stub.gotReq, "the engine must never have run")
}

// TestRunACPSession_AnnouncesTheOpenSessionOnlyOnATerminal drives both sides
// of the human/machine split through the isInteractiveTerminal seam.
//
// A conversation that has opened but not yet been typed at prints nothing, and
// is indistinguishable from a command that hung — so a person gets told the
// session is open and how to end it. A piped invocation is not a person and
// must get none of that chatter in with its turns.
func TestRunACPSession_AnnouncesTheOpenSessionOnlyOnATerminal(t *testing.T) {
	for _, tc := range []struct {
		name     string
		terminal bool
		announce bool
	}{
		{name: "a person at a terminal is told the session is open", terminal: true, announce: true},
		{name: "a piped invocation gets no chatter", terminal: false, announce: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetACPRunFlags(t)
			acpRunLLM = "acp-kiro"
			stub := &acpRunStubClient{}
			acpRunFactory = acpRunStubFactory(stub)

			saved := isInteractiveTerminal
			t.Cleanup(func() { isInteractiveTerminal = saved })
			isInteractiveTerminal = func() bool { return tc.terminal }

			cmd, _ := formatCmd(formatText)
			var stderr strings.Builder
			cmd.SetErr(&stderr)

			require.NoError(t, runACPSessionInBackground(t, cmd, acpRunTestConfig(), "", strings.NewReader("hello\n")))

			if tc.announce {
				assert.Contains(t, stderr.String(), "acp-kiro")
				assert.Contains(t, stderr.String(), "Ctrl-D")
			} else {
				assert.Empty(t, stderr.String())
			}
		})
	}
}
