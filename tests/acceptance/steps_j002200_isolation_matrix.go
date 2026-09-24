//go:build acceptance

// J002200 matrix: the per-engine config-home isolation payload the top of
// j002200_isolation.feature's own doc used to flag as "needs a real
// registered-engine fixture... out of hermetic scope here".
// Filled here via a SPY fixture, WITHOUT giving up the mock's hermetic,
// no-live-credential, no-network guarantee.
//
// SAFETY (the single load-bearing property of every step in this file):
// config.yaml names a REAL registered backend type (claude-code/codex/
// opencode), which drives isolation.Prepare exactly as a live
// run would — but PATH is rebuilt FROM SCRATCH to "<spy dir>:/usr/bin:/bin"
// for the duration of the run (isoMatrixSanitizedPATH), never merely
// prepended to the inherited PATH. That distinction is load-bearing: an
// early version of this fixture prepended a spy dir onto the inherited PATH
// and, for the one engine (opencode) whose spy never got invoked (see
// below), silently fell through to the REAL, already-authenticated opencode
// binary elsewhere on the developer's PATH and made a real (if cheap)
// completion call — discovered by hand while building this file, not by a
// gate. Rebuilding PATH from scratch instead means the literal binary name
// a backend execs ("claude") resolves
// ONLY to the recording script this file writes, or to nothing at all
// (ENOENT) — never to a real installed engine. No scenario in this file
// makes a network call or touches a real credential.
//
// THE SPY: isoMatrixSpyScript records the per-agent config-home variables it
// was actually handed (isoSpyEnvAllowlist — a CLOSED allowlist, never the
// whole environment; see that list's doc for the secret-leak hazard the
// allowlist closes) out of what a real engine process would receive, per
// internal/core/agent/base.go's BuildEnv (os.Environ() of the plugin
// subprocess + the backend's own env + the request env) — plus a `cat` of
// whatever credential file its own env vars point it at (there must be none:
// nothing is copied into a session home). This is captured
// from INSIDE the spawned process because the
// per-agent scratch config-home does NOT survive past the run: Cleanup
// removes it unconditionally once the run exits (confirmed by hand — a
// naive design that tried to inspect the scratch dir from outside, after
// the `ctxloom run` subprocess returned, always found it already gone).
//
// OPENCODE IS SCOPED DIFFERENTLY. Every other engine here is launched via a
// plain oneshot exec (agent.LaunchBackend.ExecuteCLI); opencode's real
// launch path is ACP, a stateful JSON-RPC handshake over stdio
// (internal/opencode) — a spy that just dumps its env and exits never
// completes that handshake, so the run errors before any output reaches the
// spy's output file at all (confirmed by hand: the file is never created).
// opencode's fail-loud/warn CONTRACT is still proven for it below (Scenario
// Outlines "refuses to start" / "proceed without any isolation finding" —
// both fire BEFORE any engine spawn is attempted, so they need no spy
// cooperation), but the exact spawned-env PAYLOAD (the
// XDG_DATA_HOME-vs-XDG_DATA_HOME/opencode nesting subtlety) is not
// independently re-proven here. It is already pinned at the Go level by
// internal/adapters/isolation/auth_test.go's
// TestHostCredentialSeed_OpencodeSeedsAuthJsonUnderXdgDataOpencode. See
// j002200_isolation.doc.md for the full accounting of what is and is not proven
// where.
//
// RE-VERIFIED: an earlier review claimed "the Examples table still
// lists opencode alongside four engines whose payload is checked" — re-checked
// against features/journeys/j002200_isolation.feature as it stands today and that is no
// longer true. The ONE Examples table that asserts on spy payload ("A
// worktree run copies the host credential ...") lists only claude-code and
// codex; opencode appears only in the two pre-spawn-only outlines named
// above, which read the run's OUTPUT, never the spy. So the specific
// misleading-coverage-claim harm the earlier review named does not hold
// against the current file — nothing to fix here. The underlying limitation
// (opencode's spy is never invoked, because its real launch is a stateful
// ACP handshake) is real and stays documented above; only the "the Examples
// table hides that" half was refuted.
package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cucumber/godog"

	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
)

// isoSpyEnvAllowlist is the CLOSED set of environment variables the spy is
// permitted to record — every per-agent config-home knob this file's
// assertions actually read (isoParseSpyEnv's callers), plus the two
// diagnostics (HOME, PWD) that make a failure legible.
//
// It is an allowlist, never a denylist, because of the failure path. The spy
// used to dump its whole environment (`env`), and godog prints a
// spy-reading step's captured body VERBATIM whenever that step fails — so the
// first red isolation scenario on a developer box copied that developer's
// entire environment into the console, and from there into CI logs, pasted
// bug reports and agent transcripts. During the audit that found this, a real
// PyPI TWINE_PASSWORD was printed exactly that way. Cloud credentials,
// registry tokens and SSH agent sockets all live in the same place. A
// denylist would have to enumerate every secret-bearing variable name any
// developer might ever export, which is not a knowable set; this list instead
// enumerates the handful the assertions read, so anything else structurally
// cannot reach the capture file. ADD TO THIS LIST ONLY A VARIABLE AN
// ASSERTION READS, AND ONLY ONE WHOSE VALUE IS A PATH.
var isoSpyEnvAllowlist = []string{
	"CLAUDE_CONFIG_DIR",
	"CODEX_HOME",
	"XDG_CONFIG_HOME",
	"XDG_DATA_HOME",
	"XDG_CACHE_HOME",
	"HOME",
	"PWD",
}

// isoSpyEnvAllowlistShell renders isoSpyEnvAllowlist as the literal word list
// the spy script's `for` loop iterates. The names are fixed identifiers from
// the const-adjacent slice above, never scenario input, so the `eval`
// indirection they feed carries no injection surface.
var isoSpyEnvAllowlistShell = strings.Join(isoSpyEnvAllowlist, " ")

// isoMatrixSpyScript is the recording fixture every scenario in this file
// installs in place of a real engine binary. POSIX sh (not bash) — the
// sanitized PATH deliberately carries no promise bash itself resolves from
// it; /usr/bin/sh is on every box that can run this suite at all.
//
// It records ONLY isoSpyEnvAllowlist's variables, never the whole
// environment — see that list's doc for the secret-leak hazard that
// constraint exists to close.
//
// It ANSWERS in the engine's structured protocol (claude's stream-json: an
// init line, one assistant text block, a result) because a `ctxloom run
// --one-shot` drives the engine one structured turn per process and reads
// its answer off that stream; a plain line would be no answer at all.
//
// The four non-env sections it also captures are all FILES CTXLOOM ITSELF
// CREATED inside a config home ctxloom provisioned, in a throwaway test HOME —
// a copy of the obviously-fake isoFixtureCredMarker, a directory listing, a
// mode string, and the ctxloom-GENERATED .claude.json. None of them can carry
// a developer's own secret the way a bare `env` dump could, and every one of
// them exists because the thing being asserted does not survive the run:
// S8's teardown reaps the instance at session end, so anything read from
// OUTSIDE, after `ctxloom run` returns, is reading a directory that is
// legitimately gone. Capture during, assert after.
var isoMatrixSpyScript = `#!/bin/sh
out="$CTXLOOM_ISOSPY_OUT"
{
  echo "===ENV==="
  for k in ` + isoSpyEnvAllowlistShell + `; do
    eval "v=\${$k-}"
    [ -n "$v" ] && echo "$k=$v"
  done
  echo "===ARGV==="
  printf '%s\n' "$@"
  echo "===STDIN==="
  cat
  echo "===SETUP_TOKEN==="
  if [ -z "${CLAUDE_CODE_OAUTH_TOKEN-}" ]; then echo unset
  elif [ "$CLAUDE_CODE_OAUTH_TOKEN" = "` + isoFixtureSetupToken + `" ]; then echo fixture
  else echo other; fi
  echo "===SHARED_LOGIN==="
  if [ -z "${CLAUDE_SECURESTORAGE_CONFIG_DIR+x}" ]; then echo unset
  else echo "set:$CLAUDE_SECURESTORAGE_CONFIG_DIR"; fi
  echo "===CLAUDE_CONFIG_DIR_CREDS==="
  [ -n "$CLAUDE_CONFIG_DIR" ] && cat "$CLAUDE_CONFIG_DIR/.credentials.json" 2>/dev/null
  echo "===CODEX_HOME_CREDS==="
  [ -n "$CODEX_HOME" ] && cat "$CODEX_HOME/auth.json" 2>/dev/null
  echo "===CONFIG_HOME_LISTING==="
  for d in "$CLAUDE_CONFIG_DIR" "$CODEX_HOME"; do
    [ -n "$d" ] && [ -d "$d" ] && echo "DIR $d"
  done
  for f in "$CLAUDE_CONFIG_DIR/.credentials.json" "$CODEX_HOME/auth.json"; do
    [ -f "$f" ] && echo "MODE $(ls -l "$f" | cut -d' ' -f1) $f"
  done
  echo "===CLAUDE_INSTANCE_CONFIG==="
  [ -n "$CLAUDE_CONFIG_DIR" ] && cat "$CLAUDE_CONFIG_DIR/.claude.json" 2>/dev/null
} > "$out" 2>/dev/null
echo '{"type":"system","subtype":"init","session_id":"iso-spy","model":"m"}'
echo '{"type":"assistant","message":{"content":[{"type":"text","text":"ctxloom-isolation-matrix-spy"}]}}'
echo '{"type":"result","subtype":"success","usage":{"input_tokens":1},"modelUsage":{"m":{"outputTokens":1}}}'
exit 0
`

// isoFixtureSetupToken is the obviously fake token a scenario stores with
// the setup-token file. The spy reports only whether the var it was handed
// EQUALS it — never the value — so a developer's own exported token (which
// wins over the store) can never land in a spy dump.
const isoFixtureSetupToken = "sk-ant-oat01-ISO-MATRIX-FIXTURE-NOT-A-REAL-SECRET"

// The two halves of claude's OAuth credential, each obviously fake. The
// fixture stands for Alice's own native login: something real for "never
// copied" and "never modified" to fail on.
const (
	isoFixtureAccessMarker  = "ISO-MATRIX-FIXTURE-ACCESS-TOKEN-NOT-A-REAL-SECRET"
	isoFixtureRefreshMarker = "ISO-MATRIX-FIXTURE-REFRESH-TOKEN-NOT-A-REAL-SECRET"
)

// isoFixtureClaudeCred is the host ~/.claude/.credentials.json fixture: valid
// JSON in claude's live claudeAiOauth shape (probe-verified).
const isoFixtureClaudeCred = `{"claudeAiOauth":{"accessToken":"` + isoFixtureAccessMarker + `","refreshToken":"` + isoFixtureRefreshMarker + `","expiresAt":1,"refreshTokenExpiresAt":2,"subscriptionType":"max"}}`

// isoCredFixtureContent is the host credential-file body for engine's native
// login.
func isoCredFixtureContent(engine string) (string, error) {
	switch engine {
	case "claude-code":
		return isoFixtureClaudeCred, nil
	default:
		return "", fmt.Errorf("iso matrix: no credential fixture content for engine %q", engine)
	}
}

// The three classes of Alice's OWN data the personal-claude-config fixture
// seeds, each obviously fake. They exist to be searched FOR in the generated
// instance config: an allow-list that quietly widened would carry one of them
// across, and a fixture without them could not tell that apart from a working
// one.
const (
	isoFixturePersonalSecret  = "ISO-MATRIX-ALICE-OWN-MCP-TOKEN-NOT-A-REAL-SECRET"
	isoFixturePersonalEmail   = "alice-personal@example.invalid"
	isoFixturePersonalHistory = "ISO-MATRIX-ALICE-OWN-PROMPT-HISTORY"
)

// isoBinaryNames maps a scenario's engine token to the literal binary
// name(s) that engine's backend execs (internal/{claude,codex,
// opencode}/backend.go's BinaryPath defaults) — the name(s) the
// spy script must answer to on the sanitized PATH.
func isoBinaryNames(engine string) ([]string, error) {
	switch engine {
	case "claude-code":
		return []string{"claude"}, nil
	default:
		return nil, fmt.Errorf("iso matrix: unknown engine %q", engine)
	}
}

// isoAuthEnvVars are every env var that authenticates engine — its token
// var first, then the others — read off the engine's own declaration
// (engine.TokenAuth), not re-typed here.
func isoAuthEnvVars(engine string) ([]string, error) {
	a, ok := isoTokenAuth(engine)
	if !ok {
		return nil, fmt.Errorf("iso matrix: engine %q declares no token auth", engine)
	}
	return append([]string{a.TokenVar}, a.EnvTriggers...), nil
}

// isoAPIKeyEnvVar is the first var the engine declares as authenticating it
// without the token (claude: its API key).
func isoAPIKeyEnvVar(engine string) (string, error) {
	a, ok := isoTokenAuth(engine)
	if !ok || len(a.EnvTriggers) == 0 {
		return "", fmt.Errorf("iso matrix: engine %q has no API-key alternative", engine)
	}
	return a.EnvTriggers[0], nil
}

// isoCredHostPath is where engine keeps its NATIVE login, relative to HOME:
// the file a scenario seeds to prove ctxloom never copies or modifies it.
func isoCredHostPath(engine string) (string, error) {
	switch engine {
	case "claude-code":
		return filepath.Join(claude.ConfigDirName, ".credentials.json"), nil
	default:
		return "", fmt.Errorf("iso matrix: no known native login path for engine %q", engine)
	}
}

// isoHostHomeDirRel maps an engine to the directory it uses as its config home
// on the host when NOTHING relocates it — the directory an in-tree AGENT run
// must stop reading and writing. Relative to $HOME.
func isoHostHomeDirRel(engine string) (string, error) {
	switch engine {
	case "claude-code":
		return ".claude", nil
	default:
		return "", fmt.Errorf("iso matrix: no known host config home for engine %q", engine)
	}
}

// isoInstanceLeaf maps an engine to its leaf inside ONE SESSION's config-home
// instance — `.ctxloom/state/<harp>/home/<leaf>`. The leaves duplicate
// internal/{claude,codex}'s own constants rather than importing them: this
// file's whole point is to observe the value a REAL run hands a REAL engine
// process from the outside, so deriving the expectation from the same helper
// the production code uses would make the assertion tautological.
func isoInstanceLeaf(engine string) (string, error) {
	switch engine {
	case "claude-code":
		return "claude", nil
	default:
		return "", fmt.Errorf("iso matrix: engine %q has no ctxloom-controlled in-tree home", engine)
	}
}

// isoInstanceHomes globs every config-home instance that exists for engine
// under the sessions store of homeDir (Alice's fake ~). The HARP is not
// knowable to a test from the outside — ctxloom mints it per run — so the
// glob is how an outside observer names a per-session path at all. Its
// CARDINALITY is the assertion: exactly one for a single opted-in run, zero
// when nothing opted in.
func isoInstanceHomes(homeDir, engine string) ([]string, error) {
	leaf, err := isoInstanceLeaf(engine)
	if err != nil {
		return nil, err
	}
	return filepath.Glob(filepath.Join(homeDir, ".ctxloom", "sessions", "*", "home", leaf))
}

// isoInstanceHomeShape checks that val really is a per-session instance path
// for engine under homeDir's sessions store, and returns the harp it is keyed
// by. The shape is checked component by component rather than against a
// precomputed string because the harp is the one part a test cannot predict —
// and it is exactly the part that must be there.
func isoInstanceHomeShape(homeDir, engine, val string) (harp string, err error) {
	leaf, err := isoInstanceLeaf(engine)
	if err != nil {
		return "", err
	}
	prefix := filepath.Join(homeDir, ".ctxloom", "sessions") + string(filepath.Separator)
	suffix := string(filepath.Separator) + filepath.Join("home", leaf)
	if !strings.HasPrefix(val, prefix) || !strings.HasSuffix(val, suffix) {
		return "", fmt.Errorf("%q is not a per-session config-home instance: want %s<harp>%s", val, prefix, suffix)
	}
	harp = strings.TrimSuffix(strings.TrimPrefix(val, prefix), suffix)
	if harp == "" || strings.Contains(harp, string(filepath.Separator)) {
		return "", fmt.Errorf("%q carries no single harp component between %q and %q — the instance must be keyed by SESSION", val, prefix, suffix)
	}
	return harp, nil
}

// isoCredsSectionMarker maps an engine to the spy script's own marker line
// preceding its credential dump (see isoMatrixSpyScript).
func isoCredsSectionMarker(engine string) (string, error) {
	switch engine {
	case "claude-code":
		return "===CLAUDE_CONFIG_DIR_CREDS===", nil
	default:
		return "", fmt.Errorf("iso matrix: no credential section marker for engine %q", engine)
	}
}

// isoIsPerAgentScratch reports whether a config-home value lies inside the
// per-agent scratch tree — <HOME>/.ctxloom/sessions/... under the HOME the
// harness hands the run. Its truth for a config-home var proves isolation
// engaged; its falsity across every such var proves the none axis really did
// leave the engine on the host's own config.
//
// ANCHORED AT THE RUN'S HOME, NOT A SUBSTRING. This used to test for
// ".ctxloom/sessions" anywhere in the value, and that is true of EVERY path
// the harness makes when the harness itself runs under a session-rooted
// TMPDIR — which is where every delegated agent's worktree lives. The fake
// HOME, the project, and a correct in-tree instance all carried the fragment,
// so the check was false-positive for agents and invisible from a short
// checkout.
func isoIsPerAgentScratch(w *World, val string) bool {
	sep := string(os.PathSeparator)
	root := filepath.Join(w.env.HomeDir, ".ctxloom", "sessions")
	if val != root && !strings.HasPrefix(val, root+sep) {
		return false
	}
	// Since slice 14a the session's config-home INSTANCE also lives under the
	// sessions root (~/.ctxloom/sessions/<harp>/home/<leaf>, paths.HarpSessionEngineHomes),
	// so "under sessions/" no longer means scratch. The per-agent worktree
	// scratch is the harp's EPHEMERAL member (paths.HarpEphemeralDir,
	// Worktree.scratchBase); the instance is its HOME member. Discriminate on
	// the member RELATIVE TO the sessions root — the absolute path may pass
	// through an unrelated ".../ephemeral/..." (the outer sandbox worktree).
	segs := strings.Split(strings.TrimPrefix(val, root+sep), sep)
	return len(segs) >= 2 && segs[1] == "ephemeral"
}

// isoMatrixState is this file's per-scenario fixture state: where the spy's
// output landed for the last run.
type isoMatrixState struct {
	spyOut string
	// engine is the engine token the last runIsoMatrix drove, so an
	// outcome step can hold the run to the payload THAT engine's launch
	// path is capable of leaving behind.
	engine string
	// workspace is the isolation workspace axis the last run requested.
	workspace string
	// engineHome is the "iso" agent binding's engine_home value the next
	// runIsoMatrix call renders into config.yaml — "" (the zero value) means
	// UNDECLARED, matching a scenario that never calls the
	// "Alice's agent declares engine_home" Given step at all. Set by that
	// step, consumed and left untouched by runIsoMatrix (which does not reset
	// it, so it must not leak state that outlives this file's per-scenario
	// World anyway).
	engineHome string
	// engineHomeViaCLI switches WHO writes the engineHome value above. False
	// (the default) renders it as a `engine_home:` line straight into the
	// fixture's own config.yaml. True leaves that line OUT and makes
	// `ctxloom agent edit iso --engine-home <value>` the only writer, so the
	// binding under test can only have been written by the CLI flag — which
	// is what turns this fixture from a test of the config KEY into a test of
	// the FLAG that sets it.
	engineHomeViaCLI bool
}

func isoMatrixOf(w *World) *isoMatrixState {
	if w.isoMatrix == nil {
		w.isoMatrix = &isoMatrixState{}
	}
	return w.isoMatrix
}

// isoSanitizedPATH joins dirs into a PATH value built FROM SCRATCH — the one
// mechanic every PATH-masking fixture in J002200 shares. A sanitized PATH is
// only sanitized because the inherited value contributes NOTHING to it: the
// callers below (a spy dir that must shadow every real engine binary, and a
// bin dir that must contain no container runtime at all) both depend on that,
// and prepending would defeat either one.
func isoSanitizedPATH(dirs ...string) string {
	return strings.Join(dirs, string(os.PathListSeparator))
}

// isoMatrixSanitizedPATH returns "<spyDir>:/usr/bin:/bin" — rebuilt from
// scratch, never merely prepended to the inherited PATH. See the package
// doc for why this exact shape is the load-bearing safety property of every
// scenario in this file.
func isoMatrixSanitizedPATH(spyDir string) string {
	return isoSanitizedPATH(spyDir, "/usr/bin", "/bin")
}

// installIsoSpy writes the spy script once and symlinks it under each of
// names, so every engine's exec resolves to the SAME recording behavior.
func installIsoSpy(dir string, names ...string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create spy dir: %w", err)
	}
	scriptPath := filepath.Join(dir, ".ctxloom-iso-spy.sh")
	if err := os.WriteFile(scriptPath, []byte(isoMatrixSpyScript), 0o755); err != nil {
		return fmt.Errorf("write spy script: %w", err)
	}
	for _, name := range names {
		target := filepath.Join(dir, name)
		_ = os.Remove(target)
		if err := os.Symlink(scriptPath, target); err != nil {
			return fmt.Errorf("symlink spy as %q: %w", name, err)
		}
	}
	return nil
}

// isoMatrixConfigYAML renders the project config.yaml for one engine, binding
// a single agent "iso" to it. engineHome renders as the binding's own
// `engine_home:` key when non-empty — "" leaves it OUT of the YAML entirely
// (an undeclared binding), never writes an empty string value, so a scenario
// that never calls "Alice's agent declares engine_home" gets the true
// undeclared case, not a declared-empty one.
//
// The spy's own output path (CTXLOOM_ISOSPY_OUT) is NOT in here: it reaches
// the spy the way any variable reaches an engine — exported in the
// environment ctxloom runs in, which the launched process inherits. The
// config carries no environment for an engine at all (the retired `env` key
// is refused at load, config.RetiredLLMEnvKey), and every scenario that
// reaches the spy runs it on the host, where the ambient environment is the
// engine's environment.
func isoMatrixConfigYAML(engineType, engineHome string) string {
	engineHomeLine := ""
	if engineHome != "" {
		engineHomeLine = fmt.Sprintf("    engine_home: %s\n", engineHome)
	}
	return fmt.Sprintf(fmt.Sprintf("version: %d\n", config.CurrentConfigVersion)+`llm:
  configs:
    iso:
      type: %s
      permissions: bypass
  defaults:
    primary: iso
    fast: iso
agents:
  iso:
    llm: iso
    profiles: []
%s`, engineType, engineHomeLine)
}

// writeIsoEngineHomeViaCLI makes the CLI FLAG the only writer of the "iso"
// binding's engine_home, by running the real `ctxloom agent edit iso
// --engine-home <value>` against the project the fixture just rendered.
//
// It runs HERE — inside runIsoMatrix, between the config write and the git
// commit — rather than from its own Given step, because runIsoMatrix rewrites
// .ctxloom/config.yaml wholesale: anything the CLI wrote beforehand would be
// erased by the fixture before the engine ever launched, and the scenario
// would then be measuring the fixture again instead of the flag.
//
// A failed edit is returned as the step's own error rather than left for the
// downstream assertion, so "the flag was rejected" cannot be reported as
// "the engine was not relocated".
func writeIsoEngineHomeViaCLI(w *World, value string) error {
	if err := w.env.Run("agent", "edit", "iso", "--engine-home", value); err != nil {
		return fmt.Errorf("ctxloom agent edit iso --engine-home %s: %w; output:\n%s", value, err, w.env.LastOutput())
	}
	return nil
}

// runIsoMatrix is every scenario's core action: install the spy, sanitize
// PATH (unconditionally — even a scenario expected to abort before any spawn
// gets the same safety net), write config.yaml for engine, and run `ctxloom
// run --agent iso --workspace <workspace> --one-shot`. The run's own
// success/failure is asserted by later Then steps, not here.
// isoPinHumansLogin empties Alice's own CLAUDE_CONFIG_DIR and credential
// storage var: the acceptance binary inherits the developer's shell, and a
// developer running inside a ctxloom session carries both, which would
// otherwise decide what a host run is told Alice's login is. Empty, Alice's
// claude resolves its login from her $HOME/.claude.
func isoPinHumansLogin(w *World) {
	w.env.SetEnv("CLAUDE_CONFIG_DIR", "")
	w.env.SetEnv(isoSecureStorageEnv, "")
}

// isoSecureStorageEnv moves only claude's credential storage.
const isoSecureStorageEnv = "CLAUDE_SECURESTORAGE_CONFIG_DIR"

func runIsoMatrix(c context.Context, engine, workspace string) error {
	w := worldFrom(c)
	j := isoMatrixOf(w)

	j.engine = engine
	j.workspace = workspace
	isoPinHumansLogin(w)

	binNames, err := isoBinaryNames(engine)
	if err != nil {
		return err
	}
	spyDir := filepath.Join(w.env.Root, "iso-spy-bin")
	if err := installIsoSpy(spyDir, binNames...); err != nil {
		return err
	}
	w.env.SetEnv("PATH", isoMatrixSanitizedPATH(spyDir))

	spyOut := filepath.Join(w.env.Root, "iso-spy-out.txt")
	_ = os.Remove(spyOut)
	j.spyOut = spyOut
	w.env.SetEnv("CTXLOOM_ISOSPY_OUT", spyOut)

	// The fixture writes the declaration itself UNLESS the scenario asked for
	// the CLI to be the writer, in which case the rendered YAML deliberately
	// carries no engine_home at all — see writeIsoEngineHomeViaCLI.
	renderedEngineHome := j.engineHome
	if j.engineHomeViaCLI {
		renderedEngineHome = ""
	}
	if err := w.env.WriteFile(".ctxloom/config.yaml", isoMatrixConfigYAML(engine, renderedEngineHome)); err != nil {
		return err
	}
	if j.engineHomeViaCLI {
		if err := writeIsoEngineHomeViaCLI(w, j.engineHome); err != nil {
			return err
		}
	}
	if err := w.env.GitCommit("iso matrix config for " + engine); err != nil {
		return err
	}

	_ = w.env.Run("run", "--agent", "iso", "--workspace", workspace, "--one-shot", "hello")
	return nil
}

// runIsoMatrixOwnerSession is runIsoMatrix's counterpart for ALICE'S OWN
// session: the identical fixture, the identical engine, the identical none
// axis — the ONE difference is that no agent is named on the command line.
//
// The project declares `default_agent: iso`, so this is not "a run with no
// agent resolved at all": ctxloom still binds the default agent, exactly as it
// does for any bare `ctxloom run`. The "iso" binding's engine_home is
// deliberately left UNDECLARED here (never set via the "Alice's agent
// declares engine_home" step), which is the load-bearing fact this scenario
// proves: engine_home wins on EVERY invocation path a binding resolves
// through, including a bare launch under default_agent — an undeclared
// binding resolves to the host default (agents.ParseHomeMode)
// regardless of whether it was reached via `--agent iso` or a bare `ctxloom
// run`, so Alice's own session keeps her real ~/.claude here for the SAME
// reason the sibling "undeclared binding" scenario keeps it for an explicit
// --agent run — not because "she named no agent" is itself the rule.
//
// Without the default_agent declaration a bare run would abort at the startup
// gate (an unresolvable default agent is a ClassRef finding), which would prove
// nothing about config homes.
func runIsoMatrixOwnerSession(c context.Context, engine string) error {
	w := worldFrom(c)
	j := isoMatrixOf(w)

	j.engine = engine
	j.workspace = "none"
	isoPinHumansLogin(w)

	binNames, err := isoBinaryNames(engine)
	if err != nil {
		return err
	}
	spyDir := filepath.Join(w.env.Root, "iso-spy-bin")
	if err := installIsoSpy(spyDir, binNames...); err != nil {
		return err
	}
	w.env.SetEnv("PATH", isoMatrixSanitizedPATH(spyDir))

	spyOut := filepath.Join(w.env.Root, "iso-spy-out.txt")
	_ = os.Remove(spyOut)
	j.spyOut = spyOut
	w.env.SetEnv("CTXLOOM_ISOSPY_OUT", spyOut)

	// Deliberately "" (undeclared), NOT j.engineHome: this scenario's whole
	// point is the undeclared case, and reading scenario-shared state here
	// would let an earlier "Alice's agent declares engine_home" step in some
	// other ordering silently change what is under test.
	if err := w.env.WriteFile(".ctxloom/config.yaml", isoMatrixConfigYAML(engine, "")+"default_agent: iso\n"); err != nil {
		return err
	}
	if err := w.env.GitCommit("iso matrix config for " + engine); err != nil {
		return err
	}

	_ = w.env.Run("run", "--workspace", "none", "--one-shot", "hello")
	return nil
}

// isoReadSpyOut reads the spy's recorded output, erroring with a clear
// message (not a bare os.ReadFile error) when the spy was never invoked —
// itself diagnostic evidence for a scenario asserting an isolated success
// path (a run that aborted before spawning, or a backend like opencode whose
// launch never reaches a plain exec, leaves no file).
func isoReadSpyOut(j *isoMatrixState) (string, error) {
	if j.spyOut == "" {
		return "", fmt.Errorf("iso matrix: no run recorded yet")
	}
	data, err := os.ReadFile(j.spyOut)
	if err != nil {
		return "", fmt.Errorf("spy process was never invoked (or wrote no output) — %w", err)
	}
	return string(data), nil
}

// isoParseSpyEnv extracts the ===ENV=== block (the allowlisted slice of the
// spy's own environment — isoSpyEnvAllowlist) into a lookup map.
func isoParseSpyEnv(body string) map[string]string {
	env := map[string]string{}
	inEnv := false
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "===") {
			inEnv = line == "===ENV==="
			continue
		}
		if !inEnv {
			continue
		}
		if i := strings.IndexByte(line, '='); i > 0 {
			env[line[:i]] = line[i+1:]
		}
	}
	return env
}

// isoParseSpySection extracts the content between a "===MARKER===" line and
// the next "===" line (or EOF).
func isoParseSpySection(body, marker string) string {
	idx := strings.Index(body, marker)
	if idx < 0 {
		return ""
	}
	rest := body[idx+len(marker):]
	if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
		rest = rest[nl+1:]
	}
	// Prefix a newline so an EMPTY section — the next marker on the very
	// first line — ends the section too, instead of being read as content.
	rest = "\n" + rest
	if next := strings.Index(rest, "\n==="); next >= 0 {
		rest = rest[:next]
	}
	return strings.TrimSpace(rest)
}

func registerJ002200MatrixSteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^Alice has a git-backed project$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := w.env.InitGitRepo(); err != nil {
			return err
		}
		if err := w.env.WriteFile("README.md", "iso matrix fixture project\n"); err != nil {
			return err
		}
		return w.env.GitCommit("initial commit")
	})

	// The confidentiality fixture for D4. On a real host ~/.claude.json is not
	// a narrow onboarding record — it is claude's WHOLE top-level config,
	// carrying the user's own mcpServers registrations and the secrets in
	// their env blocks, their oauthAccount identity, and their accumulated
	// per-project history. A scenario that asserts "none of Alice's own config
	// crossed" against a fixture that HAS none of it proves nothing, so this
	// writes all four classes, with obviously-fake values.
	ctx.Step(`^Alice has a personal claude config carrying her own MCP servers$`, func(c context.Context) error {
		w := worldFrom(c)
		return w.env.WriteHomeFile(".claude.json", `{
  "hasCompletedOnboarding": true,
  "lastOnboardingVersion": "9.9.9-fixture",
  "bypassPermissionsModeAccepted": true,
  "oauthAccount": {"emailAddress": "`+isoFixturePersonalEmail+`"},
  "mcpServers": {"spotify": {"command": "spotify-mcp", "args": ["--token", "`+isoFixturePersonalSecret+`"]}},
  "projects": {"/home/alice/some-other-repo": {"history": ["`+isoFixturePersonalHistory+`"]}}
}
`)
	})

	ctx.Step(`^Alice has a "([^"]*)" credential fixture on the host$`, func(c context.Context, engine string) error {
		w := worldFrom(c)
		rel, err := isoCredHostPath(engine)
		if err != nil {
			return err
		}
		body, err := isoCredFixtureContent(engine)
		if err != nil {
			return err
		}
		return w.env.WriteHomeFile(rel, body+"\n")
	})

	// The per-engine form of "Alice can authenticate": she has stored a
	// setup-token the way the docs tell her to (`ctxloom auth set-token`
	// writes this file). Her own shell's token var is emptied so the stored
	// one is what the run gets: an exported token wins over the store.
	ctx.Step(`^Alice has whatever credentials "([^"]*)" needs to authenticate$`, func(c context.Context, engine string) error {
		w := worldFrom(c)
		return isoStoreSetupToken(w, engine)
	})

	ctx.Step(`^Alice has stored a "([^"]*)" setup-token$`, func(c context.Context, engine string) error {
		w := worldFrom(c)
		return isoStoreSetupToken(w, engine)
	})

	ctx.Step(`^Alice has no "([^"]*)" credentials or API key on the host$`, func(c context.Context, engine string) error {
		w := worldFrom(c)
		keys, err := isoAuthEnvVars(engine)
		if err != nil {
			return err
		}
		// Explicit empty-set, not a bare assumption of absence: the acceptance
		// binary inherits the developer's own shell env (isolatedEnv only
		// replaces HOME/XDG_*), so a locally-exported ANTHROPIC_API_KEY (etc.)
		// would otherwise silently flip this scenario's premise.
		for _, key := range keys {
			w.env.SetEnv(key, "")
		}
		return nil
	})

	ctx.Step(`^Alice has no "([^"]*)" credentials on the host$`, func(c context.Context, engine string) error {
		w := worldFrom(c)
		// This used to be an unconditional no-op on the ASSUMPTION
		// that "the fresh TestEnvironment HOME never has one" -- true today,
		// but an assumption a scenario ordering change or a future fixture
		// helper writing into HOME earlier could silently invalidate. The
		// isoCredHostPath helper this file already has for the opposite
		// fixture step (:339-344, "Alice HAS a credential fixture") makes the
		// positive check cheap for the engines it knows (claude-code, codex).
		// Some engines (opencode) have no file-based host-credential concept
		// at all -- isoCredHostPath errors for those, which is not this
		// step's failure to report; there is genuinely nothing to check, so
		// it stays the documented no-op for exactly those engines.
		rel, err := isoCredHostPath(engine)
		if err != nil {
			return nil
		}
		if w.env.HomeFileExists(rel) {
			return fmt.Errorf("expected no %s credential fixture at ~/%s, but one exists", engine, rel)
		}
		return nil
	})

	ctx.Step(`^Alice has set the "([^"]*)" API key in the environment$`, func(c context.Context, engine string) error {
		w := worldFrom(c)
		key, err := isoAPIKeyEnvVar(engine)
		if err != nil {
			return err
		}
		w.env.SetEnv(key, "sk-fixture-not-a-real-key")
		return nil
	})

	// THE engine_home FIXTURE KNOB. Sets the "iso" agent binding's declared
	// engine_home for the NEXT runIsoMatrix call — never for
	// runIsoMatrixOwnerSession, which deliberately hardcodes "" (undeclared)
	// regardless of this state, since its whole point is the undeclared case.
	// A scenario that never calls this step gets an undeclared binding, which
	// is itself a fixture under test (see the "undeclared engine_home"
	// scenario).
	ctx.Step(`^Alice's agent declares engine_home "([^"]*)"$`, func(c context.Context, value string) error {
		w := worldFrom(c)
		j := isoMatrixOf(w)
		j.engineHome = value
		return nil
	})

	// THE SAME KNOB, TURNED BY THE CLI. Identical outcome expectations, one
	// difference in how the binding got its value: `ctxloom agent edit iso
	// --engine-home <value>` writes it, and the fixture's own YAML renders no
	// engine_home line at all. The step only records the intent — the edit
	// itself runs inside runIsoMatrix, because the fixture rewrites config.yaml
	// and would otherwise overwrite whatever the CLI had already written (see
	// writeIsoEngineHomeViaCLI).
	ctx.Step(`^Alice declares engine_home "([^"]*)" on her agent with the ctxloom CLI$`, func(c context.Context, value string) error {
		w := worldFrom(c)
		j := isoMatrixOf(w)
		j.engineHome = value
		j.engineHomeViaCLI = true
		return nil
	})

	ctx.Step(`^Alice has a "([^"]*)" and "([^"]*)" on the host$`, func(c context.Context, gitconfig, ssh string) error {
		w := worldFrom(c)
		if err := w.env.WriteHomeFile(gitconfig, "[user]\n\tname = Alice Fixture\n\temail = alice@example.com\n"); err != nil {
			return err
		}
		return os.MkdirAll(filepath.Join(w.env.HomeDir, ssh), 0o700)
	})

	ctx.Step(`^Alice runs the isolated "([^"]*)" agent under workspace "([^"]*)"$`, func(c context.Context, engine, workspace string) error {
		return runIsoMatrix(c, engine, workspace)
	})

	ctx.Step(`^Alice runs "([^"]*)" under workspace "none" as her own session, naming no agent$`, func(c context.Context, engine string) error {
		return runIsoMatrixOwnerSession(c, engine)
	})

	// The AGENT half of the in-tree config-home rule, read from INSIDE the
	// spawned engine process: the variable the engine was really handed names
	// THIS SESSION's config-home instance — not the human's own directory, not
	// a per-agent scratch tree (the WORKTREE axis' answer, asserted against so
	// this step cannot pass on the wrong axis), and not a project-wide path
	// (the durable per-project home the per-session model retired).
	//
	// The expectation is built component by component here rather than derived
	// from the production resolution (paths.HarpSessionEngineHomes plus
	// claude.HomeLeaf): an assertion that computes its expectation with the
	// same function the production code used cannot fail when that function
	// is wrong.
	//
	// EVERYTHING IS READ FROM THE RECORDING, nothing from the post-run
	// filesystem. This step used to os.Stat the value and glob the project for
	// exactly one instance; both went red the day S8's teardown started reaping
	// the instance at session end — correct, ruled behaviour that those two
	// assertions read as "the engine was pointed at a home nobody created". The
	// existence claim they were making is still made, and made BETTER: the spy
	// prints a DIR line for its own config home from inside the running
	// process, which is when the directory's existence actually matters. The
	// sibling "is gone after the run" step pins the reaping half.
	// The engine's WORKING DIRECTORY under workspace "none" is the live
	// project dir whatever its config home is: relocating the home moves
	// where claude keeps its state, never where it works.
	ctx.Step(`^the spy "([^"]*)" process ran in the live project dir$`, func(c context.Context, engine string) error {
		w := worldFrom(c)
		j := isoMatrixOf(w)
		body, err := isoReadSpyOut(j)
		if err != nil {
			return fmt.Errorf("engine %q: %w", engine, err)
		}
		if pwd := isoParseSpyEnv(body)["PWD"]; pwd != w.env.ProjectDir {
			return fmt.Errorf("engine %q ran in %q, want the live project dir %q; spy dump:\n%s", engine, pwd, w.env.ProjectDir, body)
		}
		w.docStepMaterialized = fmt.Sprintf("engine %s ran in the live project dir %s", engine, w.env.ProjectDir)
		return nil
	})

	ctx.Step(`^the spy "([^"]*)" process's "([^"]*)" env var points at this session's config-home instance$`, func(c context.Context, engine, varName string) error {
		w := worldFrom(c)
		j := isoMatrixOf(w)
		body, err := isoReadSpyOut(j)
		if err != nil {
			return fmt.Errorf("engine %q: %w", engine, err)
		}
		env := isoParseSpyEnv(body)
		val, ok := env[varName]
		if !ok || val == "" {
			return fmt.Errorf("spy %s process's env carries no %s at all — an in-tree AGENT run that declared engine_home: session must be handed a ctxloom-controlled config home; full env dump:\n%s", engine, varName, body)
		}
		harp, err := isoInstanceHomeShape(w.env.HomeDir, engine, val)
		if err != nil {
			return fmt.Errorf("%s: %w; full env dump:\n%s", varName, err, body)
		}
		if isoIsPerAgentScratch(w, val) {
			return fmt.Errorf("%s=%q is a per-agent SCRATCH home (the worktree axis' answer), not the in-tree instance", varName, val)
		}
		if strings.Contains(val, filepath.Join("state", "engines")) {
			return fmt.Errorf("%s=%q is the RETIRED durable per-project engine home", varName, val)
		}
		listing := isoParseSpySection(body, "===CONFIG_HOME_LISTING===")
		if !strings.Contains(listing, "DIR "+val+"\n") && !strings.HasSuffix(listing, "DIR "+val) {
			return fmt.Errorf("%s=%q but the spy did not see that directory while it was running — the engine was pointed at a home nobody created; listing:\n%s", varName, val, listing)
		}
		w.docStepMaterialized = fmt.Sprintf("spy %s process env: %s=%s (session %s), directory present during the run", engine, varName, val, harp)
		return nil
	})

	// The taking this whole rule exists to avoid, asserted from the other side:
	// Alice's own engine home is not merely left unread, it is not brought into
	// existence. A run that created ~/.claude and then wrote its
	// session state there would have relocated nothing at all.
	ctx.Step(`^Alice's own "([^"]*)" home directory was never created by the run$`, func(c context.Context, engine string) error {
		w := worldFrom(c)
		rel, err := isoHostHomeDirRel(engine)
		if err != nil {
			return err
		}
		path := filepath.Join(w.env.HomeDir, rel)
		info, statErr := os.Stat(path)
		if statErr != nil {
			w.docStepMaterialized = fmt.Sprintf("Alice's own ~/%s: absent, as it was before the run", rel)
			return nil
		}
		// claude-code's scenarios seed ~/.claude/.credentials.json as their
		// premise, so the DIRECTORY legitimately exists there. What must not
		// have happened is the run adding anything to it.
		entries, readErr := os.ReadDir(path)
		if readErr != nil {
			return fmt.Errorf("cannot inspect Alice's own ~/%s: %w", rel, readErr)
		}
		credRel, credErr := isoCredHostPath(engine)
		expected := 0
		if credErr == nil && w.env.HomeFileExists(credRel) {
			expected = 1 // the fixture credential this scenario seeded, and nothing else
		}
		if len(entries) != expected {
			names := make([]string, 0, len(entries))
			for _, e := range entries {
				names = append(names, e.Name())
			}
			return fmt.Errorf("the run wrote into Alice's own ~/%s: expected %d entr(ies), found %d %v", rel, expected, len(entries), names)
		}
		w.docStepMaterialized = fmt.Sprintf("Alice's own ~/%s: %d entr(ies), unchanged by the run (info: %s)", rel, len(entries), info.Mode())
		return nil
	})

	// The instance's disposal, pinned at the acceptance layer. An instance
	// holds a COPY of the user's live credential, and it is an Ephemeral
	// member of the session that the one reaper takes by age — so this step
	// first proves the instance is STILL THERE after the run (nothing but the
	// reaper removes it), then runs the reaper with a bound in the future so
	// the just-ended session counts as aged, and proves it went. The session
	// ended under its liveness lock, which is the only state the reaper
	// accepts as proof nobody is running it.
	ctx.Step(`^the "([^"]*)" config-home instance is reaped once the session has aged out$`, func(c context.Context, engine string) error {
		w := worldFrom(c)
		homes, err := isoInstanceHomes(w.env.HomeDir, engine)
		if err != nil {
			return err
		}
		if len(homes) == 0 {
			return fmt.Errorf("no %q config-home instance survived the run — the instance is the reaper's to take by age, and this scenario proves the reaper reaches it, which it cannot when something else removed it first", engine)
		}
		// A date bound parses at midnight UTC, so "tomorrow" in local time can
		// still fall BEFORE the session's activity late in the day; two days
		// out is past any clock this can run under.
		tomorrow := time.Now().AddDate(0, 0, 2).Format(time.DateOnly)
		if err := runCLI(c, "ctxloom clean --older-than "+tomorrow+" --yes", ""); err != nil {
			return err
		}
		if code := w.env.LastExitCode(); code != 0 {
			return fmt.Errorf("`ctxloom clean --older-than %s --yes` exited %d:\n%s", tomorrow, code, w.env.LastOutput())
		}
		homes, err = isoInstanceHomes(w.env.HomeDir, engine)
		if err != nil {
			return err
		}
		if len(homes) != 0 {
			return fmt.Errorf("the %q config-home instance(s) %v survived the reap — an un-reaped instance leaves copied credential bytes on disk:\n%s", engine, homes, w.env.LastOutput())
		}
		w.docStepMaterialized = fmt.Sprintf("the %q instance under %s survived the session's end and was taken by `ctxloom clean --older-than %s --yes` (it is rebuilt fresh next session)", engine, filepath.Join(w.env.HomeDir, ".ctxloom", "sessions"), tomorrow)
		return nil
	})

	// The absence half of Alice's own session. It is only meaningful next to
	// the AGENT scenario in the same feature, which proves this exact path IS
	// created in this exact fixture when an agent is named — otherwise
	// "nothing exists here" would be satisfied by a fixture where nothing ever
	// exists anywhere.
	ctx.Step(`^no ctxloom-controlled config home exists for "([^"]*)" in the project$`, func(c context.Context, engine string) error {
		w := worldFrom(c)
		homes, err := isoInstanceHomes(w.env.HomeDir, engine)
		if err != nil {
			return err
		}
		if len(homes) != 0 {
			return fmt.Errorf("ctxloom-controlled config home(s) exist at %v — a run that did not declare engine_home: session must keep its real engine home, not be relocated into a session instance", homes)
		}
		// The retired durable per-project location too: an implementation that
		// regrew it would leave this glob empty and still be wrong.
		retired := filepath.Join(w.env.ProjectDir, ".ctxloom", "state", "engines")
		if _, statErr := os.Stat(retired); statErr == nil {
			return fmt.Errorf("the retired durable per-project engine home regrew at %s", retired)
		}
		w.docStepMaterialized = fmt.Sprintf("no config-home instance for %q under %s (the run keeps its real engine home)", engine, filepath.Join(w.env.HomeDir, ".ctxloom", "sessions"))
		return nil
	})

	// PAYLOAD, not absence. This step used to assert ONLY that two needles
	// were missing from the output — which an audit proved is satisfied by a
	// run that never happened at all: with LaunchBackend.ExecuteCLI mutated to
	// error before spawning, every row of this outline stayed GREEN. A
	// scenario whose subject is "the engine proceeds untouched" cannot be
	// allowed to pass when no engine proceeded. So the run must now SHOW its
	// work: exit 0, a spy process that actually ran, that spy's own env free
	// of any per-agent scratch config-home, and its cwd the live project dir.
	ctx.Step(`^the run touches no isolation mechanism at all$`, func(c context.Context) error {
		w := worldFrom(c)
		j := isoMatrixOf(w)
		out := w.env.LastOutput()
		w.docStepMaterialized = fmt.Sprintf("exit=%d\n%s", w.env.LastExitCode(), strings.TrimSpace(out))
		for _, needle := range []string{"worktree isolation for agent", "AUTHENTICATION IS NOT", "[isolation]"} {
			if strings.Contains(out, needle) {
				return fmt.Errorf("workspace \"none\" unexpectedly triggered isolation machinery (%q); output:\n%s", needle, out)
			}
		}
		if code := w.env.LastExitCode(); code != 0 {
			return fmt.Errorf("expected exit 0 (workspace \"none\" runs the engine on the host, untouched), got %d; output:\n%s", code, out)
		}
		body, err := isoReadSpyOut(j)
		if err != nil {
			return fmt.Errorf("engine %q under workspace \"none\": %w — a run that never reached the engine cannot prove the engine ran untouched; output:\n%s", j.engine, err, out)
		}
		env := isoParseSpyEnv(body)
		for _, key := range isoSpyEnvAllowlist {
			if key == "PWD" {
				continue
			}
			if val := env[key]; isoIsPerAgentScratch(w, val) {
				return fmt.Errorf("workspace \"none\" handed engine %q an ISOLATED %s=%q — the none axis must share the host's own config-home, not relocate it; spy dump:\n%s", j.engine, key, val, body)
			}
		}
		if pwd := env["PWD"]; pwd != w.env.ProjectDir {
			return fmt.Errorf("workspace \"none\" ran engine %q in %q, want the live project dir %q; spy dump:\n%s", j.engine, pwd, w.env.ProjectDir, body)
		}
		w.docStepMaterialized = fmt.Sprintf("exit=0; engine %s ran in %s with no per-agent config-home:\n%s", j.engine, env["PWD"], isoParseSpySection(body, "===ENV==="))
		return nil
	})

	// codex's workspace-"none" exception. Two things have to be true at once
	// and the pairing is the whole point: ctxloom's OWN isolation gates stayed
	// out of the way (no finding, and NOT the ClassIsolation exit 3 those
	// gates use), yet the run still failed — because codex relocates
	// CODEX_HOME by itself on every axis and there was nothing to seed the
	// relocated home with. Asserting the engine never launched (no spy
	// recording) is what keeps this from degenerating into "some error
	// happened".
	ctx.Step(`^the run fails without any isolation finding, naming "([^"]*)"$`, func(c context.Context, needle string) error {
		w := worldFrom(c)
		j := isoMatrixOf(w)
		out := w.env.LastOutput()
		w.docStepMaterialized = fmt.Sprintf("exit=%d\n%s", w.env.LastExitCode(), strings.TrimSpace(out))
		if strings.Contains(out, "worktree isolation for agent") || strings.Contains(out, "[isolation]") {
			return fmt.Errorf("unexpected isolation finding present; output:\n%s", out)
		}
		if code := w.env.LastExitCode(); code != 1 {
			return fmt.Errorf("expected exit 1 (a plain backend failure, not the ClassIsolation exit 3), got %d; output:\n%s", code, out)
		}
		if !strings.Contains(out, needle) {
			return fmt.Errorf("output does not contain %q; output:\n%s", needle, out)
		}
		if body, err := isoReadSpyOut(j); err == nil {
			return fmt.Errorf("engine %q launched anyway — the run must refuse BEFORE spawning an engine it could not authenticate; spy dump:\n%s", j.engine, body)
		}
		return nil
	})

	ctx.Step(`^the run aborts with an isolation finding naming "([^"]*)"$`, func(c context.Context, needle string) error {
		w := worldFrom(c)
		out := w.env.LastOutput()
		w.docStepMaterialized = fmt.Sprintf("exit=%d\n%s", w.env.LastExitCode(), strings.TrimSpace(out))
		if code := w.env.LastExitCode(); code != 3 {
			return fmt.Errorf("expected exit 3 (fatal isolation finding), got %d; output:\n%s", code, out)
		}
		if !strings.Contains(out, needle) {
			return fmt.Errorf("output does not contain %q; output:\n%s", needle, out)
		}
		if !strings.Contains(out, "--degraded") {
			return fmt.Errorf("output does not carry the --degraded escape hatch; output:\n%s", out)
		}
		return nil
	})

	// PAYLOAD, not absence — same lesson as "touches no isolation mechanism at
	// all" above, and the same audit. Asserting only that no finding was
	// PRINTED made "the same engines proceed" pass in a world where nothing
	// proceeded: under a mutation that made LaunchBackend.ExecuteCLI error
	// before spawning, and under one that made Worktree.PrepareWorkspace always
	// error (silently degrading the whole chain to None — the live project dir
	// plus the host's global engine config, the exact loss of boundary this
	// journey exists to prove), every row stayed green, because the degrade
	// warning's wording matches neither needle. This step now
	// carries the run-actually-happened half itself, so every user of it
	// benefits, and the per-engine config-home var is asserted alongside it in
	// the feature file.
	ctx.Step(`^the run reports no isolation finding$`, func(c context.Context) error {
		w := worldFrom(c)
		j := isoMatrixOf(w)
		out := w.env.LastOutput()
		w.docStepMaterialized = fmt.Sprintf("exit=%d\n%s", w.env.LastExitCode(), strings.TrimSpace(out))
		if strings.Contains(out, "worktree isolation for agent") || strings.Contains(out, "[isolation]") {
			return fmt.Errorf("unexpected isolation finding present; output:\n%s", out)
		}
		if code := w.env.LastExitCode(); code != 0 {
			return fmt.Errorf("expected exit 0 (the run proceeds), got %d; output:\n%s", code, out)
		}
		if _, err := isoReadSpyOut(j); err != nil {
			return fmt.Errorf("engine %q: %w — \"proceeds without any isolation finding\" is not satisfied by a run that never reached the engine; output:\n%s", j.engine, err, out)
		}
		return nil
	})

	// opencode's own row of the SAME claim, split out because its launch path
	// cannot leave the payload the step above demands. opencode is driven over
	// ACP — a stateful JSON-RPC handshake over stdio (internal/opencode) — and
	// this file's spy is a dumb recorder that dumps and exits, so the handshake
	// never completes and the run errors AFTER the isolation gates have all
	// passed. That is a fixture limitation, not product behavior, and it means
	// no exit code and no spy file are available to assert on. What IS
	// assertable, and is the whole point of the row, is that the run got PAST
	// every isolation gate and all the way to spawning opencode from the
	// PATH-sandboxed spy dir — the failure names that very path. See the
	// file-level note on opencode for why its spawned-env payload is pinned at
	// the Go level instead (auth_test.go's
	// TestHostCredentialSeed_OpencodeSeedsAuthJsonUnderXdgDataOpencode).
	ctx.Step(`^the run proceeds past every isolation gate to spawn the engine itself$`, func(c context.Context) error {
		w := worldFrom(c)
		j := isoMatrixOf(w)
		out := w.env.LastOutput()
		w.docStepMaterialized = fmt.Sprintf("exit=%d\n%s", w.env.LastExitCode(), strings.TrimSpace(out))
		if strings.Contains(out, "worktree isolation for agent") || strings.Contains(out, "[isolation]") {
			return fmt.Errorf("unexpected isolation finding present; output:\n%s", out)
		}
		binNames, err := isoBinaryNames(j.engine)
		if err != nil {
			return err
		}
		spawned := filepath.Join(w.env.Root, "iso-spy-bin", binNames[0])
		if !strings.Contains(out, spawned) {
			return fmt.Errorf("nothing in the run's output names the sandboxed spy binary %q, so there is no evidence the run reached an engine spawn at all; output:\n%s", spawned, out)
		}
		return nil
	})

	ctx.Step(`^the run reports a non-fatal isolation warning naming "([^"]*)"$`, func(c context.Context, needle string) error {
		w := worldFrom(c)
		out := w.env.LastOutput()
		w.docStepMaterialized = fmt.Sprintf("exit=%d\n%s", w.env.LastExitCode(), strings.TrimSpace(out))
		if code := w.env.LastExitCode(); code != 0 {
			return fmt.Errorf("expected exit 0 (non-fatal warning, no --degraded needed), got %d; output:\n%s", code, out)
		}
		if !strings.Contains(out, needle) {
			return fmt.Errorf("output does not contain %q; output:\n%s", needle, out)
		}
		// Match the stable "aborting " PREFIX, not one phase word: this is a
		// NEGATIVE assertion, so pinning it to a single phase would silently
		// stop catching aborts from every phase added later.
		if strings.Contains(out, "ctxloom: aborting ") {
			return fmt.Errorf("run unexpectedly aborted (should be a non-fatal warning, not a ClassIsolation fatal); output:\n%s", out)
		}
		return nil
	})

	ctx.Step(`^the spy "([^"]*)" process's ARGV contains "([^"]*)"$`, func(c context.Context, engine, want string) error {
		w := worldFrom(c)
		j := isoMatrixOf(w)
		body, err := isoReadSpyOut(j)
		if err != nil {
			return fmt.Errorf("engine %q: %w", engine, err)
		}
		argv := isoParseSpySection(body, "===ARGV===")
		if !strings.Contains(argv, want) {
			return fmt.Errorf("spy %s process's argv does not contain %q; argv:\n%s", engine, want, argv)
		}
		w.docStepMaterialized = fmt.Sprintf("spy %s process argv:\n%s", engine, argv)
		return nil
	})

	ctx.Step(`^the spy "([^"]*)" process's STDIN contains "([^"]*)"$`, func(c context.Context, engine, want string) error {
		w := worldFrom(c)
		j := isoMatrixOf(w)
		body, err := isoReadSpyOut(j)
		if err != nil {
			return fmt.Errorf("engine %q: %w", engine, err)
		}
		stdin := isoParseSpySection(body, "===STDIN===")
		if !strings.Contains(stdin, want) {
			return fmt.Errorf("spy %s process's stdin does not contain %q; stdin:\n%s", engine, want, stdin)
		}
		w.docStepMaterialized = fmt.Sprintf("spy %s process stdin:\n%s", engine, stdin)
		return nil
	})

	// The VERBATIM-copy assertion, for an engine whose whole credential file is
	// safe to copy (codex's auth.json — no rotation, no projector). claude-code
	// is NOT verbatim: it uses the access-token-only step below.
	ctx.Step(`^the isolated "([^"]*)" credential matches the host fixture byte-for-byte$`, func(c context.Context, engine string) error {
		w := worldFrom(c)
		j := isoMatrixOf(w)
		body, err := isoReadSpyOut(j)
		if err != nil {
			return fmt.Errorf("engine %q: %w", engine, err)
		}
		marker, err := isoCredsSectionMarker(engine)
		if err != nil {
			return err
		}
		got := isoParseSpySection(body, marker)
		wantBody, err := isoCredFixtureContent(engine)
		if err != nil {
			return err
		}
		want := strings.TrimSpace(wantBody)
		if got != want {
			return fmt.Errorf("isolated %s credential content = %q, want the host fixture %q; full spy dump:\n%s", engine, got, want, body)
		}
		w.docStepMaterialized = fmt.Sprintf("isolated %s credential (read from inside the spy process, via %s):\n%s", engine, marker, got)
		return nil
	})

	// Nothing is copied into a session home: the engine authenticates from
	// its env. Read from inside the running engine, where the home exists.
	ctx.Step(`^the isolated "([^"]*)" home holds no credential file$`, func(c context.Context, engine string) error {
		w := worldFrom(c)
		j := isoMatrixOf(w)
		body, err := isoReadSpyOut(j)
		if err != nil {
			return fmt.Errorf("engine %q: %w", engine, err)
		}
		marker, err := isoCredsSectionMarker(engine)
		if err != nil {
			return err
		}
		if got := isoParseSpySection(body, marker); got != "" {
			return fmt.Errorf("a credential file was copied into the isolated %s home; the run must authenticate from its env. spy read:\n%s", engine, got)
		}
		for _, l := range strings.Split(isoParseSpySection(body, "===CONFIG_HOME_LISTING==="), "\n") {
			if strings.HasPrefix(l, "MODE ") {
				return fmt.Errorf("a credential file exists in the isolated %s home: %s", engine, l)
			}
		}
		w.docStepMaterialized = fmt.Sprintf("isolated %s home, read from inside the spy process: no credential file", engine)
		return nil
	})

	// A HOST run shares Alice's own login in place: the storage var SET to
	// what her claude resolves ("" under isoPinHumansLogin: her
	// $HOME/.claude), read from inside the spy.
	ctx.Step(`^the spy "([^"]*)" process shares Alice's own login in place$`, func(c context.Context, engine string) error {
		w := worldFrom(c)
		body, err := isoReadSpyOut(isoMatrixOf(w))
		if err != nil {
			return fmt.Errorf("engine %q: %w", engine, err)
		}
		if got := isoParseSpySection(body, "===SHARED_LOGIN==="); got != "set:" {
			return fmt.Errorf("the %s process does not share Alice's login: %s is %q, want set to \"\"", engine, isoSecureStorageEnv, got)
		}
		w.docStepMaterialized = fmt.Sprintf("the %s process's %s is set to \"\": Alice's own $HOME/.claude login", engine, isoSecureStorageEnv)
		return nil
	})

	// The setup-token is blanked on the host even when one is stored: claude
	// reads it ahead of any credential, so it would shadow the shared login.
	ctx.Step(`^the spy "([^"]*)" process was handed no setup-token$`, func(c context.Context, engine string) error {
		w := worldFrom(c)
		body, err := isoReadSpyOut(isoMatrixOf(w))
		if err != nil {
			return fmt.Errorf("engine %q: %w", engine, err)
		}
		if got := isoParseSpySection(body, "===SETUP_TOKEN==="); got != "unset" {
			return fmt.Errorf("the %s process was handed a setup-token on the host (spy saw %q)", engine, got)
		}
		w.docStepMaterialized = fmt.Sprintf("the %s process's token var is empty (read from inside the spy)", engine)
		return nil
	})

	// D4's end-to-end payload, read out of the file the ENGINE actually had in
	// front of it. Three claims at once, and all three matter:
	//
	//   - the onboarding answer CROSSED (otherwise every agent session
	//     re-onboards, which is the cost the field-scoped copy exists to avoid);
	//   - the workspace-trust answer was GENERATED for the directory the run
	//     works in (otherwise a headless run proceeds untrusted, silently);
	//   - none of Alice's own config crossed with it — asserted against the raw
	//     bytes, because a key can be absent while its VALUE rode in under
	//     another name.
	ctx.Step(`^the instance's claude config carries the generated trust answer and the account identity, and none of Alice's own registrations or history$`, func(c context.Context) error {
		w := worldFrom(c)
		j := isoMatrixOf(w)
		body, err := isoReadSpyOut(j)
		if err != nil {
			return err
		}
		got := isoParseSpySection(body, "===CLAUDE_INSTANCE_CONFIG===")
		if got == "" {
			return fmt.Errorf("the spy read no .claude.json out of its config home — claude would meet its onboarding and trust dialogs with nothing answered; full spy dump:\n%s", body)
		}
		var cfg struct {
			HasCompletedOnboarding        bool `json:"hasCompletedOnboarding"`
			BypassPermissionsModeAccepted bool `json:"bypassPermissionsModeAccepted"`
			Projects                      map[string]struct {
				HasTrustDialogAccepted bool `json:"hasTrustDialogAccepted"`
			} `json:"projects"`
			MCPServers map[string]any `json:"mcpServers"`
		}
		if err := json.Unmarshal([]byte(got), &cfg); err != nil {
			return fmt.Errorf("the instance .claude.json is not valid JSON (%w):\n%s", err, got)
		}
		if !cfg.HasCompletedOnboarding {
			return fmt.Errorf("hasCompletedOnboarding did not cross into the instance — every agent session would re-onboard:\n%s", got)
		}
		if cfg.BypassPermissionsModeAccepted {
			return fmt.Errorf("the instance inherited Alice's standing bypass-permissions answer; that answer belongs to her own interactive session, not an agent run:\n%s", got)
		}
		workDir := isoParseSpyEnv(body)["PWD"]
		entry, ok := cfg.Projects[workDir]
		if !ok || !entry.HasTrustDialogAccepted {
			return fmt.Errorf("no generated trust answer for the run's own working directory %q — headless, claude proceeds untrusted rather than prompting; instance config:\n%s", workDir, got)
		}
		if len(cfg.Projects) != 1 {
			return fmt.Errorf("the instance carries %d project entries, want exactly the one this run works in — Alice's own projects map must never be copied wholesale:\n%s", len(cfg.Projects), got)
		}
		if cfg.MCPServers != nil {
			return fmt.Errorf("the agent's instance inherited Alice's own mcpServers registrations:\n%s", got)
		}
		for _, secret := range []string{isoFixturePersonalSecret, isoFixturePersonalHistory} {
			if strings.Contains(got, secret) {
				return fmt.Errorf("the agent's instance config carries Alice's own %q:\n%s", secret, got)
			}
		}
		// The account identity CROSSES (ruled 2026-09-21): claude's own
		// session seeding copies oauthAccount beside the credential, and it
		// is what claude shows and checks for a subscription token.
		if !strings.Contains(got, isoFixturePersonalEmail) {
			return fmt.Errorf("the agent's instance config does not carry the account identity (oauthAccount) claude reads with a subscription token:\n%s", got)
		}
		w.docStepMaterialized = "instance .claude.json, read from inside the spy process:\n" + got
		return nil
	})

	ctx.Step(`^the host "([^"]*)" credential file was never modified$`, func(c context.Context, engine string) error {
		w := worldFrom(c)
		rel, err := isoCredHostPath(engine)
		if err != nil {
			return err
		}
		got, err := w.env.ReadHomeFile(rel)
		if err != nil {
			return fmt.Errorf("host %s credential file missing after run: %w", engine, err)
		}
		want, err := isoCredFixtureContent(engine)
		if err != nil {
			return err
		}
		if strings.TrimSpace(got) != strings.TrimSpace(want) {
			return fmt.Errorf("host %s credential file content changed by the run — ctxloom never writes the user's own login; got:\n%s", engine, got)
		}
		w.docStepMaterialized = fmt.Sprintf("host %s credential file (%s), unchanged after the run:\n%s", engine, rel, strings.TrimSpace(got))
		return nil
	})
}

// isoTokenAuth is the engine's token-auth declaration off its own Home.
func isoTokenAuth(name string) (engine.TokenAuth, bool) {
	kind, ok := engines.Registry().Lookup(engine.Name(name))
	if !ok {
		return engine.TokenAuth{}, false
	}
	return kind.Home().Auth.Get()
}

// isoStoreSetupToken stores the fixture setup-token for engine where
// `ctxloom auth set-token` would (paths.HomeEngineTokenPath), owner-only,
// and empties engine's token var so the stored token is the one the run
// gets.
func isoStoreSetupToken(w *World, engine string) error {
	a, ok := isoTokenAuth(engine)
	if !ok {
		return fmt.Errorf("iso matrix: engine %q declares no token auth", engine)
	}
	rel := filepath.Join(paths.AppDirName, paths.HomeAuthDirName, engine+paths.EngineTokenExt)
	if err := w.env.WriteHomeFile(rel, isoFixtureSetupToken); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Join(w.env.HomeDir, rel), 0o600); err != nil {
		return err
	}
	w.env.SetEnv(a.TokenVar, "")
	return nil
}
