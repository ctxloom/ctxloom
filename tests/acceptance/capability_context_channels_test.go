// Untagged: the STRUCTURAL half of P1's adjudication, hermetic, no engine and
// no paid turn.
//
// WHY THIS FILE EXISTS. P1's first live pass produced two claims about delivery
// MECHANISMS, and both were inferred from what a model said rather than from
// what ctxloom wrote. That is exactly backwards for a probe whose whole subject
// is which mechanism carried the bytes: a model's answer is downstream of every
// channel at once, so it can never attribute one. S4's hook-firing probe found
// the same class of error from the other side (a cell answered correctly while
// its hook had provably never run — the engine had searched the workspace for
// the phrase), which is what forced this re-adjudication.
//
// The corrective is not a better live assertion. It is to pin the mechanisms
// where they are DECLARED — in each backend's SurfaceFor — by building the real
// surface set over an in-memory filesystem and looking at the bytes that land.
// A test here fails the day a backend changes what an approach delivers, which
// is the day a P1 cell would otherwise start quietly measuring something else.
//
// The finding below was established by reading production and is now held by
// it:
//
//   - claude's ApproachHook context delivery is a documented NO-OP. Pinning it
//     writes no context at all, which is why that cell reds — the route is not
//     broken, it is empty by declaration.
package acceptance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
)

// channelProbeHarp is a stand-in nonce for the structural tests. It never
// reaches an engine, so it does not go through the ledger — minting here would
// consume a cell key for a test that has no cell.
const channelProbeHarp = "probe-structural-harp"

// deliverContextUnder builds engine's REAL surface set over an in-memory
// filesystem, selects the context surface at approach, delivers it into a
// directory, and returns every file that landed with its content.
//
// The whole point is that nothing is simulated: this is backends.Declared
// and the engine's own declared constructor, the same two calls the launch
// path makes.
func deliverContextUnder(t *testing.T, engine string, approach string) map[string]string {
	t.Helper()
	fs := afero.NewMemMapFs()
	dir := "/work"
	require.NoError(t, fs.MkdirAll(dir, 0o755))

	delivery, ok := hostedDeclaration(engine).Construct(agent.SurfaceContext, approach, agent.SurfaceInputs{
		Context:   "The nonce for this session is " + channelProbeHarp,
		Fragments: []*agent.Fragment{{Name: "nonce", Content: "The nonce for this session is " + channelProbeHarp}},
	}, fs)
	require.True(t, ok, "%s must construct its context surface at %s — the P1 cell that pins it depends on this call succeeding", engine, approach)
	require.NotNil(t, delivery)

	_, err := delivery.Deliver(present.ProjectOnHost(dir))
	require.NoError(t, err)

	out := map[string]string{}
	require.NoError(t, afero.Walk(fs, dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return err
		}
		b, rerr := afero.ReadFile(fs, path)
		if rerr != nil {
			return rerr
		}
		rel, _ := filepath.Rel(dir, path)
		out[rel] = string(b)
		return nil
	}))
	return out
}

// TestClaudeHookApproach_DeliversNothing pins the mechanism behind P1's one red.
//
// claude's context at ApproachHook is the shared agent.HookCarriedContext —
// a Rider whose own Deliver is a documented no-op: the context rides the
// settings-borne inject hook plus a cache file, both of which the LAUNCH
// installs once it sees the rider resolved (LaunchBackend.deliverSet, on
// every cell). The surface itself therefore writes nothing, and this pins
// that: if it has started writing, the launch would double the context.
func TestClaudeHookApproach_DeliversNothing(t *testing.T) {
	files := deliverContextUnder(t, "claude-code", agent.ApproachHook)
	require.Empty(t, files,
		"claude's context surface at ApproachHook is documented as a no-op. If it has started writing something, P1's red cell must be re-measured rather than assumed: got %v", keysOf(files))
}

// deliverContextAcrossRoots builds engine's REAL surface set over an in-memory
// filesystem and delivers its context surface at approach, with the project
// root and the engine home advised as DISTINCT directories so that an
// assertion about one cannot be satisfied by a write to the other. It returns
// every file that landed, partitioned by whether it is beneath the project
// root, keyed by absolute path.
//
// It advises four separate roots rather than the single one ProjectOnHost
// gives, because the question here is precisely WHICH root the bytes chose.
func deliverContextAcrossRoots(t *testing.T, engine, approach string) (inProject, outsideProject map[string]string) {
	t.Helper()
	const (
		projectRoot = "/probe/project"
		engineHome  = "/probe/engine-home"
		ctxloomHome = "/probe/ctxloom-home"
		scratch     = "/probe/scratch"
	)
	fs := afero.NewMemMapFs()
	for _, d := range []string{projectRoot, engineHome, ctxloomHome, scratch} {
		require.NoError(t, fs.MkdirAll(d, 0o755))
	}

	body := "The nonce for this session is " + channelProbeHarp
	delivery, ok := hostedDeclaration(engine).Construct(agent.SurfaceContext, approach, agent.SurfaceInputs{
		Context:   body,
		Fragments: []*agent.Fragment{{Name: "nonce", Content: body}},
	}, fs)
	require.True(t, ok, "%s must construct its context surface at %s — the P1 cell that pins it depends on this call succeeding", engine, approach)
	require.NotNil(t, delivery)

	_, err := delivery.Deliver(present.New(present.OnHost(present.Paths{
		ProjectRoot: present.Root{Host: projectRoot},
		EngineHome:  present.Root{Host: engineHome},
		CtxloomHome: present.Root{Host: ctxloomHome},
		Scratch:     present.Root{Host: scratch},
	})))
	require.NoError(t, err, "%s context=%s must deliver when every root is advised", engine, approach)

	inProject, outsideProject = map[string]string{}, map[string]string{}
	require.NoError(t, afero.Walk(fs, "/", func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return err
		}
		b, rerr := afero.ReadFile(fs, path)
		if rerr != nil {
			return rerr
		}
		if path == projectRoot || strings.HasPrefix(path, projectRoot+"/") {
			inProject[path] = string(b)
			return nil
		}
		outsideProject[path] = string(b)
		return nil
	}))
	return inProject, outsideProject
}

// TestSharedCwdDelivery_OnlyClaudeSystemPromptStaysOutOfTheWorkspace is the
// structural basis for the side-channel judgement recorded against every P1
// cell.
//
// A workspace=none cell is a SHARED cell, so "does this cell's DELIVERY put
// nonce bytes where a workspace search can reach them" has to be answered
// about the BYTES, per (engine, approach), and nowhere else.
//
// It is answered here by delivering for real against a filesystem whose
// project root and engine home are different directories, and then looking at
// what landed in each. It deliberately does NOT ask the predicate a shared
// launch derives its preference from: that predicate is a disjunction of
// reasons an approach MAY avoid the cwd — two of its arms are type assertions,
// which establish that a type declares a form, not that this delivery used it.
// A test calling it would agree with the rule even when the rule is wrong, and
// could only ever catch a change in what Construct returns.
//
// The answer is lopsided, and the asymmetry is exactly which of claude's cells
// can be argued side-channel-controlled:
//
//	claude  context/system-prompt -> lands beneath the run's PRIVATE root, never the project
//	claude  context/unsafe-file   -> the project file: the caller asked for CLAUDE.md
//	claude  context/hook          -> writes nothing: it is a no-op anyway
//
// BOTH halves of the system-prompt claim are asserted. The negative alone —
// "nothing under the project root" — is satisfied by a delivery that writes
// nothing at all anywhere, which is this codebase's signature failure (exit 0,
// a success message, zero bytes). So the positive half pins that the context
// really did materialize, with its nonce in it, outside the workspace.
func TestSharedCwdDelivery_OnlyClaudeSystemPromptStaysOutOfTheWorkspace(t *testing.T) {
	inProject, outside := deliverContextAcrossRoots(t, "claude-code", claude.ApproachSystemPrompt)

	// POSITIVE: the framed system prompt really landed, carrying the nonce,
	// somewhere that is not the project root.
	var framed string
	for path, content := range outside {
		if strings.HasSuffix(path, agent.SCMFramedContextSuffix) {
			framed = path
			assert.Contains(t, content, channelProbeHarp,
				"the framed system prompt at %s must actually carry the session's context; an empty or stale file would make the cell measure nothing", path)
			assert.Contains(t, content, agent.ProjectContextHeader,
				"the framed system prompt must carry the ctxloom framing")
		}
	}
	require.NotEmpty(t, framed,
		"claude's system-prompt delivery must WRITE the framed context outside the project root. Nothing matching %q landed anywhere: a delivery that writes nothing would satisfy the out-of-workspace half of this test while giving the model no context at all. Wrote outside: %v",
		agent.SCMFramedContextSuffix, keysOf(outside))

	// NEGATIVE: and it put nothing at all where a workspace search could reach.
	assert.Empty(t, inProject,
		"claude's system-prompt approach must leave the project root untouched: it is the ONE context delivery in the ladder that puts no nonce bytes in the workspace, and P1's side-channel argument for that cell rests entirely on it. Leaked: %v", keysOf(inProject))

	// THE CONTRAST: unsafe-file is the caller's explicit request for the native
	// in-workspace write. If it stopped landing in the project root the two
	// claude cells would be measuring the same thing.
	unsafeInProject, _ := deliverContextAcrossRoots(t, "claude-code", agent.ApproachUnsafeFile)
	require.NotEmpty(t, unsafeInProject,
		"unsafe-file must write its context INTO the project root — that is the whole of what the caller asked for, and the contrast that makes the system-prompt cell meaningful")
	var wroteNonce bool
	for _, content := range unsafeInProject {
		if strings.Contains(content, channelProbeHarp) {
			wroteNonce = true
		}
	}
	assert.True(t, wroteNonce,
		"unsafe-file's in-workspace file must carry the nonce, or it is not the reachable side channel this cell is contrasted against: wrote %v", keysOf(unsafeInProject))
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// hostedDeclaration is the named engine's named-form table off the engine
// value (agent.Hosted); empty for an engine that is not Hosted.
func hostedDeclaration(name string) agent.Declaration {
	h, ok := engines.Hosted(name)
	if !ok {
		return agent.Declaration{}
	}
	return h.Declaration()
}
