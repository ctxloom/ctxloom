//go:build acceptance

// J001400 (j001400_bundle_distribution.feature): publishing a directory-form bundle
// carrying EVERY surface kind, and a consumer receiving each kind's payload
// intact — across the two isolation axes.
//
// The publication AND consumption halves are GREEN. A directory-form bundle
// used to be unfetchable — fetchAtLockedSHA resolved a ref to ONE file and
// called FetchFile on it — while skills REQUIRE the directory form
// (internal/bundles/loader.go:389). `remote pull` now probes the directory
// form, walks the tree at the pinned SHA through internal/content/remotetree,
// and installs it under the consumer's cache with the publisher's exec bit
// intact; config.loadRemoteBundleSeed then reads the installed tree back into a
// bundle through convert.Read, verified by internal/content/attest.
//
// THE DELIVERY HALF IS NOW HERMETIC TOO, on the host runtime. The vehicle is
// `profile materialize --backend mock`, over the mock backend's own context and
// skills surfaces (internal/lm/backends/mock_surfaces.go) — the shared
// agent.WriteManagedContext and agent.WriteManagedSkillPackages writers every
// real engine uses, differing only in the directory they target. Materialize
// rather than `ctxloom run` because a run's Cleanup strips what it delivered
// before any step could stat it (grpc.RunTurn calls Cleanup immediately after
// Execute and the shared LIFO reversal removes the managed section); the
// mid-turn half is covered in tests/integration/delivery_approach_matrix_test.go
// and grpc_test's TestRunTurn_MockDeliversContextSurfaceDuringTheTurn.
//
// THE CONTAINER HALF IS HERMETIC TOO, through internal/testsupport/containercell:
// a `FROM scratch` image carrying one static ctxloom, the environment root
// bind-mounted at its own absolute path, `--network=none`, and the SAME
// `profile materialize --backend mock` executed inside the container. The rows
// assert on the HOST side of that mount, and add OWNERSHIP to bytes and mode —
// the one property a process boundary breaks while the other two come through
// untouched (a rootful daemon writes byte-identical, mode-identical, ROOT-OWNED
// files). See j001400DeliverInContainer for the anti-vacuity guards.
//
// HOOK ORDER IS ALSO HERMETIC NOW. The fixture declares its post_file_edit
// hooks' order the only way a tree-form bundle can — a content.Hook.Order
// sidecar per hook (see j001400AuthoredTree) — and the consumer-side assertions
// read the pulled tree through the same content.NewTreeStore + Bundle.Refs /
// Item / Surface path the product itself uses, then resolve execution order
// with content.SortHooks (see j001400OpenPulledBundle, j001400ConsumerHookOrder,
// j001400CheckHookBuckets), rather than trusting directory enumeration — which
// sorts BY NAME and would silently agree with a tree whose hooks are the
// right bytes in the wrong order.
//
// TWO DELIBERATE CHOICES ABOUT HOW IT FAILS — they still matter, because the
// rows that remain red must keep naming the gap rather than an exit code:
//
//  1. The pull step does NOT use runOK. If it aborted on a non-zero exit, the
//     scenario would fail at the WHEN with a CLI error and never state what
//     the consumer was supposed to end up with. Instead the pull outcome is
//     recorded and quoted by whichever THEN assertion fires, so the failure
//     message names both the missing payload and the fetch diagnostic that
//     explains it. A red test whose message does not identify the gap is a
//     maintenance burden rather than a specification.
//
//  2. Assertions are on PAYLOAD — bytes off disk, parsed structure, POSIX
//     modes, ordered hook names — never on an exit code and never on a
//     substring of a success message. This journey exists because a capability
//     is missing; an exit-code assertion would go green the moment some
//     command started exiting 0 without delivering anything, which is this
//     codebase's characteristic failure mode.
//
// The remote seeding here is mode-aware and therefore local rather than
// testenv.SeedRemote: that helper writes every file 0o644, which would drop
// the skill script's exec bit AT THE SOURCE and make the consumer-side mode
// assertion vacuous — it would be asserting the fixture, not the product.
// git records the exec bit as tree mode 100755, so a mode-preserving seed is
// what makes "the exec bit survived publication" a real claim.
package acceptance

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/cucumber/godog"
	"github.com/spf13/afero"
	"golang.org/x/crypto/ssh"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/content"
	"github.com/ctxloom/ctxloom/internal/content/attest"
	"github.com/ctxloom/ctxloom/internal/testsupport/containercell"
	"github.com/ctxloom/ctxloom/internal/testsupport/dockergate"
	"github.com/ctxloom/ctxloom/internal/trust"
	"github.com/ctxloom/ctxloom/tests/integration/testenv"
)

// j001400State is this journey's fixture state.
type j001400State struct {
	signer    *testenv.TestSigner
	principal string

	// authored is the bundle tree Trent wrote, bundle-relative path -> file.
	// It is the ONLY source of truth for what "as published" means: every
	// consumer-side assertion compares against this map rather than against a
	// second literal, so a fixture typo cannot make an assertion vacuously
	// agree with itself.
	authored map[string]j001400File

	url  string // file:// URL of the seeded bare repo
	bare string // the bare repo dir (url minus the file:// prefix)

	pullErr    error  // the pull's error, if any — quoted by later assertions
	pullOutput string // the pull's combined output, likewise
	pulled     bool   // whether the reference+pull step ran at all

	runErr    error  // the isolation-matrix delivery's error, if any
	runOutput string // that delivery's output
	agentRoot string // where that agent could read files, if it got that far
	// agentBackend is the engine whose NATIVE surface layout the delivered
	// artifacts were written in — the matrix asserts paths, and a path is only
	// meaningful per engine (.claude/skills vs .mock/skills, CLAUDE.md vs
	// MOCK_CONTEXT.md). Empty until a delivery step sets it, so
	// j001400AgentVisiblePath can refuse rather than guess.
	agentBackend string
	// cellRuntime names the container runtime a container row actually ran
	// under ("docker-rootless", ...), empty for a host row. A matrix that
	// cannot say WHICH runtime it exercised cannot claim to have covered one.
	cellRuntime string
}

func j001400Of(w *World) *j001400State {
	if w.j001400 == nil {
		w.j001400 = &j001400State{authored: map[string]j001400File{}}
	}
	return w.j001400
}

// j001400File is one authored file: its exact bytes and its declared POSIX mode.
// The mode is carried explicitly rather than inferred from the path because
// it is an ASSERTED property — a skill script is 0755 because the author said
// so, and the whole point is to check that claim survives the trip.
type j001400File struct {
	Body string
	Mode os.FileMode
}

const j001400Bundle = "atelier"

// j001400BundleRel is where a bundle's tree lives inside a publishing repo — the
// same ".ctxloom/content/bundles/<name>/" layout an authored project uses,
// which is what makes "publish" a copy rather than a transformation.
func j001400BundleRel(rel string) string {
	return ".ctxloom/content/bundles/" + j001400Bundle + "/" + rel
}

// j001400ConfigYAML is Alice's project config. It declares TWO engines rather than
// the one buildJ000200Config renders, and the second one is the point.
//
// The publication scenarios only need a project that exists, and claude-code —
// the engine an ordinary consumer runs — is what they had. But the DELIVERY
// MATRIX has to observe delivered files on a machine with no engine installed,
// and the only backend that both materializes real surfaces and needs nothing
// on the host is "mock" (internal/lm/backends/mock_surfaces.go: a context
// surface at MOCK_CONTEXT.md and a skills tree at .mock/skills/, both through
// the SAME shared writers claude/kiro/opencode/antigravity go through).
// Hardcoding a single claude-code engine here is what previously kept every
// matrix row away from that route.
//
// Both are DECLARED, not just registered: `--backend mock` resolves against
// the backend registry, but a project that materializes for an engine it never
// declared is not a configuration any consumer would have, and the matrix is
// supposed to describe one.
func j001400ConfigYAML() string {
	return `version: 4
llm:
  configs:
    claude-code:
      type: claude-code
    mock:
      type: mock
  defaults:
    primary: claude-code
    fast: claude-code
agents:
  default:
    engine: claude-code
    profiles:
      - default
default_agent: default
`
}

// j001400AuthoredTree is the fixture: one artifact of EVERY surface kind a bundle
// can hold, in the canonical tree layout that internal/content already reads
// (see internal/content/testdata/tree). Two spellings are easy to get wrong
// and are pinned here deliberately:
//
//   - a command lives under "prompts/", not "commands/" — trust.KindPrompt's
//     Dir() is "prompts" (the skill/command rename freed "skills" for real
//     Agent Skills).
//   - metadata placement is per-kind, not uniform: the .md content kinds carry
//     it as FRONT-MATTER inside their own bytes, while the .yaml kinds use a
//     ".<name>.meta.yaml" SIDECAR — a separate file a publication path must
//     remember to carry.
//
// The post_file_edit hooks are named so that ALPHABETICAL AND DECLARED ORDER
// DISAGREE ("stamp" declared first, "audit" second). A fixture whose names
// happened to sort into declared order would let a sequence-losing tree format
// pass the ordering assertion while proving nothing.
func j001400AuthoredTree() map[string]j001400File {
	f := func(body string) j001400File { return j001400File{Body: body, Mode: 0o644} }
	return map[string]j001400File{
		"bundle.yaml": f("version: \"1.0.0\"\ndescription: Trent's atelier bundle\n"),

		// fragment — content kind, .md, metadata as front-matter.
		//
		// The BODY is deliberately several lines long. A fragment is not
		// delivered as a file: it is merged into the engine's context file
		// (CLAUDE.md / MOCK_CONTEXT.md), so the delivery-matrix assertion can
		// only be "this body appears VERBATIM inside that file". A one-line
		// body would reduce that to a marker search, which a delivery path that
		// reflowed, truncated or re-wrapped the fragment would still pass.
		"fragments/house-style.md": f("---\ndescription: ATELIER-FRAGMENT-DESC\n---\n\nATELIER-FRAGMENT-4a91c2\n\nHouse style, first rule: name the thing.\nHouse style, second rule: say why, not what.\nHouse style, third rule: delete the third rule.\n"),

		// fragment — the one STUDIO selects, and the only carrier of the
		// profile marker. It exists so a profile's delivery can be asserted by
		// its EFFECT: a profile is not content an assistant reads, it is what
		// SELECTS what an assistant reads, so the only way to observe that a
		// published profile arrived AND resolves AND is honored is to
		// materialize it and look for content only IT selects.
		"fragments/studio-brief.md": f("---\ndescription: ATELIER-STUDIO-BRIEF-DESC\n---\n\nATELIER-PROFILE-6b41fc\n\nThe studio brief: ship the smallest true thing.\n"),

		// command — the OTHER content kind, also .md/front-matter, but a
		// user-invoked slash template rather than model-read context. Same
		// spelling, different meaning; both are asserted.
		"prompts/ship-it.md": f("---\ndescription: ATELIER-COMMAND-DESC\n---\n\nATELIER-COMMAND-7d33e1\n"),

		// mcp — executable kind, .yaml, metadata in a sidecar. Structured:
		// command/args/env are asserted as PARSED FIELDS, not as a substring.
		"mcp/ledger.yaml":       f("command: /usr/bin/ledger-mcp\nargs:\n  - --serve\n  - --marker\n  - ATELIER-MCP-1f88b0\nenv:\n  LEDGER_MODE: readonly\n"),
		"mcp/.ledger.meta.yaml": f("description: ATELIER-MCP-DESC\n"),

		// hooks — one file per hook under hooks/<event>/<name>.yaml. Two under
		// post_file_edit (order matters, names sort against it), one under
		// session_start (proves event bucketing, not just a flat list).
		//
		// The tree format's ONLY order carrier is content.Hook.Order, a sparse
		// int living in each hook's own ".<name>.meta.yaml" sidecar (see
		// content.SortHooks / internal/content/convert.go's hookOrder — a
		// directory has no list, so a tree-form bundle that wants "stamp" before
		// "audit" has no way to say so except this field). "stamp" is declared
		// first (order 100) and "audit" second (order 200) — the reverse of
		// their alphabetical/filename order — so a reader that fell back to
		// directory enumeration instead of resolving Order would get the wrong
		// sequence and disagree with this fixture, not agree with it by luck.
		"hooks/post_file_edit/stamp.yaml":       f("type: command\ncommand: echo ATELIER-HOOK-9c02af\n"),
		"hooks/post_file_edit/.stamp.meta.yaml": f("order: 100\n"),
		"hooks/post_file_edit/audit.yaml":       f("type: command\ncommand: echo ATELIER-HOOK-audit\n"),
		"hooks/post_file_edit/.audit.meta.yaml": f("order: 200\n"),
		"hooks/session_start/greet.yaml":        f("type: command\ncommand: echo ATELIER-HOOK-greet\n"),

		// skill — the only MULTI-FILE item and the only one carrying a
		// load-bearing POSIX mode.
		"skills/reviewer/SKILL.md":       f("---\nname: reviewer\ndescription: ATELIER-SKILL-3e77da\n---\n\nATELIER-SKILL-3e77da\n"),
		"skills/reviewer/scripts/run.sh": {Body: "#!/bin/sh\necho ATELIER-SKILL-SCRIPT-8b21ce\n", Mode: 0o755},
		// The sidecar DECLARES which files are executable, and that declaration
		// is the authority — a mode bit is not portable, so the tree format
		// attests the declaration instead of the filesystem (see
		// content.ComponentMode). A publisher who ships a 0755 file without
		// listing it here produces a manifest claiming 0644, and the consumer
		// rejects the package on extraction with an integrity error. The two
		// must agree, and the fixture says so in both places on purpose.
		"skills/.reviewer.meta.yaml": f("description: ATELIER-SKILL-DESC\nexecutable:\n  - scripts/run.sh\n"),

		// profile — the sixth kind. Never trust-gated as an item, but still a
		// file in the tree that must arrive intact.
		//
		// It selects ONE fragment by item ref rather than pulling the whole
		// bundle, because that is what makes materializing it an EFFECT
		// assertion rather than a bundle assertion: the marker below can only
		// reach a target if the profile arrived, was seeded into the shared
		// loader, its short same-repo ref resolved against the PULLED tree, and
		// the selected fragment was then delivered. The marker deliberately
		// lives in that fragment, not in this description — a description is
		// metadata about the selector, and no engine surface ever renders it.
		"profiles/studio.yaml": f("description: ATELIER-PROFILE-DESC\nbundles:\n  - atelier#fragments/studio-brief\n"),
	}
}

// j001400SeedTreeRemote seeds a bare git repo carrying the authored tree WITH ITS
// MODES, and signs the named paths if a signer is given. It mirrors
// testenv.SeedRemote's plumbing but preserves each file's mode, for the reason
// in this file's package doc.
func j001400SeedTreeRemote(w *World, st *j001400State) error {
	root, err := os.MkdirTemp(w.env.Root, "j001400-remote-*")
	if err != nil {
		return fmt.Errorf("create remote root: %w", err)
	}
	bare := filepath.Join(root, "remote.git")
	work := filepath.Join(root, "work")

	if err := j001400GitAll("", [][]string{{"init", "--bare", "-b", "main", bare}, {"init", "-b", "main", work}}); err != nil {
		return err
	}
	if err := j001400GitAll(work, [][]string{
		{"config", "user.email", "trent@example.com"},
		{"config", "user.name", "Trent"},
		{"config", "commit.gpgsign", "false"},
	}); err != nil {
		return err
	}
	if err := j001400WriteTree(work, st.authored); err != nil {
		return err
	}
	if err := j001400SignTree(work, st); err != nil {
		return err
	}
	if err := j001400GitAll(work, [][]string{
		{"add", "-A"},
		{"commit", "-m", "publish atelier tree"},
		{"remote", "add", "origin", bare},
		{"push", "origin", "main"},
	}); err != nil {
		return err
	}
	if err := j001400Git(bare, "symbolic-ref", "HEAD", "refs/heads/main"); err != nil {
		return err
	}
	st.bare = bare
	st.url = "file://" + bare
	return nil
}

// j001400GitAll runs a sequence of git commands in dir, stopping at the first
// failure. Seeding is a linear script of them and inlining each loop made the
// one step that is NOT a git command — signing the tree — hard to see among
// them.
func j001400GitAll(dir string, cmds [][]string) error {
	for _, a := range cmds {
		if err := j001400Git(dir, a...); err != nil {
			return err
		}
	}
	return nil
}

// j001400Git runs one git command, surfacing its combined output on failure so a
// seeding problem is diagnosable rather than a bare exit status.
func j001400Git(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %s in %s: %w: %s", strings.Join(args, " "), dir, err, out)
	}
	return nil
}

// j001400WriteTree writes the authored bundle tree into a working clone WITH each
// file's declared mode. The explicit Chmod is not redundant: os.WriteFile
// applies a mode only at CREATION, so a rewrite would silently drop the exec
// bit — the same trap internal/shared/agent/packagefiles.go documents on the
// delivery side.
func j001400WriteTree(work string, authored map[string]j001400File) error {
	for rel, file := range authored {
		full := filepath.Join(work, filepath.FromSlash(j001400BundleRel(rel)))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return fmt.Errorf("mkdir for %s: %w", rel, err)
		}
		if err := os.WriteFile(full, []byte(file.Body), file.Mode); err != nil {
			return fmt.Errorf("write %s: %w", rel, err)
		}
		if err := os.Chmod(full, file.Mode); err != nil {
			return fmt.Errorf("chmod %s: %w", rel, err)
		}
	}
	return nil
}

// j001400SignTree signs the authored tree with Trent's key, which is what the
// scenarios' "signed with the company key" actually means: attest.SignBundle
// builds the tree's SHA256SUMS manifest and writes a detached publisher
// signature over it into the tree's own .sigs/ store, so both travel with the
// bundle to a consumer who has never seen its content.
//
// It goes through the PRODUCT's signing path rather than hand-writing a manifest
// and a .sig. Hand-rolling either would make this fixture a second
// implementation of the signed-tree format, and the first thing it would stop
// catching is the format drifting out from under it.
//
// There is no CLI route to it yet — `ctxloom bundle sign` signs a single file's
// bytes — so the Go API is used directly (taskloom: no verb signs a tree).
// Publishing is likewise not the product's job here: remote.NewPublisher refuses
// generic git hosts, so a bare-repo fixture has to push with git either way.
func j001400SignTree(work string, st *j001400State) error {
	if st.signer == nil {
		return fmt.Errorf("Trent has no publishing key, so the tree cannot be signed")
	}
	root := filepath.Join(work, ".ctxloom", "content", "bundles")
	store, err := content.NewTreeStore(afero.NewOsFs(), root, content.Provenance{RepoURL: "https://example.test/trent/company"})
	if err != nil {
		return fmt.Errorf("open the authored tree at %s: %w", root, err)
	}
	ctx := context.Background()
	bundle, err := store.Open(ctx, content.BundleID(j001400Bundle))
	if err != nil {
		return fmt.Errorf("open the %q tree for signing: %w", j001400Bundle, err)
	}
	if err := attest.SignBundle(ctx, store, bundle, st.signer.Signer); err != nil {
		return fmt.Errorf("sign the %q tree with Trent's key: %w", j001400Bundle, err)
	}
	return nil
}

// j001400ConsumerTreePath is where a PULLED bundle tree would have to land for a
// consumer to use it: under the cache's bundles root. The exact remote-name
// segment is derived from the URL by remote.Reference.LocalRemoteName, so
// rather than reimplementing that derivation this searches the cache for the
// bundle directory — which also means the assertion still reports usefully if
// the pull landed the tree somewhere unexpected instead of nowhere.
func j001400ConsumerTreePath(w *World, rel string) (string, bool) {
	cacheRoot := filepath.Join(w.env.ProjectDir, ".ctxloom", "cache", "bundles")
	var found string
	_ = filepath.WalkDir(cacheRoot, func(p string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() || found != "" {
			return nil //nolint:nilerr // a missing cache is the ordinary red case, not an error to surface here
		}
		if d.Name() == j001400Bundle {
			found = p
		}
		return nil
	})
	if found == "" {
		return "", false
	}
	return filepath.Join(found, filepath.FromSlash(rel)), true
}

// j001400PullDiagnostic renders what the pull actually did, for quoting inside an
// assertion failure. This is what makes a red run diagnosable at a glance
// rather than only "file not found".
func (st *j001400State) j001400PullDiagnostic() string {
	if !st.pulled {
		return "the reference+pull step never ran"
	}
	if st.pullErr != nil {
		return fmt.Sprintf("the pull FAILED: %v\n--- pull output ---\n%s", st.pullErr, st.pullOutput)
	}
	return fmt.Sprintf("the pull reported success; its output was:\n%s", st.pullOutput)
}

// j001400RequireConsumerFile reads one file from the consumer's pulled tree, or
// returns the diagnostic-bearing failure the scenario exists to produce.
func j001400RequireConsumerFile(w *World, rel string) ([]byte, os.FileInfo, error) {
	st := j001400Of(w)
	p, ok := j001400ConsumerTreePath(w, rel)
	if !ok {
		return nil, nil, fmt.Errorf("no %q bundle DIRECTORY exists anywhere under the consumer's .ctxloom/cache/bundles — "+
			"a directory-form bundle was published but the consumer received no tree.\n%s",
			j001400Bundle, st.j001400PullDiagnostic())
	}
	body, err := os.ReadFile(p)
	if err != nil {
		return nil, nil, fmt.Errorf("the consumer's %q tree exists but %q is missing from it: %w\n%s",
			j001400Bundle, rel, err, st.j001400PullDiagnostic())
	}
	info, err := os.Stat(p)
	if err != nil {
		return nil, nil, fmt.Errorf("stat consumer %q: %w", rel, err)
	}
	return body, info, nil
}

func registerJ001400Steps(ctx *godog.ScenarioContext) {
	// --- Given: authoring + trust ------------------------------------------

	ctx.Step(`^Trent authors a directory-form bundle "([^"]*)" carrying every surface kind$`, func(c context.Context, name string) error {
		w := worldFrom(c)
		st := j001400Of(w)
		if name != j001400Bundle {
			return fmt.Errorf("this journey's fixture publishes %q, not %q", j001400Bundle, name)
		}
		if err := scaffoldProjectWithConfig(w, j001400ConfigYAML()); err != nil {
			return err
		}
		signer, err := testenv.GenerateTestSigner()
		if err != nil {
			return fmt.Errorf("generate Trent's publishing key: %w", err)
		}
		st.signer = signer
		st.authored = j001400AuthoredTree()
		// Guard against a fixture that silently authors nothing: an empty
		// authored map would make every "byte for byte" assertion below
		// compare nothing against nothing and pass.
		if len(st.authored) < 6 {
			return fmt.Errorf("fixture authored only %d files; it must carry an artifact of every surface kind", len(st.authored))
		}
		return nil
	})

	ctx.Step(`^Alice trusts Trent's publishing key$`, func(c context.Context) error {
		w := worldFrom(c)
		st := j001400Of(w)
		if st.signer == nil {
			return fmt.Errorf("Trent has not authored anything yet, so there is no key to trust")
		}
		st.principal = "trent@example.com"
		keyPath := filepath.Join(w.env.Root, "j001400-trent.pub")
		if err := os.WriteFile(keyPath, ssh.MarshalAuthorizedKey(st.signer.Public), 0o644); err != nil {
			return fmt.Errorf("write Trent's public key: %w", err)
		}
		return runOK(w, "trust", "signer", "create", st.principal, "--key", keyPath, "--project")
	})

	// --- Given/When: publication and consumption ----------------------------

	ctx.Step(`^Trent publishes the "([^"]*)" tree to his company repo, signed with the company key$`, func(c context.Context, name string) error {
		w := worldFrom(c)
		st := j001400Of(w)
		if name != j001400Bundle {
			return fmt.Errorf("this journey publishes %q, not %q", j001400Bundle, name)
		}
		return j001400SeedTreeRemote(w, st)
	})

	// Deliberately NOT runOK — see this file's package doc. The outcome is
	// recorded so the payload assertions can quote it.
	ctx.Step(`^Alice references the company's "([^"]*)" bundle and pulls it$`, func(c context.Context, name string) error {
		w := worldFrom(c)
		st := j001400Of(w)
		if st.url == "" {
			return fmt.Errorf("Trent has not published %q yet", name)
		}
		if err := runOK(w, "remote", "create", "company", st.url, "--forge", "git"); err != nil {
			return err
		}
		// A profile needs at least one bundle or parent at creation time, so
		// the consumer gets a local seed bundle first and the published one is
		// ADDED to the profile below. Same shape J001600 uses. Doing it in one
		// step would make the profile's creation depend on the very fetch this
		// scenario is testing, and the failure would then read as "could not
		// create a profile" rather than "the published tree never arrived".
		// ensureProjectWithEngine may already have seeded this bundle; creating
		// it again is a hard error, so create only when absent.
		if !w.env.FileExists(".ctxloom/content/bundles/seed.yaml") {
			if err := runOK(w, "bundle", "create", "seed", "-d", "J001400 consumer seed bundle"); err != nil {
				return err
			}
		}
		if !w.env.FileExists(".ctxloom/profiles/default.yaml") {
			if err := runOK(w, "profile", "create", "default", "-b", "seed", "-d", "J001400 consumer profile"); err != nil {
				return err
			}
		}
		// Adding the bundle to a profile is what makes `remote pull` fetch it;
		// it may itself refuse, which is equally a symptom of the gap, so its
		// outcome is recorded rather than fatal.
		_ = w.env.Run("profile", "modify", "default", "--add-bundle", "company/"+name)
		addOut := w.env.LastOutput()

		st.pulled = true
		if perr := w.env.Run("remote", "pull"); perr != nil || w.env.LastExitCode() != 0 {
			st.pullErr = fmt.Errorf("`remote pull` exited %d", w.env.LastExitCode())
		}
		st.pullOutput = "profile modify said:\n" + addOut + "\nremote pull said:\n" + w.env.LastOutput()
		return nil
	})

	// --- Then: payload assertions -------------------------------------------

	ctx.Step(`^Alice's pulled "([^"]*)" tree carries "([^"]*)" byte for byte as published$`, func(c context.Context, name, rel string) error {
		w := worldFrom(c)
		st := j001400Of(w)
		want, ok := st.authored[rel]
		if !ok {
			return fmt.Errorf("the fixture never authored %q, so there is nothing to compare against", rel)
		}
		got, _, err := j001400RequireConsumerFile(w, rel)
		if err != nil {
			return err
		}
		if string(got) != want.Body {
			return fmt.Errorf("consumer's %q differs from what Trent published\n--- published (%d bytes) ---\n%s\n--- received (%d bytes) ---\n%s",
				rel, len(want.Body), want.Body, len(got), got)
		}
		return nil
	})

	ctx.Step(`^the "([^"]*)" reaches Alice's assistant carrying the marker "([^"]*)"$`, func(c context.Context, kind, marker string) error {
		w := worldFrom(c)
		st := j001400Of(w)
		// Materialize the consumer's profile and search every delivered
		// surface — the assistant-visible payload, not the cache.
		_ = w.env.Run("profile", "materialize", "default", "--target", "out", "--backend", "claude-code")
		matOut := w.env.LastOutput()
		hits, err := j001400MarkerHits(filepath.Join(w.env.ProjectDir, "out"), marker)
		if err != nil {
			return err
		}
		if len(hits) == 0 {
			return fmt.Errorf("the %s marker %q reached NOTHING the assistant can read under out/ — "+
				"the published %s never arrived.\n%s\n--- materialize said ---\n%s",
				kind, marker, kind, st.j001400PullDiagnostic(), matOut)
		}
		return nil
	})

	// A profile cannot "reach an assistant" the way the five content kinds do:
	// it is not content an assistant reads, it is what SELECTS what an
	// assistant reads. So delivery is asserted by its EFFECT — materialize THAT
	// profile, by its bundle-qualified ref, and require the content only it
	// selects to arrive. That catches all three ways this breaks: the profile
	// file arrives but is never seeded into the shared loader; it is seeded but
	// its selectors do not resolve against the pulled tree; they resolve but the
	// selected content is never delivered.
	ctx.Step(`^materializing Alice's "([^"]*)" delivers the marker "([^"]*)"$`, func(c context.Context, profileRef, marker string) error {
		w := worldFrom(c)
		st := j001400Of(w)
		// A target of its OWN, never the one the "default" profile materializes
		// into: sharing a directory would let content default selected satisfy
		// an assertion about what STUDIO selects.
		target := filepath.Join(w.env.ProjectDir, "out-studio")
		_ = w.env.Run("profile", "materialize", profileRef, "--target", target, "--backend", "mock")
		matOut := w.env.LastOutput()
		hits, err := j001400MarkerHits(target, marker)
		if err != nil {
			return fmt.Errorf("%w\n%s\n--- materialize said ---\n%s", err, st.j001400PullDiagnostic(), matOut)
		}
		if len(hits) == 0 {
			return fmt.Errorf("materializing the published profile %q delivered NOTHING carrying %q — "+
				"the profile arrived on disk, but it was not seeded into the shared profile loader, or its selectors did not resolve "+
				"against the pulled tree, or the content it selects was never delivered.\n%s\n--- materialize said ---\n%s",
				profileRef, marker, st.j001400PullDiagnostic(), matOut)
		}
		return nil
	})

	ctx.Step(`^Alice's pulled "([^"]*)" tree stores the "([^"]*)" metadata "([^"]*)" as (.+)$`, func(c context.Context, name, kind, probe, placement string) error {
		w := worldFrom(c)
		// The placement phrase names the FILE the metadata must live in; the
		// distinction the scenario is making is front-matter (inside the .md)
		// versus a separate sidecar file, so the assertion is simply "this
		// probe is in THAT file" — with the file chosen per kind.
		rel, err := j001400PlacementFile(placement)
		if err != nil {
			return err
		}
		got, _, err := j001400RequireConsumerFile(w, rel)
		if err != nil {
			return err
		}
		if !strings.Contains(string(got), probe) {
			return fmt.Errorf("the %s metadata %q is NOT stored in %q as the layout requires; that file's %d bytes are:\n%s",
				kind, probe, rel, len(got), got)
		}
		return nil
	})

	ctx.Step(`^Alice's pulled "([^"]*)" MCP server "([^"]*)" parses with its command, args and env intact$`, func(c context.Context, name, server string) error {
		w := worldFrom(c)
		rel := "mcp/" + server + ".yaml"
		got, _, err := j001400RequireConsumerFile(w, rel)
		if err != nil {
			return err
		}
		var cfg struct {
			Command string            `yaml:"command"`
			Args    []string          `yaml:"args"`
			Env     map[string]string `yaml:"env"`
		}
		if uerr := yaml.Unmarshal(got, &cfg); uerr != nil {
			return fmt.Errorf("consumer's %q is not parseable YAML: %w\n%s", rel, uerr, got)
		}
		// Structure, not substring: a flattened or field-dropped round trip
		// still contains every string, and would pass a text match.
		if cfg.Command != "/usr/bin/ledger-mcp" {
			return fmt.Errorf("MCP command did not survive: want %q, got %q", "/usr/bin/ledger-mcp", cfg.Command)
		}
		wantArgs := []string{"--serve", "--marker", "ATELIER-MCP-1f88b0"}
		if len(cfg.Args) != len(wantArgs) {
			return fmt.Errorf("MCP args did not survive: want %v, got %v", wantArgs, cfg.Args)
		}
		for i := range wantArgs {
			if cfg.Args[i] != wantArgs[i] {
				return fmt.Errorf("MCP arg %d did not survive: want %q, got %q (full: %v)", i, wantArgs[i], cfg.Args[i], cfg.Args)
			}
		}
		if cfg.Env["LEDGER_MODE"] != "readonly" {
			return fmt.Errorf("MCP env did not survive: want LEDGER_MODE=readonly, got %v", cfg.Env)
		}
		return nil
	})

	ctx.Step(`^Alice's pulled skill "([^"]*)" contains exactly the files Trent published, byte for byte$`, func(c context.Context, skill string) error {
		w := worldFrom(c)
		st := j001400Of(w)
		prefix := "skills/" + skill + "/"
		var want []string
		for rel := range st.authored {
			if strings.HasPrefix(rel, prefix) {
				want = append(want, rel)
			}
		}
		sort.Strings(want)
		if len(want) < 2 {
			return fmt.Errorf("fixture authored only %d file(s) under %q; a multi-file package assertion needs at least SKILL.md plus a script", len(want), prefix)
		}
		for _, rel := range want {
			got, _, err := j001400RequireConsumerFile(w, rel)
			if err != nil {
				return err
			}
			if string(got) != st.authored[rel].Body {
				return fmt.Errorf("skill file %q differs from what was published\n--- published ---\n%s\n--- received ---\n%s",
					rel, st.authored[rel].Body, got)
			}
		}
		// The other direction: nothing EXTRA may ride along inside the package.
		dir, ok := j001400ConsumerTreePath(w, prefix)
		if !ok {
			return fmt.Errorf("no consumer-side %q directory at all\n%s", prefix, st.j001400PullDiagnostic())
		}
		var extra []string
		_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil //nolint:nilerr
			}
			rel, _ := filepath.Rel(dir, p)
			if _, known := st.authored[prefix+filepath.ToSlash(rel)]; !known {
				extra = append(extra, filepath.ToSlash(rel))
			}
			return nil
		})
		if len(extra) > 0 {
			sort.Strings(extra)
			return fmt.Errorf("the consumer's %q package carries %d file(s) Trent never published: %v", skill, len(extra), extra)
		}
		return nil
	})

	ctx.Step(`^Alice's pulled skill "([^"]*)" file "([^"]*)" is executable$`, func(c context.Context, skill, rel string) error {
		w := worldFrom(c)
		full := "skills/" + skill + "/" + rel
		_, info, err := j001400RequireConsumerFile(w, full)
		if err != nil {
			return err
		}
		if info.Mode()&0o111 == 0 {
			return fmt.Errorf("consumer's %q has mode %v — the exec bit did NOT survive publication, so the model cannot run the script it was shipped",
				full, info.Mode().Perm())
		}
		return nil
	})

	// --- Then: hook bucketing and ordering ----------------------------------

	ctx.Step(`^Alice's pulled "([^"]*)" hooks under "([^"]*)" are exactly "([^"]*)" in that order$`, func(c context.Context, name, event, wantCSV string) error {
		w := worldFrom(c)
		st := j001400Of(w)
		var want []string
		for _, s := range strings.Split(wantCSV, ",") {
			if t := strings.TrimSpace(s); t != "" {
				want = append(want, t)
			}
		}
		got, err := j001400ConsumerHookOrder(w, event)
		if err != nil {
			return err
		}
		if len(got) != len(want) {
			return fmt.Errorf("event %q: want %d hook(s) %v, got %d %v\n%s", event, len(want), want, len(got), got, st.j001400PullDiagnostic())
		}
		for i := range want {
			if got[i] != want[i] {
				return fmt.Errorf("event %q: hook %d is %q, want %q — the DECLARED ORDER did not survive (full sequence: got %v, want %v). "+
					"A name-keyed directory enumerates SORTED, and %v is what sorting produces; the bytes are identical either way, "+
					"which is exactly why this has to be asserted rather than assumed",
					event, i, got[i], want[i], got, want, got)
			}
		}
		return nil
	})

	ctx.Step(`^no pulled "([^"]*)" hook appears under an event it was not declared in$`, func(c context.Context, name string) error {
		w := worldFrom(c)
		declared := j001400DeclaredHookEvents(j001400Of(w).authored)
		if len(declared) == 0 {
			return fmt.Errorf("fixture declared no hooks, so this assertion would be vacuous")
		}
		return j001400CheckHookBuckets(w, declared)
	})

	// --- When/Then: the isolation delivery matrix ---------------------------

	ctx.Step(`^the pulled surfaces are delivered to a "([^"]*)" agent in its "([^"]*)" workspace$`, func(c context.Context, runtime, workspace string) error {
		w := worldFrom(c)
		st := j001400Of(w)
		st.agentRoot, st.agentBackend, st.cellRuntime = "", "", ""
		root, err := j001400WorkspaceRoot(w, runtime, workspace)
		if err != nil {
			// A configuration this suite has no hermetic vehicle for is
			// RECORDED, not raised: the THEN assertions then fail naming the
			// missing delivery rather than the WHEN failing on setup, which is
			// what keeps a red row a specification instead of a stack trace.
			st.runErr = err
			st.runOutput = ""
			return nil
		}
		// The vehicle is `profile materialize`, NOT `ctxloom run`, and that is a
		// decision rather than a convenience: grpc.RunTurn calls Cleanup
		// immediately after Execute, and the shared LIFO reversal strips the
		// delivered managed section — so a step that shells out to `run` and
		// then stats the directory observes nothing, for mock and for every real
		// backend alike. Materialize persists by design and goes through the
		// same backends.BuildSurfaces seam. The live-run half stays covered by
		// tests/integration/delivery_approach_matrix_test.go and grpc's
		// TestRunTurn_MockDeliversContextSurfaceDuringTheTurn.
		//
		// The RUNTIME axis decides WHERE that command executes: on the host, or
		// inside a container against the same path through a bind mount.
		if runtime == "container" {
			return j001400DeliverInContainer(c, w, root)
		}
		if rerr := w.env.Run("profile", "materialize", "default", "--target", root, "--backend", "mock"); rerr != nil || w.env.LastExitCode() != 0 {
			st.runErr = fmt.Errorf("`ctxloom profile materialize default --target %s --backend mock` exited %d", root, w.env.LastExitCode())
		}
		st.runOutput = w.env.LastOutput()
		st.agentRoot = root
		st.agentBackend = "mock"
		return nil
	})

	// OWNERSHIP is the container half's own assertion, and it is on the
	// container outline alone because it is the only axis a process boundary
	// can break while bytes and modes come through untouched. A rootful daemon
	// writing through a bind mount leaves byte-identical, mode-identical,
	// ROOT-OWNED files in the invoking user's tree — undeletable by that user,
	// and invisible to every check this journey previously made.
	ctx.Step(`^"([^"]*)" is owned on the host by the user that ran the delivery$`, func(c context.Context, rel string) error {
		w := worldFrom(c)
		st := j001400Of(w)
		p, err := j001400AgentVisiblePath(w, rel)
		if err != nil {
			return err
		}
		if oerr := containercell.AssertOwnedByInvoker(p, rel); oerr != nil {
			return fmt.Errorf("%w\n%s", oerr, st.j001400RunDiagnostic())
		}
		uid, gid, _ := containercell.Owner(p)
		w.docStepMaterialized = fmt.Sprintf("%s -> uid=%d gid=%d (runtime %s)", p, uid, gid, st.cellRuntime)
		return nil
	})

	ctx.Step(`^the "([^"]*)" reaches that agent's workspace carrying its published bytes$`, func(c context.Context, rel string) error {
		w := worldFrom(c)
		st := j001400Of(w)
		want, ok := st.authored[rel]
		if !ok {
			return fmt.Errorf("the fixture never authored %q", rel)
		}
		p, err := j001400AgentVisiblePath(w, rel)
		if err != nil {
			return err
		}
		got, rerr := os.ReadFile(p)
		if rerr != nil {
			return fmt.Errorf("the agent cannot read %q at %q: %w\n%s", rel, p, rerr, st.j001400RunDiagnostic())
		}
		// A fragment is the one kind that is NOT delivered as a file. It is
		// merged into the engine's single context file alongside every other
		// fragment the profile selects, so "byte for byte" can only mean "its
		// published body, verbatim, inside that file" — whole-file equality
		// would be a claim about the assembler's framing, not about the
		// fragment surviving the trip. Every other kind IS a file and is
		// compared whole.
		if strings.HasPrefix(rel, "fragments/") {
			return j001400AssertContextCarries(rel, want.Body, string(got), p, st)
		}
		if string(got) != want.Body {
			return fmt.Errorf("the agent's copy of %q differs from what was published\n--- published ---\n%s\n--- agent sees ---\n%s",
				rel, want.Body, got)
		}
		return nil
	})

	ctx.Step(`^the mode of "([^"]*)" is "([^"]*)" where that agent can read it$`, func(c context.Context, rel, wantMode string) error {
		w := worldFrom(c)
		st := j001400Of(w)
		p, err := j001400AgentVisiblePath(w, rel)
		if err != nil {
			return err
		}
		info, serr := os.Stat(p)
		if serr != nil {
			return fmt.Errorf("stat agent-visible %q: %w\n%s", rel, serr, st.j001400RunDiagnostic())
		}
		got := fmt.Sprintf("%04o", info.Mode().Perm())
		if got != wantMode {
			return fmt.Errorf("the agent sees %q with mode %s, want %s — the POSIX mode did not survive delivery",
				rel, got, wantMode)
		}
		return nil
	})
}

// j001400MarkerHits lists every file under root whose bytes carry marker, relative
// to root. An absent root yields no hits — that IS the red case each caller
// reports, with the materialize output quoted alongside. An empty marker is a
// fixture error rather than a match against everything.
func j001400MarkerHits(root, marker string) ([]string, error) {
	if marker == "" {
		return nil, fmt.Errorf("fixture error: an empty marker would be found in every file, making this assertion vacuous")
	}
	var hits []string
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // an absent materialize root is the red case
		}
		b, rerr := os.ReadFile(p)
		if rerr == nil && strings.Contains(string(b), marker) {
			rel, _ := filepath.Rel(root, p)
			hits = append(hits, rel)
		}
		return nil
	})
	return hits, nil
}

// j001400RunDiagnostic renders what the delivery did, for quoting.
func (st *j001400State) j001400RunDiagnostic() string {
	if st.runErr != nil {
		return fmt.Sprintf("the delivery FAILED: %v\n--- delivery output ---\n%s", st.runErr, st.runOutput)
	}
	return fmt.Sprintf("the delivery reported success; its output was:\n%s", st.runOutput)
}

// j001400WorkspaceRoot resolves the directory an agent in the named (runtime,
// workspace) configuration reads its surfaces from, or says why this suite
// cannot produce one.
//
//   - host + none     — the live project dir. The baseline.
//   - host + worktree — a DETACHED CHECKOUT of Alice's project outside the
//     project tree, which is the shape isolation.Worktree resolves
//     (worktree.go's worktreeScratchPath puts it under the session's
//     ephemeral/ dir, never under the project). Delivery must follow the
//     workspace there; a surface writer that resolved paths against the
//     PROJECT instead of the target would land nothing here and the row goes
//     red. What this does NOT prove is the RESOLUTION — that a real run points
//     delivery at its own worktree — which is J002200's subject (its mock
//     req.WorkDir record) and the integration matrix's.
//   - container + *   — the SAME directory, reached through a bind mount from
//     inside a container (internal/testsupport/containercell). The workspace
//     axis is unchanged by the runtime axis, which is the composition the docs
//     claim: workspace isolates the FILES, runtime isolates the PROCESS. What
//     changes is WHO writes: a ctxloom process inside the container, whose
//     only route to this path is the mount. Deliver into a container-private
//     path instead and the host sees nothing — which is reason (2) in the
//     feature file's container block, stated as an assertion rather than a
//     worry.
func j001400WorkspaceRoot(w *World, runtime, workspace string) (string, error) {
	switch runtime {
	case "host", "container":
	default:
		return "", fmt.Errorf("this journey knows no runtime axis %q", runtime)
	}
	switch workspace {
	case "none":
		return w.env.ProjectDir, nil
	case "worktree":
		return j001400DetachedCheckout(w)
	default:
		return "", fmt.Errorf("this journey knows no workspace axis %q", workspace)
	}
}

// j001400DetachedCheckout makes (once per scenario) a detached git worktree of
// Alice's project OUTSIDE the project tree, and refuses to return one that is
// nested inside it — a nested checkout would let a delivery that ignored the
// target still be found, which is the whole failure this cell is about.
func j001400DetachedCheckout(w *World) (string, error) {
	path := filepath.Join(w.env.Root, "j001400-agent-worktree")
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	// `git worktree add` needs a commit to detach from; the scaffolded project
	// has files but no commit of its own.
	if err := w.env.GitCommit("j001400: project state before the agent's worktree is cut"); err != nil {
		return "", fmt.Errorf("commit before cutting the agent's worktree: %w", err)
	}
	if err := j001400Git(w.env.ProjectDir, "worktree", "add", "--detach", path); err != nil {
		return "", err
	}
	proj := w.env.ProjectDir + string(filepath.Separator)
	if path == w.env.ProjectDir || strings.HasPrefix(path, proj) {
		return "", fmt.Errorf("fixture error: the agent's worktree %q is inside the project %q, so this cell would assert nothing", path, w.env.ProjectDir)
	}
	return path, nil
}

// j001400FragmentBody strips a fragment's YAML front-matter, leaving the body an
// engine's context file actually carries. Front-matter is metadata ABOUT the
// fragment and is asserted separately by the metadata scenario; it is not part
// of what reaches an assistant.
func j001400FragmentBody(published string) string {
	rest, ok := strings.CutPrefix(published, "---\n")
	if !ok {
		return strings.TrimSpace(published)
	}
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		return strings.TrimSpace(published)
	}
	return strings.TrimSpace(rest[end+len("\n---\n"):])
}

// j001400MinFragmentBody is the shortest published fragment body a "carries it
// verbatim" assertion is allowed to be made about. A one-line body degenerates
// into a marker search, which a delivery that truncated or re-wrapped the
// fragment would still pass — so a fixture that shrank below this fails the
// assertion instead of quietly weakening it.
const j001400MinFragmentBody = 60

// j001400AssertContextCarries checks the engine's context file carries a published
// fragment's body verbatim, and refuses to make the check at all when the
// fixture body is too small for it to mean anything.
func j001400AssertContextCarries(rel, published, delivered, path string, st *j001400State) error {
	body := j001400FragmentBody(published)
	if len(body) < j001400MinFragmentBody {
		return fmt.Errorf("fixture error: %q's published body is %d bytes, below the %d-byte floor a verbatim-containment assertion needs — "+
			"a body this short reduces this row to a marker search", rel, len(body), j001400MinFragmentBody)
	}
	if !strings.Contains(delivered, body) {
		return fmt.Errorf("the agent's context file %q does NOT carry %q's published body verbatim\n--- published body (%d bytes) ---\n%s\n--- the agent's whole context file (%d bytes) ---\n%s\n%s",
			path, rel, len(body), body, len(delivered), delivered, st.j001400RunDiagnostic())
	}
	return nil
}

// j001400AgentVisiblePath resolves where a delivered artifact would have to be for
// the agent in THIS configuration to read it. When the configuration's root
// cannot be determined it fails loudly and names why, rather than falling back
// to the project dir — a fallback would let a container or worktree row pass
// by reading the host's copy, which is precisely the mistake this matrix
// exists to catch.
func j001400AgentVisiblePath(w *World, rel string) (string, error) {
	st := j001400Of(w)
	if st.agentRoot == "" || st.agentBackend == "" {
		return "", fmt.Errorf("cannot locate the root this agent actually reads from: no delivery resolved an observable workspace. %s",
			st.j001400RunDiagnostic())
	}
	// Delivered surfaces land under the ENGINE's own native layout, not at the
	// bundle-relative path: a skill published as skills/reviewer/SKILL.md is
	// delivered to .claude/skills/reviewer/SKILL.md on claude-code and to
	// .mock/skills/reviewer/SKILL.md on mock, and a fragment is not delivered
	// as a file at all — it is merged into the engine's one context file.
	//
	// mock nests its skills under .mock/ for the same reason every real engine
	// nests its own (.claude/skills, .agents/skills, .kiro/skills,
	// .opencode/skill, .codex/skills): a bare top-level skills/ would collide
	// with the skills/ directory of a bundle content tree materialized into the
	// same project.
	var skillsDir []string
	var contextFile string
	switch st.agentBackend {
	case "mock":
		skillsDir, contextFile = []string{".mock", "skills"}, "MOCK_CONTEXT.md"
	case "claude-code":
		skillsDir, contextFile = []string{".claude", "skills"}, "CLAUDE.md"
	default:
		return "", fmt.Errorf("this journey does not know %q's native surface layout", st.agentBackend)
	}
	if delivered, ok := strings.CutPrefix(rel, "skills/"); ok {
		return filepath.Join(append([]string{st.agentRoot}, append(skillsDir, filepath.FromSlash(delivered))...)...), nil
	}
	if strings.HasPrefix(rel, "fragments/") {
		return filepath.Join(st.agentRoot, contextFile), nil
	}
	return "", fmt.Errorf("this journey does not yet know where a %q artifact is delivered for an agent to read", rel)
}

// j001400DeclaredHookEvents maps each authored hook's name to the event it was
// declared under. Only the hook's OWN file counts as a declaration — its
// ".<name>.meta.yaml" order sidecar lives in the same directory and must not
// be mistaken for a second hook.
func j001400DeclaredHookEvents(authored map[string]j001400File) map[string]string {
	declared := map[string]string{}
	for rel := range authored {
		if !strings.HasPrefix(rel, "hooks/") {
			continue
		}
		parts := strings.Split(strings.TrimPrefix(rel, "hooks/"), "/")
		if len(parts) != 2 || strings.HasPrefix(parts[1], ".") {
			continue
		}
		declared[strings.TrimSuffix(parts[1], ".yaml")] = parts[0]
	}
	return declared
}

// j001400OpenPulledBundle opens the consumer's pulled "atelier" tree through the
// SAME read path the product itself uses to turn a tree back into items
// (content.NewTreeStore + Bundle.Refs/Item/Surface — see internal/bundles's
// ReadTree, which internal/bundles/reader_repofs.go's readTreeForm calls for
// every pulled tree), rather than a raw directory walk. That distinction is
// the whole point of this scenario: os.ReadDir yields entries sorted BY NAME,
// which silently agrees with a tree whose hooks are the right bytes in the
// wrong order — the failure this journey exists to catch. Going through the
// content package instead resolves each hook's declared content.Hook.Order
// from its sidecar, the only order carrier the tree format has.
func j001400OpenPulledBundle(w *World) (content.Bundle, error) {
	dir, ok := j001400ConsumerTreePath(w, "")
	if !ok {
		return nil, fmt.Errorf("no %q bundle directory exists anywhere under the consumer's .ctxloom/cache/bundles — "+
			"a directory-form bundle was published but the consumer received no tree.\n%s",
			j001400Bundle, j001400Of(w).j001400PullDiagnostic())
	}
	store, err := content.NewTreeStore(afero.NewOsFs(), filepath.Dir(dir), content.Provenance{IsLocal: true})
	if err != nil {
		return nil, fmt.Errorf("open a tree store over the consumer's pulled bundles at %s: %w", filepath.Dir(dir), err)
	}
	bundle, err := store.Open(context.Background(), content.BundleID(filepath.Base(dir)))
	if err != nil {
		return nil, fmt.Errorf("open the consumer's pulled %q tree: %w", j001400Bundle, err)
	}
	return bundle, nil
}

// j001400ConsumerHooks decodes every hook the consumer's pulled tree holds,
// through Bundle.Item + Item.Surface — i.e. through hookType.Decode, the
// SAME production code that assigns each hook's Event and Name when
// internal/bundles.ReadTree turns a pulled tree into the bundle the product
// actually merges (reader.add's `case content.Hook: r.hooks[v.Event] =
// append(...)` keys purely off the DECODED Event field, not off the ref's
// path string). Deriving bucket membership from the ref path instead would
// only prove the file sits in the right directory, and would stay green even
// if Decode's own "<event>/<name>" split were the thing that broke — which
// is the failure that actually determines which lifecycle event a hook fires
// under once a real profile materializes it.
func j001400ConsumerHooks(w *World) ([]content.Hook, error) {
	bundle, err := j001400OpenPulledBundle(w)
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	refs, err := bundle.Refs(ctx, trust.KindHook)
	if err != nil {
		return nil, fmt.Errorf("list the consumer's hook refs: %w", err)
	}
	if len(refs) == 0 {
		return nil, fmt.Errorf("the consumer received no hooks at all\n%s", j001400Of(w).j001400PullDiagnostic())
	}
	hooks := make([]content.Hook, 0, len(refs))
	for _, ref := range refs {
		item, err := bundle.Item(ctx, ref)
		if err != nil {
			return nil, fmt.Errorf("open consumer hook %q: %w", ref.Name, err)
		}
		surf, err := item.Surface(ctx)
		if err != nil {
			return nil, fmt.Errorf("read consumer hook %q surface: %w", ref.Name, err)
		}
		h, ok := surf.(content.Hook)
		if !ok {
			return nil, fmt.Errorf("consumer hook %q surface is %T, not content.Hook", ref.Name, surf)
		}
		hooks = append(hooks, h)
	}
	return hooks, nil
}

// j001400CheckHookBuckets asserts every hook the consumer received DECODES into
// the event it was declared in, and that none decodes into an event it was
// never declared under. See j001400ConsumerHooks for why this checks the decoded
// content.Hook.Event field rather than the ref's path string.
func j001400CheckHookBuckets(w *World, declared map[string]string) error {
	hooks, err := j001400ConsumerHooks(w)
	if err != nil {
		return err
	}
	for _, h := range hooks {
		want, known := declared[h.Name]
		if !known {
			return fmt.Errorf("hook %q arrived under event %q but was never declared", h.Name, h.Event)
		}
		if want != h.Event {
			return fmt.Errorf("hook %q was declared under event %q but decoded into %q — event bucketing was lost", h.Name, want, h.Event)
		}
	}
	return nil
}

// j001400ConsumerHookOrder returns the hook names the consumer received under one
// event, resolved into EXECUTION order via content.SortHooks over each hook's
// content.Hook.Order — the same resolution the product applies when it reads a
// pulled tree back into a bundle (internal/bundles.ReadTree's finishHooks). A
// raw directory listing would return the same names sorted BY FILENAME, which
// is exactly the wrong-order failure this scenario is written to catch.
func j001400ConsumerHookOrder(w *World, event string) ([]string, error) {
	all, err := j001400ConsumerHooks(w)
	if err != nil {
		return nil, err
	}
	var hooks []content.Hook
	for _, h := range all {
		if h.Event == event {
			hooks = append(hooks, h)
		}
	}
	if len(hooks) == 0 {
		return nil, fmt.Errorf("the consumer received no hooks/%s\n%s", event, j001400Of(w).j001400PullDiagnostic())
	}
	content.SortHooks(hooks)
	out := make([]string, len(hooks))
	for i, h := range hooks {
		out[i] = h.Name
	}
	return out, nil
}

// j001400PlacementFile maps a scenario's placement phrase to the file the metadata
// must live in. The phrase is prose in the feature so the scenario reads as a
// claim about WHERE metadata lives per kind; this is the one place that prose
// is turned back into a path.
func j001400PlacementFile(placement string) (string, error) {
	p := strings.TrimSpace(placement)
	// Both shapes end in a quoted path: `front-matter in "x"` / `the sidecar "y"`.
	i := strings.Index(p, `"`)
	j := strings.LastIndex(p, `"`)
	if i < 0 || j <= i {
		return "", fmt.Errorf("placement phrase %q names no file", placement)
	}
	return p[i+1 : j], nil
}

// j001400ContainerReport prints the container-runtime probe once per suite run.
// The acceptance suite already prints a live-engine availability report on
// every run for the same reason: a run that covered one runtime must be
// distinguishable from one that covered three, and from one that covered none.
var j001400ContainerReport sync.Once

// j001400DeliverInContainer is the container half of the delivery matrix: run
// `ctxloom profile materialize` INSIDE a container, delivering into a
// bind-mounted target, and leave the host-side assertions to the Then steps.
//
// WHAT MAKES THIS NON-VACUOUS, since "assert a host path" is exactly what a
// vacuous version of this row would also do:
//
//   - the delivering process is in the container, and its ONLY route to the
//     target is the bind mount. A ctxloom that resolved the target to a
//     container-private path (the containerConfigOverlay failure mode) writes
//     into the ephemeral layer and this row goes red with an absent file.
//   - the target is checked to be EMPTY of delivered surfaces first, so a
//     leftover from an earlier host step cannot stand in for a container
//     delivery.
//   - the assertions that follow are on bytes, POSIX mode AND ownership.
//
// A missing runtime SKIPS the scenario, naming the runtime and what did not
// run — never a silent pass. Under CTXLOOM_REQUIRE_DOCKER=1 the same condition
// is a failure, and under CTXLOOM_REQUIRE_RUNTIMES a lane can demand a
// specific one; all three outcomes come from dockergate rather than from a
// second copy of the policy here.
func j001400DeliverInContainer(c context.Context, w *World, root string) error {
	st := j001400Of(w)
	rt, decision, msg := containercell.Select(c, "J001400's container delivery-matrix rows")
	j001400ContainerReport.Do(func() { fmt.Println("\n" + containercell.Report(containercell.Detect(c))) })
	switch decision {
	case dockergate.Fail:
		return errors.New(msg)
	case dockergate.Skip:
		fmt.Printf("SKIPPED (J001400 container delivery row): %s\n", msg)
		w.docStepMaterialized = "SKIPPED: " + msg
		return godog.ErrSkip
	case dockergate.Proceed:
	}

	// The anti-vacuity guard. If the engine's context file were already there,
	// every Then below could pass on bytes no container ever wrote.
	if _, err := os.Stat(filepath.Join(root, "MOCK_CONTEXT.md")); err == nil {
		return fmt.Errorf("fixture error: %q already carries MOCK_CONTEXT.md before the container ran, so this row could pass on a host-side leftover", root)
	}

	res, err := rt.Run(c, containercell.Spec{
		// ONE mount, the whole environment root, at its own absolute path: it
		// holds Alice's home (the pulled bundle cache), her project, and the
		// detached worktree checkout the "worktree" rows deliver into. Mounting
		// less would make the run fail for a reason that has nothing to do with
		// the claim.
		Mounts:  []string{w.env.Root},
		WorkDir: w.env.ProjectDir,
		// The cell image is FROM scratch and inherits no environment, so the
		// isolation testenv.isolatedEnv() applies on the host is re-stated here
		// explicitly — the same fake HOME and XDG roots, all under the mount.
		Env: map[string]string{
			"HOME":            w.env.HomeDir,
			"XDG_CONFIG_HOME": filepath.Join(w.env.HomeDir, ".config"),
			"XDG_DATA_HOME":   filepath.Join(w.env.HomeDir, ".local", "share"),
		},
		Args: []string{"profile", "materialize", "default", "--target", root, "--backend", "mock"},
	})
	if err != nil {
		st.runErr = fmt.Errorf("the %s container cell could not run: %w", rt.Name, err)
		st.runOutput = res.Output
		return nil
	}
	if res.ExitCode != 0 {
		st.runErr = fmt.Errorf("in-container `ctxloom profile materialize default --target %s --backend mock` exited %d under %s (argv: %s)",
			root, res.ExitCode, rt.Name, strings.Join(res.Argv, " "))
	}
	st.runOutput = res.Output
	st.agentRoot = root
	st.agentBackend = "mock"
	st.cellRuntime = rt.Name
	w.docStepMaterialized = fmt.Sprintf("ran INSIDE a %s container: %s\n%s", rt.Name, strings.Join(res.Argv, " "), strings.TrimSpace(res.Output))
	return nil
}
