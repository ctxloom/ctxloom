//go:build acceptance

// J20 (j20_bundle_distribution.feature): publishing a directory-form bundle
// carrying EVERY surface kind, and a consumer receiving each kind's payload
// intact — across the two isolation axes.
//
// Every scenario in this file is @wip and RED, on purpose. The feature file
// records why and what untags it; the short version is that a directory-form
// bundle is not fetchable at all (internal/remote/bundle_reader.go's
// fetchAtLockedSHA resolves a ref to ONE file and calls FetchFile on it),
// while skills REQUIRE the directory form (internal/bundles/loader.go:389).
//
// TWO DELIBERATE CHOICES ABOUT HOW IT FAILS:
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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/cucumber/godog"
	"golang.org/x/crypto/ssh"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/tests/integration/testenv"
)

// j20State is this journey's fixture state.
type j20State struct {
	signer    *testenv.TestSigner
	principal string

	// authored is the bundle tree Trent wrote, bundle-relative path -> file.
	// It is the ONLY source of truth for what "as published" means: every
	// consumer-side assertion compares against this map rather than against a
	// second literal, so a fixture typo cannot make an assertion vacuously
	// agree with itself.
	authored map[string]j20File

	url  string // file:// URL of the seeded bare repo
	bare string // the bare repo dir (url minus the file:// prefix)

	pullErr    error  // the pull's error, if any — quoted by later assertions
	pullOutput string // the pull's combined output, likewise
	pulled     bool   // whether the reference+pull step ran at all

	runErr    error  // the isolation-matrix run's error, if any
	runOutput string // that run's output
	agentRoot string // where that run's agent could read files, if it got that far
}

func j20Of(w *World) *j20State {
	if w.j20 == nil {
		w.j20 = &j20State{authored: map[string]j20File{}}
	}
	return w.j20
}

// j20File is one authored file: its exact bytes and its declared POSIX mode.
// The mode is carried explicitly rather than inferred from the path because
// it is an ASSERTED property — a skill script is 0755 because the author said
// so, and the whole point is to check that claim survives the trip.
type j20File struct {
	Body string
	Mode os.FileMode
}

const j20Bundle = "atelier"

// j20BundleRel is where a bundle's tree lives inside a publishing repo — the
// same ".ctxloom/content/bundles/<name>/" layout an authored project uses,
// which is what makes "publish" a copy rather than a transformation.
func j20BundleRel(rel string) string {
	return ".ctxloom/content/bundles/" + j20Bundle + "/" + rel
}

// j20AuthoredTree is the fixture: one artifact of EVERY surface kind a bundle
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
func j20AuthoredTree() map[string]j20File {
	f := func(body string) j20File { return j20File{Body: body, Mode: 0o644} }
	return map[string]j20File{
		"bundle.yaml": f("version: \"1.0.0\"\ndescription: Trent's atelier bundle\n"),

		// fragment — content kind, .md, metadata as front-matter.
		"fragments/house-style.md": f("---\ndescription: ATELIER-FRAGMENT-DESC\n---\n\nATELIER-FRAGMENT-4a91c2\n"),

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
		"hooks/post_file_edit/stamp.yaml": f("type: command\ncommand: echo ATELIER-HOOK-9c02af\n"),
		"hooks/post_file_edit/audit.yaml": f("type: command\ncommand: echo ATELIER-HOOK-audit\n"),
		"hooks/session_start/greet.yaml":  f("type: command\ncommand: echo ATELIER-HOOK-greet\n"),

		// skill — the only MULTI-FILE item and the only one carrying a
		// load-bearing POSIX mode.
		"skills/reviewer/SKILL.md":       f("---\nname: reviewer\ndescription: ATELIER-SKILL-3e77da\n---\n\nATELIER-SKILL-3e77da\n"),
		"skills/reviewer/scripts/run.sh": {Body: "#!/bin/sh\necho ATELIER-SKILL-SCRIPT-8b21ce\n", Mode: 0o755},
		"skills/.reviewer.meta.yaml":     f("description: ATELIER-SKILL-DESC\n"),

		// profile — the sixth kind. Never trust-gated as an item, but still a
		// file in the tree that must arrive intact.
		"profiles/studio.yaml": f("description: ATELIER-PROFILE-6b41fc\nbundles:\n  - atelier\n"),
	}
}

// j20SeedTreeRemote seeds a bare git repo carrying the authored tree WITH ITS
// MODES, and signs the named paths if a signer is given. It mirrors
// testenv.SeedRemote's plumbing but preserves each file's mode, for the reason
// in this file's package doc.
func j20SeedTreeRemote(w *World, st *j20State) error {
	root, err := os.MkdirTemp(w.env.Root, "j20-remote-*")
	if err != nil {
		return fmt.Errorf("create remote root: %w", err)
	}
	bare := filepath.Join(root, "remote.git")
	work := filepath.Join(root, "work")

	for _, a := range [][]string{{"init", "--bare", "-b", "main", bare}, {"init", "-b", "main", work}} {
		if err := j20Git("", a...); err != nil {
			return err
		}
	}
	for _, a := range [][]string{
		{"config", "user.email", "trent@example.com"},
		{"config", "user.name", "Trent"},
		{"config", "commit.gpgsign", "false"},
	} {
		if err := j20Git(work, a...); err != nil {
			return err
		}
	}
	if err := j20WriteTree(work, st.authored); err != nil {
		return err
	}
	for _, a := range [][]string{
		{"add", "-A"},
		{"commit", "-m", "publish atelier tree"},
		{"remote", "add", "origin", bare},
		{"push", "origin", "main"},
	} {
		if err := j20Git(work, a...); err != nil {
			return err
		}
	}
	if err := j20Git(bare, "symbolic-ref", "HEAD", "refs/heads/main"); err != nil {
		return err
	}
	st.bare = bare
	st.url = "file://" + bare
	return nil
}

// j20Git runs one git command, surfacing its combined output on failure so a
// seeding problem is diagnosable rather than a bare exit status.
func j20Git(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %s in %s: %w: %s", strings.Join(args, " "), dir, err, out)
	}
	return nil
}

// j20WriteTree writes the authored bundle tree into a working clone WITH each
// file's declared mode. The explicit Chmod is not redundant: os.WriteFile
// applies a mode only at CREATION, so a rewrite would silently drop the exec
// bit — the same trap internal/shared/agent/packagefiles.go documents on the
// delivery side.
func j20WriteTree(work string, authored map[string]j20File) error {
	for rel, file := range authored {
		full := filepath.Join(work, filepath.FromSlash(j20BundleRel(rel)))
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

// j20ConsumerTreePath is where a PULLED bundle tree would have to land for a
// consumer to use it: under the cache's bundles root. The exact remote-name
// segment is derived from the URL by remote.Reference.LocalRemoteName, so
// rather than reimplementing that derivation this searches the cache for the
// bundle directory — which also means the assertion still reports usefully if
// the pull landed the tree somewhere unexpected instead of nowhere.
func j20ConsumerTreePath(w *World, rel string) (string, bool) {
	cacheRoot := filepath.Join(w.env.ProjectDir, ".ctxloom", "cache", "bundles")
	var found string
	_ = filepath.WalkDir(cacheRoot, func(p string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() || found != "" {
			return nil //nolint:nilerr // a missing cache is the ordinary red case, not an error to surface here
		}
		if d.Name() == j20Bundle {
			found = p
		}
		return nil
	})
	if found == "" {
		return "", false
	}
	return filepath.Join(found, filepath.FromSlash(rel)), true
}

// j20PullDiagnostic renders what the pull actually did, for quoting inside an
// assertion failure. This is what makes a red run diagnosable at a glance
// rather than only "file not found".
func (st *j20State) j20PullDiagnostic() string {
	if !st.pulled {
		return "the reference+pull step never ran"
	}
	if st.pullErr != nil {
		return fmt.Sprintf("the pull FAILED: %v\n--- pull output ---\n%s", st.pullErr, st.pullOutput)
	}
	return fmt.Sprintf("the pull reported success; its output was:\n%s", st.pullOutput)
}

// j20RequireConsumerFile reads one file from the consumer's pulled tree, or
// returns the diagnostic-bearing failure the scenario exists to produce.
func j20RequireConsumerFile(w *World, rel string) ([]byte, os.FileInfo, error) {
	st := j20Of(w)
	p, ok := j20ConsumerTreePath(w, rel)
	if !ok {
		return nil, nil, fmt.Errorf("no %q bundle DIRECTORY exists anywhere under the consumer's .ctxloom/cache/bundles — "+
			"a directory-form bundle was published but the consumer received no tree.\n%s",
			j20Bundle, st.j20PullDiagnostic())
	}
	body, err := os.ReadFile(p)
	if err != nil {
		return nil, nil, fmt.Errorf("the consumer's %q tree exists but %q is missing from it: %w\n%s",
			j20Bundle, rel, err, st.j20PullDiagnostic())
	}
	info, err := os.Stat(p)
	if err != nil {
		return nil, nil, fmt.Errorf("stat consumer %q: %w", rel, err)
	}
	return body, info, nil
}

func registerJ20Steps(ctx *godog.ScenarioContext) {
	// --- Given: authoring + trust ------------------------------------------

	ctx.Step(`^Trent authors a directory-form bundle "([^"]*)" carrying every surface kind$`, func(c context.Context, name string) error {
		w := worldFrom(c)
		st := j20Of(w)
		if name != j20Bundle {
			return fmt.Errorf("this journey's fixture publishes %q, not %q", j20Bundle, name)
		}
		if err := ensureProjectWithEngine(w, "claude-code", "claude-code"); err != nil {
			return err
		}
		signer, err := testenv.GenerateTestSigner()
		if err != nil {
			return fmt.Errorf("generate Trent's publishing key: %w", err)
		}
		st.signer = signer
		st.authored = j20AuthoredTree()
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
		st := j20Of(w)
		if st.signer == nil {
			return fmt.Errorf("Trent has not authored anything yet, so there is no key to trust")
		}
		st.principal = "trent@example.com"
		keyPath := filepath.Join(w.env.Root, "j20-trent.pub")
		if err := os.WriteFile(keyPath, ssh.MarshalAuthorizedKey(st.signer.Public), 0o644); err != nil {
			return fmt.Errorf("write Trent's public key: %w", err)
		}
		return runOK(w, "trust", "signer", "create", st.principal, "--key", keyPath, "--project")
	})

	// --- Given/When: publication and consumption ----------------------------

	ctx.Step(`^Trent publishes the "([^"]*)" tree to his company repo, signed with the company key$`, func(c context.Context, name string) error {
		w := worldFrom(c)
		st := j20Of(w)
		if name != j20Bundle {
			return fmt.Errorf("this journey publishes %q, not %q", j20Bundle, name)
		}
		return j20SeedTreeRemote(w, st)
	})

	// Deliberately NOT runOK — see this file's package doc. The outcome is
	// recorded so the payload assertions can quote it.
	ctx.Step(`^Alice references the company's "([^"]*)" bundle and pulls it$`, func(c context.Context, name string) error {
		w := worldFrom(c)
		st := j20Of(w)
		if st.url == "" {
			return fmt.Errorf("Trent has not published %q yet", name)
		}
		if err := runOK(w, "remote", "create", "company", st.url, "--forge", "git"); err != nil {
			return err
		}
		// A profile needs at least one bundle or parent at creation time, so
		// the consumer gets a local seed bundle first and the published one is
		// ADDED to the profile below. Same shape J18 uses. Doing it in one
		// step would make the profile's creation depend on the very fetch this
		// scenario is testing, and the failure would then read as "could not
		// create a profile" rather than "the published tree never arrived".
		// ensureProjectWithEngine may already have seeded this bundle; creating
		// it again is a hard error, so create only when absent.
		if !w.env.FileExists(".ctxloom/content/bundles/seed.yaml") {
			if err := runOK(w, "bundle", "create", "seed", "-d", "J20 consumer seed bundle"); err != nil {
				return err
			}
		}
		if !w.env.FileExists(".ctxloom/profiles/default.yaml") {
			if err := runOK(w, "profile", "create", "default", "-b", "seed", "-d", "J20 consumer profile"); err != nil {
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
		st := j20Of(w)
		want, ok := st.authored[rel]
		if !ok {
			return fmt.Errorf("the fixture never authored %q, so there is nothing to compare against", rel)
		}
		got, _, err := j20RequireConsumerFile(w, rel)
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
		st := j20Of(w)
		// Materialize the consumer's profile and search every delivered
		// surface — the assistant-visible payload, not the cache.
		_ = w.env.Run("profile", "materialize", "default", "--target", "out", "--backend", "claude-code")
		matOut := w.env.LastOutput()
		root := filepath.Join(w.env.ProjectDir, "out")
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
		if len(hits) == 0 {
			return fmt.Errorf("the %s marker %q reached NOTHING the assistant can read under out/ — "+
				"the published %s never arrived.\n%s\n--- materialize said ---\n%s",
				kind, marker, kind, st.j20PullDiagnostic(), matOut)
		}
		return nil
	})

	ctx.Step(`^Alice's pulled "([^"]*)" tree stores the "([^"]*)" metadata "([^"]*)" as (.+)$`, func(c context.Context, name, kind, probe, placement string) error {
		w := worldFrom(c)
		// The placement phrase names the FILE the metadata must live in; the
		// distinction the scenario is making is front-matter (inside the .md)
		// versus a separate sidecar file, so the assertion is simply "this
		// probe is in THAT file" — with the file chosen per kind.
		rel, err := j20PlacementFile(placement)
		if err != nil {
			return err
		}
		got, _, err := j20RequireConsumerFile(w, rel)
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
		got, _, err := j20RequireConsumerFile(w, rel)
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
		st := j20Of(w)
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
			got, _, err := j20RequireConsumerFile(w, rel)
			if err != nil {
				return err
			}
			if string(got) != st.authored[rel].Body {
				return fmt.Errorf("skill file %q differs from what was published\n--- published ---\n%s\n--- received ---\n%s",
					rel, st.authored[rel].Body, got)
			}
		}
		// The other direction: nothing EXTRA may ride along inside the package.
		dir, ok := j20ConsumerTreePath(w, prefix)
		if !ok {
			return fmt.Errorf("no consumer-side %q directory at all\n%s", prefix, st.j20PullDiagnostic())
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
		_, info, err := j20RequireConsumerFile(w, full)
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
		st := j20Of(w)
		var want []string
		for _, s := range strings.Split(wantCSV, ",") {
			if t := strings.TrimSpace(s); t != "" {
				want = append(want, t)
			}
		}
		got, err := j20ConsumerHookOrder(w, event)
		if err != nil {
			return err
		}
		if len(got) != len(want) {
			return fmt.Errorf("event %q: want %d hook(s) %v, got %d %v\n%s", event, len(want), want, len(got), got, st.j20PullDiagnostic())
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
		declared := j20DeclaredHookEvents(j20Of(w).authored)
		if len(declared) == 0 {
			return fmt.Errorf("fixture declared no hooks, so this assertion would be vacuous")
		}
		return j20CheckHookBuckets(w, declared)
	})

	// --- When/Then: the isolation delivery matrix ---------------------------

	ctx.Step(`^Alice runs an agent with runtime "([^"]*)" and workspace "([^"]*)"$`, func(c context.Context, runtime, workspace string) error {
		w := worldFrom(c)
		st := j20Of(w)
		args := []string{"run", "--workspace", workspace, "--print", "j20-delivery-check"}
		if runtime == "container" {
			args = append(args, "--runtime", "container")
		}
		if rerr := w.env.Run(args...); rerr != nil || w.env.LastExitCode() != 0 {
			st.runErr = fmt.Errorf("`ctxloom %s` exited %d", strings.Join(args, " "), w.env.LastExitCode())
		}
		st.runOutput = w.env.LastOutput()
		// Where the agent could read files depends on the workspace axis: for
		// "none" that is the project dir; for "worktree" it is a detached
		// checkout the run resolves and this suite cannot name in advance.
		// Leaving it empty when it cannot be determined is deliberate — the
		// assertion then reports "could not locate", never a silent pass.
		if workspace == "none" {
			st.agentRoot = w.env.ProjectDir
		} else {
			st.agentRoot = ""
		}
		return nil
	})

	ctx.Step(`^the "([^"]*)" is readable by that agent, byte for byte as published$`, func(c context.Context, rel string) error {
		w := worldFrom(c)
		st := j20Of(w)
		want, ok := st.authored[rel]
		if !ok {
			return fmt.Errorf("the fixture never authored %q", rel)
		}
		p, err := j20AgentVisiblePath(w, rel)
		if err != nil {
			return err
		}
		got, rerr := os.ReadFile(p)
		if rerr != nil {
			return fmt.Errorf("the agent cannot read %q at %q: %w\n%s", rel, p, rerr, st.j20RunDiagnostic())
		}
		if string(got) != want.Body {
			return fmt.Errorf("the agent's copy of %q differs from what was published\n--- published ---\n%s\n--- agent sees ---\n%s",
				rel, want.Body, got)
		}
		return nil
	})

	ctx.Step(`^the mode of "([^"]*)" is "([^"]*)" where that agent can read it$`, func(c context.Context, rel, wantMode string) error {
		w := worldFrom(c)
		st := j20Of(w)
		p, err := j20AgentVisiblePath(w, rel)
		if err != nil {
			return err
		}
		info, serr := os.Stat(p)
		if serr != nil {
			return fmt.Errorf("stat agent-visible %q: %w\n%s", rel, serr, st.j20RunDiagnostic())
		}
		got := fmt.Sprintf("%04o", info.Mode().Perm())
		if got != wantMode {
			return fmt.Errorf("the agent sees %q with mode %s, want %s — the POSIX mode did not survive delivery",
				rel, got, wantMode)
		}
		return nil
	})
}

// j20RunDiagnostic renders what the isolation run did, for quoting.
func (st *j20State) j20RunDiagnostic() string {
	if st.runErr != nil {
		return fmt.Sprintf("the agent run FAILED: %v\n--- run output ---\n%s", st.runErr, st.runOutput)
	}
	return fmt.Sprintf("the agent run reported success; its output was:\n%s", st.runOutput)
}

// j20AgentVisiblePath resolves where a delivered artifact would have to be for
// the agent in THIS configuration to read it. When the configuration's root
// cannot be determined it fails loudly and names why, rather than falling back
// to the project dir — a fallback would let a container or worktree row pass
// by reading the host's copy, which is precisely the mistake this matrix
// exists to catch.
func j20AgentVisiblePath(w *World, rel string) (string, error) {
	st := j20Of(w)
	if st.agentRoot == "" {
		return "", fmt.Errorf("cannot locate the root this agent actually reads from: the run resolved no observable workspace. %s\n"+
			"(For workspace=worktree the checkout lives under the session's ephemeral/ dir and this suite has no hermetic vehicle "+
			"that both isolates a run and materializes surfaces — the mock backend materializes nothing at all.)",
			st.j20RunDiagnostic())
	}
	// Delivered surfaces land under the engine's own native directory, not at
	// the bundle-relative path — a skill at skills/reviewer/SKILL.md is
	// delivered to .claude/skills/reviewer/SKILL.md.
	if delivered, ok := strings.CutPrefix(rel, "skills/"); ok {
		return filepath.Join(st.agentRoot, ".claude", "skills", filepath.FromSlash(delivered)), nil
	}
	if strings.HasPrefix(rel, "fragments/") {
		return filepath.Join(st.agentRoot, "CLAUDE.md"), nil
	}
	return "", fmt.Errorf("this journey does not yet know where a %q artifact is delivered for an agent to read", rel)
}

// j20DeclaredHookEvents maps each authored hook's name to the event it was
// declared under.
func j20DeclaredHookEvents(authored map[string]j20File) map[string]string {
	declared := map[string]string{}
	for rel := range authored {
		if !strings.HasPrefix(rel, "hooks/") {
			continue
		}
		parts := strings.Split(strings.TrimPrefix(rel, "hooks/"), "/")
		if len(parts) != 2 {
			continue
		}
		declared[strings.TrimSuffix(parts[1], ".yaml")] = parts[0]
	}
	return declared
}

// j20CheckHookBuckets asserts every hook the consumer received sits under the
// event it was declared in, and that none arrived that was never declared.
func j20CheckHookBuckets(w *World, declared map[string]string) error {
	dir, ok := j20ConsumerTreePath(w, "hooks")
	if !ok {
		return fmt.Errorf("the consumer received no hooks/ directory at all\n%s", j20Of(w).j20PullDiagnostic())
	}
	events, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read consumer hooks dir: %w", err)
	}
	for _, ev := range events {
		if !ev.IsDir() {
			continue
		}
		if err := j20CheckOneEvent(filepath.Join(dir, ev.Name()), ev.Name(), declared); err != nil {
			return err
		}
	}
	return nil
}

func j20CheckOneEvent(dir, event string, declared map[string]string) error {
	hooks, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read consumer hook event %q: %w", event, err)
	}
	for _, h := range hooks {
		name := strings.TrimSuffix(h.Name(), ".yaml")
		want, known := declared[name]
		if !known {
			return fmt.Errorf("hook %q arrived under event %q but was never declared", name, event)
		}
		if want != event {
			return fmt.Errorf("hook %q was declared under event %q but arrived under %q — event bucketing was lost", name, want, event)
		}
	}
	return nil
}

// j20ConsumerHookOrder returns the hook names the consumer received under one
// event, in the order the consumer's own layout yields them.
func j20ConsumerHookOrder(w *World, event string) ([]string, error) {
	dir, ok := j20ConsumerTreePath(w, "hooks/"+event)
	if !ok {
		return nil, fmt.Errorf("the consumer received no hooks/%s directory\n%s", event, j20Of(w).j20PullDiagnostic())
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read consumer hooks/%s: %w", event, err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".yaml") {
			out = append(out, strings.TrimSuffix(e.Name(), ".yaml"))
		}
	}
	return out, nil
}

// j20PlacementFile maps a scenario's placement phrase to the file the metadata
// must live in. The phrase is prose in the feature so the scenario reads as a
// claim about WHERE metadata lives per kind; this is the one place that prose
// is turned back into a path.
func j20PlacementFile(placement string) (string, error) {
	p := strings.TrimSpace(placement)
	// Both shapes end in a quoted path: `front-matter in "x"` / `the sidecar "y"`.
	i := strings.Index(p, `"`)
	j := strings.LastIndex(p, `"`)
	if i < 0 || j <= i {
		return "", fmt.Errorf("placement phrase %q names no file", placement)
	}
	return p[i+1 : j], nil
}
