//go:build acceptance

// J000800: "a new engineer clones the repo and is already set up" journey
// (j000800_onboarding.feature). Bob is a new teammate; Carol's checkout
// (w.env.ProjectDir) plays the team's shared project exactly as it does in
// steps_j000700_team.go, whose two-checkout plumbing (bare origin + Bob's own
// clone directory, driven via exec.Cmd against the ctxloom binary) this file
// reuses DIRECTLY — j000700SetupTeamProject/j000700CommitAndPush/j000700BobPull/runBob/
// readBobFile all operate on the same generic "team origin + Bob's clone"
// state (w.j000700()), which is not actually J000700-specific despite the field's name.
// Reusing it here (rather than re-deriving a second copy of the same git
// plumbing) is the point of the worktree brief's "reuse the existing harness"
// instruction. World carries exactly one new field for this journey's OWN
// bookkeeping (j000800s *j000800State — signers/URLs/markers that cross step
// boundaries), mirroring J000700/J001500's one-field convention.
package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"
	"golang.org/x/crypto/ssh"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/tests/integration/testenv"
)

// Distinctive marker strings this journey's bundles carry, so a materialized
// context can be checked for exactly the right payload (ASSERTION
// DISCIPLINE) rather than a bare exit-code or file-exists proxy.
const (
	j000800TeamMarker    = "J000800-TEAM-STANDARDIZED-CONTEXT-MARKER"
	j000800PinnedMarker  = "J000800-PINNED-UPSTREAM-VERSION-MARKER"
	j000800NewerMarker   = "J000800-NEWER-UPSTREAM-VERSION-MARKER"
	j000800CompanyMarker = "J000800-TRENT-COMPANY-CONTENT-MARKER"
	j000800RepriseMarker = "J000800-REPRISE-DEPENDENT-GUIDANCE-MARKER"

	j000800UpstreamPrincipal = "upstream@example.com"
	j000800CompanyPrincipal  = "trent@example.com"
	j000800ReprisePrincipal  = "reprise@example.com"

	j000800UpstreamBundle = "upstream-bundle"
	j000800CompanyBundle  = "secure-standards"
)

// j000800State is this journey's fixture state: the signers/URLs/bundle names its
// Given steps seed, needed later by a companion step in the SAME scenario
// (e.g. re-signing an advanced remote, or installing the fake companion with
// the envelope a prior step built).
type j000800State struct {
	upstreamSigner *testenv.TestSigner
	upstreamBare   string

	companySigner *testenv.TestSigner

	repriseEnvelope    string
	repriseVersionJSON string

	// pinnedSHA is the commit the team's lockfile froze when Carol held the
	// upstream bundle — the concrete thing "the versions the team pinned"
	// means. Read off the committed lock.yaml at pin time so Bob's own
	// lockfile can be held to it afterwards.
	pinnedSHA string

	// engine/target: this scenario's current Examples row (the multi-engine
	// outline below) — which backend Bob materialized for, and the
	// --target dir his materialize wrote into.
	engine string
	target string
}

// j000800LockedSHA reads the lockfile at rel (project-relative) and returns the
// locked commit SHA for the upstream bundle entry. YAML is parsed rather than
// grepped so a reshaped lockfile fails loudly instead of silently matching
// nothing.
func j000800LockedSHA(raw string) (string, error) {
	var lock struct {
		Bundles map[string]struct {
			SHA    string `yaml:"sha"`
			Pinned bool   `yaml:"pinned"`
		} `yaml:"bundles"`
	}
	if err := yaml.Unmarshal([]byte(raw), &lock); err != nil {
		return "", fmt.Errorf("parse lockfile: %w\n%s", err, raw)
	}
	for ref, entry := range lock.Bundles {
		if strings.Contains(ref, j000800UpstreamBundle) {
			if entry.SHA == "" {
				return "", fmt.Errorf("lockfile entry %q carries no sha:\n%s", ref, raw)
			}
			return entry.SHA, nil
		}
	}
	return "", fmt.Errorf("lockfile has no entry for the upstream bundle %q:\n%s", j000800UpstreamBundle, raw)
}

// j000800 returns (lazily allocating) this scenario's J000800 fixture state.
func (w *World) j000800() *j000800State {
	if w.j000800s == nil {
		w.j000800s = &j000800State{}
	}
	return w.j000800s
}

func registerJ000800Steps(ctx *godog.ScenarioContext) {
	// --- Background -----------------------------------------------------

	ctx.Step(`^the team's project carries the context Carol has standardized on$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := j000700SetupTeamProject(w); err != nil {
			return err
		}
		// Team's own first-party fragment (LOCAL — authored straight into the
		// project bundle, so it is allowed unconditionally, no signing/trust
		// needed — see internal/operations/trust.go's EffectiveTrust step 3).
		if err := w.env.WriteFile(".ctxloom/content/bundles/"+j000700Bundle+".yaml", j000700FragmentBundleYAML(j000800TeamMarker)); err != nil {
			return err
		}
		// Later scenarios (2/5) have Carol run `deps pull`, which populates a
		// local git-clone cache under .ctxloom/cache/ — exclude it so a `git add
		// -A` commit never sweeps a nested git repo into the team's history.
		if err := w.env.WriteFile(".gitignore", ".ctxloom/cache/\n"); err != nil {
			return err
		}
		return j000700CommitAndPush(w, "author team standards")
	})

	ctx.Step(`^Bob has just joined and has never used ctxloom before$`, func(c context.Context) error {
		// Narrative only: Bob's clone directory does not exist until his own
		// "Bob clones the project" step below.
		return nil
	})

	// --- Shared clone/session steps (every scenario uses these) ----------

	ctx.Step(`^Bob clones the project$`, func(c context.Context) error {
		return j000700BobPull(worldFrom(c))
	})

	ctx.Step(`^Bob fetches the context the project draws on$`, func(c context.Context) error {
		return runBob(worldFrom(c), "deps", "pull")
	})

	ctx.Step(`^Bob starts a session$`, func(c context.Context) error {
		return runBob(worldFrom(c), "profile", "materialize", j000700Profile, "--target", "out")
	})

	// --- Multi-engine outline: Bob's engine may not be Alice's ------------
	//
	// Reuses J000400's engine-axis machinery (engineContextRelPath, steps_j000400.go)
	// rather than re-deriving a second per-engine path table — the same engines
	// J000400's materialization outline proved (claude-code, kiro, codex). New
	// step text throughout: "Bob starts a session" above
	// already has a different meaning (materialize with NO --backend, into
	// "out"), so reusing it verbatim for a --backend-qualified materialize
	// would be wrong, not just ambiguous.

	ctx.Step(`^Bob starts a session on (\S+)$`, func(c context.Context, engine string) error {
		w := worldFrom(c)
		j000800 := w.j000800()
		j000800.engine = engine
		j000800.target = "out-" + engine
		return runBob(w, "profile", "materialize", j000700Profile, "--target", j000800.target, "--backend", engine)
	})

	ctx.Step(`^his assistant receives the team's standardized context in (\S+)'s own native surface$`, func(c context.Context, engine string) error {
		w := worldFrom(c)
		j000800 := w.j000800()
		rel, err := engineContextRelPath(j000800.target, engine)
		if err != nil {
			return err
		}
		body, err := readBobFile(w, rel)
		if err != nil {
			return fmt.Errorf("read Bob's materialized %s context (%s): %w", engine, rel, err)
		}
		// Real evidence for the @doc capture sidecar (set-and-consume; no-op
		// when capture is off) — the actual bytes Bob's own engine-native
		// surface carries, not a restatement of the assertion.
		w.docStepMaterialized = fmt.Sprintf("%s:\n%s", rel, j000400Excerpt(body, j000800TeamMarker, 1))
		if !strings.Contains(body, j000800TeamMarker) {
			return fmt.Errorf("Bob's materialized %s context does not contain the team's standardized-context marker; content:\n%s", engine, body)
		}
		return nil
	})

	// --- Scenario 1: cloning alone suffices -------------------------------

	ctx.Step(`^his assistant receives the team's standardized context$`, func(c context.Context) error {
		return assertBobMaterializedContains(worldFrom(c), j000800TeamMarker)
	})

	ctx.Step(`^he was not asked to configure anything to get it$`, func(c context.Context) error {
		w := worldFrom(c)
		// The only way this harness could have forced a configuration/review
		// step on Bob is an item landing pending — first-party team content
		// never does (see EffectiveTrust step 3, LOCAL). Mirrors
		// steps_j000700_team.go's "it reached him without any review" check.
		//
		// Asserts the structured total from --format json rather
		// than grepping for the exact prose sentence "Nothing is pending
		// review." (a wording change would silently break the assertion into
		// a false red, or a false green if the new wording still contained
		// that substring).
		if err := runBob(w, "review", "--list", "--format", "json"); err != nil {
			return err
		}
		out := w.j000700().bobOutput
		var res struct {
			Total int `json:"total"`
		}
		if err := json.Unmarshal([]byte(out), &res); err != nil {
			return fmt.Errorf("review --list --format json: not valid JSON: %w (output:\n%s)", err, out)
		}
		if res.Total != 0 {
			return fmt.Errorf("expected nothing to require Bob's review/configuration (total=0), got total=%d; `review --list --format json` output:\n%s", res.Total, out)
		}
		return nil
	})

	// --- Scenario 2: pinned, not latest -----------------------------------

	ctx.Step(`^the project pins the versions of the context it draws from elsewhere$`, func(c context.Context) error {
		w := worldFrom(c)
		j000800 := w.j000800()
		signer, err := testenv.GenerateTestSigner()
		if err != nil {
			return fmt.Errorf("generate upstream signer: %w", err)
		}
		j000800.upstreamSigner = signer
		rel := ".ctxloom/content/bundles/" + j000800UpstreamBundle + ".yaml"
		url, err := w.env.SeedSignedRemote(map[string]string{rel: j000700FragmentBundleYAML(j000800PinnedMarker)}, []string{rel}, signer)
		if err != nil {
			return fmt.Errorf("seed signed upstream remote: %w", err)
		}
		j000800.upstreamBare = strings.TrimPrefix(url, "file://")
		// Trust is PROJECT-scoped (committed .ctxloom/allowed_signers) so Bob
		// inherits it by cloning — exactly like J000200/J001500's own signed sources.
		if err := w.env.TrustSigner(signer, j000800UpstreamPrincipal, true); err != nil {
			return fmt.Errorf("trust upstream signer: %w", err)
		}
		if err := runOK(w, "remote", "create", "upstream", url, "--forge", "git"); err != nil {
			return err
		}
		if err := runOK(w, "profile", "modify", j000700Profile, "--add-bundle", "upstream/"+j000800UpstreamBundle); err != nil {
			return err
		}
		if err := runOK(w, "deps", "pull"); err != nil {
			return err
		}
		// The explicit pin: `deps hold` freezes the lockfile entry's SHA/
		// version so a later pull (even a FRESH one with no local cache, e.g.
		// Bob's) restores this exact content instead of drifting to whatever
		// the upstream branch's tip has moved to (internal/remote/pull.go's
		// updateLockfile: hadExisting && existing.Pinned restores the old
		// SHA/version regardless of what this pull just resolved/fetched).
		if err := runOK(w, "deps", "hold", "upstream/"+j000800UpstreamBundle); err != nil {
			return err
		}
		// Record the SHA the hold froze. "The versions the team pinned" is a
		// concrete commit, and Bob's lockfile after his own pull is the place
		// that claim is either kept or broken; asserting on it is what stops
		// this scenario from being satisfiable by a pull that never resolved
		// anything.
		raw, err := w.env.ReadFile(".ctxloom/lock.yaml")
		if err != nil {
			return fmt.Errorf("read the team's lockfile after holding %s: %w", j000800UpstreamBundle, err)
		}
		sha, err := j000800LockedSHA(raw)
		if err != nil {
			return err
		}
		j000800.pinnedSHA = sha
		return j000700CommitAndPush(w, "pin upstream dependency")
	})

	ctx.Step(`^an upstream has since published a newer version$`, func(c context.Context) error {
		w := worldFrom(c)
		j000800 := w.j000800()
		rel := ".ctxloom/content/bundles/" + j000800UpstreamBundle + ".yaml"
		return w.env.AdvanceSignedRemote(j000800.upstreamBare, map[string]string{rel: j000700FragmentBundleYAML(j000800NewerMarker)}, []string{rel}, j000800.upstreamSigner)
	})

	// THE PULL IS FORCED, and that is the entire point of this step existing
	// separately from the ordinary "Bob fetches the context the project draws
	// on" the other scenarios share.
	//
	// An audit isolated what was actually protecting Bob here, and it was not
	// the pin. Deleting the pin-restore block from remote.Puller.updateLockfile
	// — so a hold no longer freezes SHA or version at all — left all eleven j000800
	// scenarios green. Three controlled runs found why: an ordinary pull sees
	// the bundle already installed (operations.syncItem's !force &&
	// isInstalled skip) and returns "skipped" without ever re-resolving, so the
	// freeze was never on Bob's critical path. The scenario was satisfied by
	// the pull not running. Only with the skip disabled AND the pin removed did
	// it go red.
	//
	// `--force` puts the pull back on the path the pin actually guards: the
	// reference IS re-resolved against the advanced upstream, and the hold is
	// what has to hold it back.
	ctx.Step(`^Bob fetches the context the project draws on, re-resolving every reference$`, func(c context.Context) error {
		return runBob(worldFrom(c), "deps", "pull", "--force")
	})

	ctx.Step(`^he receives the pinned versions, the same ones the rest of the team has$`, func(c context.Context) error {
		w := worldFrom(c)
		j000800 := w.j000800()
		if j000800.pinnedSHA == "" {
			return fmt.Errorf("j000800: no pinned SHA recorded — the pinning step must run before this one")
		}
		raw, err := readBobFile(w, filepath.Join(".ctxloom", "lock.yaml"))
		if err != nil {
			return fmt.Errorf("read Bob's lockfile after his pull: %w", err)
		}
		got, err := j000800LockedSHA(raw)
		if err != nil {
			return fmt.Errorf("Bob's lockfile: %w", err)
		}
		if got != j000800.pinnedSHA {
			return fmt.Errorf("Bob's pull moved the upstream bundle off the team's pin: lockfile now records sha %q, the team pinned %q — a hold must freeze the resolved commit even when the pull re-resolves against a newer upstream; Bob's lockfile:\n%s", got, j000800.pinnedSHA, raw)
		}
		return assertBobMaterializedContains(w, j000800PinnedMarker)
	})

	ctx.Step(`^he does not receive the newer upstream version$`, func(c context.Context) error {
		return assertBobMaterializedDoesNotContain(worldFrom(c), j000800NewerMarker)
	})

	// --- Scenarios 3 & 4: the trust gate survives onboarding --------------

	ctx.Step(`^the project references a bundle published by Trent's company$`, func(c context.Context) error {
		w := worldFrom(c)
		j000800 := w.j000800()
		signer, err := testenv.GenerateTestSigner()
		if err != nil {
			return fmt.Errorf("generate company signer: %w", err)
		}
		j000800.companySigner = signer
		rel := ".ctxloom/content/bundles/" + j000800CompanyBundle + ".yaml"
		url, err := w.env.SeedSignedRemote(map[string]string{rel: j000700FragmentBundleYAML(j000800CompanyMarker)}, []string{rel}, signer)
		if err != nil {
			return fmt.Errorf("seed signed company remote: %w", err)
		}
		if err := runOK(w, "remote", "create", "trent-company", url, "--forge", "git"); err != nil {
			return err
		}
		if err := runOK(w, "profile", "modify", j000700Profile, "--add-bundle", "trent-company/"+j000800CompanyBundle); err != nil {
			return err
		}
		// Deliberately does NOT pull here: the reference is committed
		// unresolved, and either scenario's own "Bob fetches..." step performs
		// the first-ever pull, mirroring how a bundle reference genuinely
		// arrives at a team's project (added, committed, later pulled).
		return j000700CommitAndPush(w, "reference trent's company bundle")
	})

	ctx.Step(`^Bob has not trusted the company key$`, func(c context.Context) error {
		return nil // default state: nothing to arrange
	})

	ctx.Step(`^the company's content is held for his review$`, func(c context.Context) error {
		w := worldFrom(c)
		body, err := bobMaterialized(w)
		if err != nil {
			return err
		}
		if !strings.Contains(w.j000700().bobOutput, "awaiting review") {
			return fmt.Errorf("expected Bob to be told the company's content is awaiting review; materialize output:\n%s", w.j000700().bobOutput)
		}
		if strings.Contains(body, j000800CompanyMarker) {
			return fmt.Errorf("Bob's materialized context unexpectedly contains the held company marker; content:\n%s", body)
		}
		return nil
	})

	ctx.Step(`^his assistant still receives the team's own context, because the project is first-party$`, func(c context.Context) error {
		return assertBobMaterializedContains(worldFrom(c), j000800TeamMarker)
	})

	ctx.Step(`^Bob has cloned the project and the company's content is held for his review$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := j000700BobPull(w); err != nil {
			return err
		}
		if err := runBob(w, "deps", "pull"); err != nil {
			return err
		}
		return assertBobMaterializedDoesNotContain(w, j000800CompanyMarker)
	})

	ctx.Step(`^Bob trusts the company key$`, func(c context.Context) error {
		w := worldFrom(c)
		j000800 := w.j000800()
		pubBytes := ssh.MarshalAuthorizedKey(j000800.companySigner.Public)
		keyPath := filepath.Join(w.env.Root, "company-signer.pub")
		if err := os.WriteFile(keyPath, pubBytes, 0o644); err != nil {
			return fmt.Errorf("write company public key: %w", err)
		}
		// Bob's OWN project-scoped trust decision, in his own checkout — the
		// gate opening the ordinary way, exactly like J001500's "Alice trusts the
		// company key".
		return runBob(w, "signer", "trust", j000800CompanyPrincipal, "--key", keyPath, "--project")
	})

	ctx.Step(`^his assistant receives the company's content$`, func(c context.Context) error {
		return assertBobMaterializedContains(worldFrom(c), j000800CompanyMarker)
	})

	// --- Scenario 5: companion-dependent guidance -------------------------

	ctx.Step(`^the team's context includes guidance for the "reprise" companion$`, func(c context.Context) error {
		w := worldFrom(c)
		j000800 := w.j000800()
		signer, err := testenv.GenerateTestSigner()
		if err != nil {
			return fmt.Errorf("generate reprise signer: %w", err)
		}
		bundleYAML := j000700FragmentBundleYAML(j000800RepriseMarker)
		sig, err := signing.Sign([]byte(bundleYAML), signer.Signer, signing.NamespacePublish)
		if err != nil {
			return fmt.Errorf("sign reprise loadout bundle: %w", err)
		}
		envelope, err := signing.EncodeLoadoutEnvelope([]byte(bundleYAML), sig, j000800ReprisePrincipal)
		if err != nil {
			return fmt.Errorf("encode reprise loadout envelope: %w", err)
		}
		j000800.repriseEnvelope = string(envelope)
		j000800.repriseVersionJSON = `{"name":"reprise","version":"9.9.9-j000800-fake"}`
		// The TEAM (not Bob individually) already trusts reprise's publisher
		// key, project-scoped and committed — so whenever a teammate DOES have
		// reprise installed, its self-advertised loadout content (S8 —
		// config.ProbeCompanionLoadouts, unconditionally injected into
		// assembled context by AssembleContext's appendBuiltinFragments,
		// independent of profile membership) reaches them without any
		// per-teammate review, exactly as "the team's context includes
		// guidance for reprise" describes.
		if err := w.env.TrustSigner(signer, j000800ReprisePrincipal, true); err != nil {
			return fmt.Errorf("trust reprise signer: %w", err)
		}
		return j000700CommitAndPush(w, "trust the reprise companion's publisher key")
	})

	ctx.Step(`^the "reprise" companion is (installed|not installed) on Bob's machine$`, func(c context.Context, presence string) error {
		w := worldFrom(c)
		// Scrub any REAL "reprise" binary from this process's PATH first: a
		// developer/CI machine that happens to already have one installed
		// (e.g. the ctxloom devcontainer itself) would otherwise satisfy
		// config.DiscoverCompanions's lookPath even in the "not installed"
		// row, making the negative case host-dependent instead of
		// deterministic.
		if err := j001800ScrubFromPath(w, "reprise"); err != nil {
			return err
		}
		if presence == "not installed" {
			return nil
		}
		j000800 := w.j000800()
		return w.env.InstallFakeCompanion("reprise", j000800.repriseVersionJSON, j000800.repriseEnvelope)
	})

	ctx.Step(`^his assistant receives the team's context that does not depend on reprise$`, func(c context.Context) error {
		return assertBobMaterializedContains(worldFrom(c), j000800TeamMarker)
	})

	ctx.Step(`^the reprise-dependent guidance (reaches|does not reach) his assistant$`, func(c context.Context, reaches string) error {
		w := worldFrom(c)
		if reaches == "reaches" {
			return assertBobMaterializedContains(w, j000800RepriseMarker)
		}
		return assertBobMaterializedDoesNotContain(w, j000800RepriseMarker)
	})

	ctx.Step(`^nothing fails because of the companion's presence or absence$`, func(c context.Context) error {
		w := worldFrom(c)
		// Real evidence for the @doc capture sidecar: by the time this step
		// runs, "Bob starts a session" already consumed w.j000700().bobOutput via
		// the automatic docLastBobOutput attribution, so this assertion (which
		// runs no new command of its own) would otherwise render with an empty
		// evidence pane despite being the load-bearing "and it actually
		// succeeded" check — re-attach the same exit/output pair explicitly.
		w.docStepMaterialized = fmt.Sprintf("Bob's session start: exit=%d\n%s", w.j000700().bobExit, strings.TrimSpace(w.j000700().bobOutput))
		if w.j000700().bobExit != 0 {
			return fmt.Errorf("expected Bob's session start to succeed regardless of the companion's presence/absence; exit %d, output:\n%s",
				w.j000700().bobExit, w.j000700().bobOutput)
		}
		return nil
	})
}

// bobMaterialized (re-)materializes Bob's "default" profile into "out" and
// returns the resulting CLAUDE.md — the payload every J000800 Then step checks.
// Always re-materializing (rather than trusting an earlier step already
// did) keeps each Then step self-sufficient regardless of whether the
// scenario's own When steps included an explicit "Bob starts a session"
// (scenario 2's does not: its last When is "Bob fetches the context...",
// and the assembled/materialized context is exactly what its Then steps
// need to inspect). Mirrors steps_j000200_setup.go's materializeDefault
// convention: materialize is expected to exit 0 even while withholding
// pending content, so only a read failure afterward is fatal here.
func bobMaterialized(w *World) (string, error) {
	_ = runBob(w, "profile", "materialize", j000700Profile, "--target", "out")
	body, err := readBobFile(w, filepath.Join("out", "CLAUDE.md"))
	if err != nil {
		return "", fmt.Errorf("read Bob's materialized out/CLAUDE.md (materialize output:\n%s): %w", w.j000700().bobOutput, err)
	}
	return body, nil
}

// assertBobMaterializedContains/DoesNotContain materialize Bob's context and
// check for want — the payload assertion every J000800 Then step reduces to.
func assertBobMaterializedContains(w *World, want string) error {
	body, err := bobMaterialized(w)
	if err != nil {
		return err
	}
	if !strings.Contains(body, want) {
		return fmt.Errorf("Bob's materialized context does not contain %q; content:\n%s", want, body)
	}
	return nil
}

func assertBobMaterializedDoesNotContain(w *World, notWant string) error {
	body, err := bobMaterialized(w)
	if err != nil {
		return err
	}
	if strings.Contains(body, notWant) {
		return fmt.Errorf("Bob's materialized context unexpectedly contains %q; content:\n%s", notWant, body)
	}
	return nil
}
