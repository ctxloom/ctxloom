//go:build acceptance

// J7: "a team lead shares a command with the team" journey
// (j7_team_authoring.feature). Carol and Bob are two team members with
// separate local checkouts of the SAME project's git history — modeled here
// as two directories cloned from a common bare "origin" this file creates.
// w.env.ProjectDir plays Carol's checkout (the CLI/env plumbing every other
// step file already assumes); Bob's checkout is a second, harness-managed
// directory this file drives directly via exec.Cmd (TestEnvironment has no
// second-project-directory concept, so this file adds its own thin git/exec
// plumbing rather than extending the shared testenv package — see the
// worktree brief's request to keep shared-file churn tight).
//
// Authoring goes straight to the bundle's YAML content field (mirroring
// steps_j2_common.go's fragmentSourceYAML/commandSourceYAML) rather than
// through the interactive `command create`+`edit` round trip: the mechanism
// under test is trust/propagation/distillation, not the editor UX, and direct
// YAML authorship gives exact, reproducible marker content across the
// "before" and "after" states scenario 3 needs. `bundle create`/`profile
// create` still run for real, so the bundle/profile registration path itself
// is exercised.
package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"
)

const (
	j7Bundle  = "team-standards"
	j7Profile = "default"

	// j7CommandMarkerV1/V2 are the "before"/"after" content of the
	// conventional-commits command scenarios 1 and 3 author/edit — distinct so a
	// materialized command file can be checked for exactly one of them.
	j7CommandMarkerV1 = "J7-CONVENTIONAL-COMMITS-V1-USE-IMPERATIVE-MOOD-IN-THE-SUBJECT-LINE"
	j7CommandMarkerV2 = "J7-CONVENTIONAL-COMMITS-V2-INCLUDE-A-SCOPE-IN-THE-SUBJECT-LINE"

	// j7FragVerboseMarker/DistilledMarker are scenario 2's original/compact
	// fragment content. The distilled form is what the mock LLM backend is
	// configured to answer with (CTXLOOM_MOCK_RESPONSE), so `fragment distill`
	// exercises the REAL distill machinery (bundle parse, plugin invocation,
	// content-hash bookkeeping, save) hermetically.
	j7FragVerboseMarker   = "J7-VERBOSE-ORIGINAL-FRAGMENT-GUIDANCE-KEEP-THIS-LONG-FORM-TEXT-INTACT-FOR-THE-TEST"
	j7FragDistilledMarker = "J7-DISTILLED-COMPACT-FRAGMENT-GUIDANCE"
)

// j7State is J7's per-scenario state, held as a single field on World (see
// world.go) to keep that shared struct's diff to one line.
type j7State struct {
	bareOrigin  string // the team project's bare git origin (file path, no scheme)
	bobDir      string // Bob's separate clone directory (empty until his first pull)
	bobOutput   string // last command's combined stdout+stderr run in Bob's checkout
	bobExit     int
	commandName string // the command name scenarios 1/3 authored/edited (default "conventional-commits")
}

// j7 returns (lazily allocating) this scenario's J7 state.
func (w *World) j7() *j7State {
	if w.j7s == nil {
		w.j7s = &j7State{}
	}
	return w.j7s
}

func registerJ7Steps(ctx *godog.ScenarioContext) {
	// --- Scenario 1: authored in-project, reaches a teammate, no review -----

	ctx.Step(`^Carol is working in the team's project$`, func(c context.Context) error {
		return j7SetupTeamProject(worldFrom(c))
	})

	ctx.Step(`^Carol authors a "([^"]*)" command$`, func(c context.Context, name string) error {
		return j7AuthorCommand(worldFrom(c), name, j7CommandMarkerV1)
	})

	ctx.Step(`^she commits it to the project$`, func(c context.Context) error {
		w := worldFrom(c)
		return j7CommitAndPush(w, "author "+w.j7().commandName+" command")
	})

	ctx.Step(`^Bob pulls the project$`, func(c context.Context) error {
		return j7BobPull(worldFrom(c))
	})

	ctx.Step(`^Bob's assistant can invoke the conventional-commits command$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := runBob(w, "profile", "materialize", j7Profile, "--target", "out"); err != nil {
			return err
		}
		body, err := findBobCommandFile(w, w.j7().commandNameOrDefault())
		if err != nil {
			return err
		}
		if !strings.Contains(body, j7CommandMarkerV1) {
			return fmt.Errorf("materialized command file does not contain the command's content; content:\n%s", body)
		}
		return nil
	})

	ctx.Step(`^it reached him without any review, because the project is first-party$`, func(c context.Context) error {
		w := worldFrom(c)
		// This used to grep for the exact prose sentence "Nothing
		// is pending review." -- a wording change would silently break the
		// assertion into a false red, or (if the new wording still contained
		// that substring) a false green. review --list --format json emits
		// operations.PendingReviewResult, whose Total field is the real
		// structured signal this step means to check.
		if err := runBob(w, "review", "--list", "--format", "json"); err != nil {
			return err
		}
		out := w.j7().bobOutput
		var res struct {
			Total int `json:"total"`
		}
		if err := json.Unmarshal([]byte(out), &res); err != nil {
			return fmt.Errorf("review --list --format json: not valid JSON: %w (output:\n%s)", err, out)
		}
		if res.Total != 0 {
			return fmt.Errorf("expected first-party project content to need no review (total=0), got total=%d; `review --list --format json` output:\n%s", res.Total, out)
		}
		return nil
	})

	// --- Scenario 2: distillation serves the compact form -------------------

	ctx.Step(`^Carol has authored a verbose fragment in the project$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := j7SetupDistillProject(w); err != nil {
			return err
		}
		return w.env.WriteFile(".ctxloom/content/bundles/"+j7Bundle+".yaml", j7FragmentBundleYAML(j7FragVerboseMarker))
	})

	ctx.Step(`^Carol distills the fragment$`, func(c context.Context) error {
		return runOK(worldFrom(c), "fragment", "distill", j7Bundle+"#fragments/guidance", "-f")
	})

	ctx.Step(`^she commits both forms$`, func(c context.Context) error {
		return j7CommitAndPush(worldFrom(c), "distill guidance fragment")
	})

	ctx.Step(`^Bob pulls the project with distilled context enabled$`, func(c context.Context) error {
		return j7BobPull(worldFrom(c))
	})

	ctx.Step(`^Bob's assistant receives the distilled guidance$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := runBob(w, "profile", "materialize", j7Profile, "--target", "out"); err != nil {
			return err
		}
		body, err := readBobFile(w, filepath.Join("out", "CLAUDE.md"))
		if err != nil {
			return err
		}
		if !strings.Contains(body, j7FragDistilledMarker) {
			return fmt.Errorf("materialized CLAUDE.md does not contain the distilled marker; content:\n%s", body)
		}
		return nil
	})

	ctx.Step(`^it does not receive the verbose original$`, func(c context.Context) error {
		w := worldFrom(c)
		body, err := readBobFile(w, filepath.Join("out", "CLAUDE.md"))
		if err != nil {
			return err
		}
		if strings.Contains(body, j7FragVerboseMarker) {
			return fmt.Errorf("materialized CLAUDE.md unexpectedly contains the verbose original marker; content:\n%s", body)
		}
		return nil
	})

	// --- Scenario 3: an edit propagates, the stale version does not ---------

	ctx.Step(`^Bob's assistant already has Carol's conventional-commits command$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := j7SetupTeamProject(w); err != nil {
			return err
		}
		if err := j7AuthorCommand(w, "conventional-commits", j7CommandMarkerV1); err != nil {
			return err
		}
		if err := j7CommitAndPush(w, "author conventional-commits command"); err != nil {
			return err
		}
		if err := j7BobPull(w); err != nil {
			return err
		}
		return runBob(w, "profile", "materialize", j7Profile, "--target", "out")
	})

	ctx.Step(`^Carol edits the command$`, func(c context.Context) error {
		w := worldFrom(c)
		return j7AuthorCommand(w, w.j7().commandNameOrDefault(), j7CommandMarkerV2)
	})

	ctx.Step(`^she commits the change$`, func(c context.Context) error {
		return j7CommitAndPush(worldFrom(c), "edit conventional-commits command")
	})

	ctx.Step(`^Bob pulls the project again$`, func(c context.Context) error {
		return j7BobPull(worldFrom(c))
	})

	ctx.Step(`^Bob's assistant has the updated command$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := runBob(w, "profile", "materialize", j7Profile, "--target", "out"); err != nil {
			return err
		}
		body, err := findBobCommandFile(w, w.j7().commandNameOrDefault())
		if err != nil {
			return err
		}
		if !strings.Contains(body, j7CommandMarkerV2) {
			return fmt.Errorf("materialized command file does not contain the updated command marker; content:\n%s", body)
		}
		return nil
	})

	ctx.Step(`^it no longer has the previous version$`, func(c context.Context) error {
		w := worldFrom(c)
		body, err := findBobCommandFile(w, w.j7().commandNameOrDefault())
		if err != nil {
			return err
		}
		if strings.Contains(body, j7CommandMarkerV1) {
			return fmt.Errorf("materialized command file unexpectedly still contains the previous command version marker; content:\n%s", body)
		}
		return nil
	})
}

// commandNameOrDefault returns the command name scenario 1/3's author step
// recorded, defaulting to "conventional-commits" — the only name the LOCKED
// Gherkin ever actually uses, but steps read this rather than the literal so
// a future scenario naming a different command does not silently pass by
// checking the wrong file.
func (s *j7State) commandNameOrDefault() string {
	if s.commandName == "" {
		return "conventional-commits"
	}
	return s.commandName
}

// j7SetupTeamProject scaffolds Carol's checkout (w.env.ProjectDir) as the
// team's shared project with the minimal fixture config (no LLM needed:
// `bundle`/`profile create` and `profile materialize` all work against it,
// exactly as review.feature/command.feature/fragment.feature already rely on).
func j7SetupTeamProject(w *World) error {
	if err := j7SetupProject(w, minimalConfig); err != nil {
		return err
	}
	// minimalHomeEditorConfig lives in HOME: editor.command is ScopeMachine
	// (internal/config/layerscope), so it no longer survives a real Load
	// from the committed project file minimalConfig alone now carries.
	return w.env.WriteHomeFile(".ctxloom/config.yaml", minimalHomeEditorConfig)
}

// j7SetupDistillProject scaffolds Carol's checkout with a "mock" LLM entry
// wired as both the primary and (via FastLabel's fallback) the fast/distill
// role, its response pinned to j7FragDistilledMarker via the labeled config's
// own env map (mirrors testenv/mock_lm.go's WriteConfig) — so `fragment
// distill` genuinely invokes the plugin/self-invoking-client machinery
// hermetically, and the saved "distilled" bytes are exactly this marker.
// use_distilled is set explicitly (even though true is already the default)
// so the scenario's "with distilled context enabled" precondition is honest
// about what it depends on.
//
// llm.configs.mock.env lives in HOME (w.env.WriteHomeFile below), not the
// project file j7SetupProject writes: llm.configs.*.env is ScopeMachine
// (internal/config/layerscope) — credential passthrough, where a committed
// project-file value is a leaked secret — so it no longer survives a real
// Load from the project layer. Splitting the "mock" label across layers like
// this is legal (unlike agents.*, llm.configs.* has no atomic-replace merge
// rule), so the project's own `type: mock` and home's `env` deep-merge into
// one usable entry.
func j7SetupDistillProject(w *World) error {
	cfg := "version: 4\n" +
		"llm:\n" +
		"  configs:\n" +
		"    mock:\n" +
		"      type: mock\n" +
		"  defaults:\n" +
		"    primary: mock\n" +
		"config:\n" +
		"  use_distilled: true\n"
	if err := j7SetupProject(w, cfg); err != nil {
		return err
	}
	homeCfg := "version: 4\n" +
		"llm:\n" +
		"  configs:\n" +
		"    mock:\n" +
		"      env:\n" +
		fmt.Sprintf("        CTXLOOM_MOCK_RESPONSE: %q\n", j7FragDistilledMarker)
	return w.env.WriteHomeFile(".ctxloom/config.yaml", homeCfg)
}

// j7SetupProject is the shared scaffold behind j7SetupTeamProject/
// j7SetupDistillProject: git-init Carol's checkout on a deterministic "main"
// branch (pinned via symbolic-ref before the first commit, since
// TestEnvironment.InitGitRepo has no way to pass `-b main` through and the
// host git's init.defaultBranch is not something this suite controls), write
// the given config, create the team bundle + its default profile for real
// (exercising those CLI paths), commit the scaffold, then create a bare
// origin and push — the shared history Bob's checkout(s) clone/pull from.
func j7SetupProject(w *World, configYAML string) error {
	if err := w.env.InitGitRepo(); err != nil {
		return err
	}
	if _, err := j7Git(w.env.ProjectDir, "symbolic-ref", "HEAD", "refs/heads/main"); err != nil {
		return err
	}
	if err := w.env.WriteFile(".ctxloom/config.yaml", configYAML); err != nil {
		return err
	}
	if err := runOK(w, "bundle", "create", j7Bundle, "-d", "Team standards"); err != nil {
		return err
	}
	if err := runOK(w, "profile", "create", j7Profile, "-b", j7Bundle, "-d", "Team default profile"); err != nil {
		return err
	}
	if err := w.env.GitCommit("initial ctxloom project scaffold"); err != nil {
		return err
	}
	bare, err := j7NewBareOrigin(w)
	if err != nil {
		return err
	}
	w.j7().bareOrigin = bare
	if _, err := j7Git(w.env.ProjectDir, "remote", "add", "origin", bare); err != nil {
		return err
	}
	_, err = j7Git(w.env.ProjectDir, "push", "-u", "origin", "main")
	return err
}

// j7AuthorCommand overwrites the team bundle's YAML file so it carries exactly
// one command (name, content) — both the initial "authors a command" act and
// a later "edits the command" act (a full overwrite, not an append, so an
// edit genuinely REPLACES the previous content rather than merely adding to
// it, which is what scenario 3's "no longer has the previous version" needs
// to be meaningful). Records the name on World's J7 state for later steps.
func j7AuthorCommand(w *World, name, content string) error {
	w.j7().commandName = name
	body := fmt.Sprintf("version: \"1.0.0\"\ncommands:\n  %s:\n    content: %q\n", name, content)
	return w.env.WriteFile(".ctxloom/content/bundles/"+j7Bundle+".yaml", body)
}

// j7FragmentBundleYAML is j7AuthorCommand's fragment analogue for scenario 2 —
// a single fragment named "guidance" carrying content.
func j7FragmentBundleYAML(content string) string {
	return fmt.Sprintf("version: \"1.0.0\"\nfragments:\n  guidance:\n    content: %q\n", content)
}

// j7CommitAndPush commits every change in Carol's checkout and pushes it to
// the team origin — "she commits it to the project" / "she commits both
// forms" / "she commits the change".
func j7CommitAndPush(w *World, message string) error {
	if err := w.env.GitCommit(message); err != nil {
		return err
	}
	_, err := j7Git(w.env.ProjectDir, "push", "origin", "main")
	return err
}

// j7BobPull is "Bob pulls the project" / "...again" / "...with distilled
// context enabled": the FIRST call clones the team origin fresh into Bob's
// own directory; every subsequent call fetches+merges into that SAME
// checkout — a real `git pull`, so a harness that skipped this step would
// leave Bob's files genuinely stale (the propagation scenario 3 exists to
// prove is not what happens).
func j7BobPull(w *World) error {
	j7 := w.j7()
	if j7.bareOrigin == "" {
		return fmt.Errorf("no team origin seeded yet (Carol's project was never set up)")
	}
	if j7.bobDir == "" {
		dir := filepath.Join(w.env.Root, "bob-project")
		if _, err := j7Git("", "clone", j7.bareOrigin, dir); err != nil {
			return err
		}
		j7.bobDir = dir
		return nil
	}
	_, err := j7Git(j7.bobDir, "pull", "origin", "main")
	return err
}

// runBob runs the ctxloom binary in Bob's checkout — TestEnvironment.Run is
// hardcoded to Carol's ProjectDir, so this reuses its isolated-env/binary
// plumbing via the exported Command() constructor and only overrides the
// working directory. Fails loudly on a non-zero exit (mirroring steps_j2_
// common.go's runOK) rather than deferring to a separate "command succeeds"
// step Bob's checkout has no such step for.
func runBob(w *World, args ...string) error {
	j7 := w.j7()
	if j7.bobDir == "" {
		return fmt.Errorf("bob has not pulled the project yet")
	}
	cmd := w.env.Command(nil, args...)
	cmd.Dir = j7.bobDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	j7.bobOutput = stdout.String() + stderr.String()
	if exitErr, ok := err.(*exec.ExitError); ok {
		j7.bobExit = exitErr.ExitCode()
	} else if err != nil {
		j7.bobExit = -1
	} else {
		j7.bobExit = 0
	}
	if j7.bobExit != 0 {
		return fmt.Errorf("%v failed in bob's checkout (exit %d): %s", args, j7.bobExit, j7.bobOutput)
	}
	return nil
}

// readBobFile reads a file relative to Bob's checkout (the ReadFile analogue
// of runBob — TestEnvironment.ReadFile is hardcoded to Carol's ProjectDir).
func readBobFile(w *World, rel string) (string, error) {
	data, err := os.ReadFile(filepath.Join(w.j7().bobDir, rel))
	if err != nil {
		return "", err
	}
	// Surface the delivered file (e.g. Bob's materialized CLAUDE.md) to the
	// @doc capture sidecar — the marker-bearing proof of what propagation
	// actually put in front of the teammate (set-and-consume; no-op off-capture).
	w.docStepMaterialized = string(data)
	return string(data), nil
}

// findBobCommandFile globs for the materialized slash-command file backing
// commandName under Bob's "out" materialize target and reads the first match.
// The exact basename is an internal naming detail (bundle-prefixed on any
// export-name collision, see internal/lm/backends/commandfiles.go's
// exportNames), so this asserts on CONTENT rather than an exact path.
func findBobCommandFile(w *World, commandName string) (string, error) {
	glob := filepath.Join(w.j7().bobDir, "out", ".claude", "commands", "*"+commandName+"*.md")
	matches, err := filepath.Glob(glob)
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("no materialized command file matches %q under %s", glob, w.j7().bobDir)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		return "", err
	}
	// Hand the marker-bearing file that actually reached Bob to the @doc capture
	// sidecar (set-and-consume; a no-op when capture is off). J7's teammate-side
	// assertions read Bob's files directly rather than through w.env, so this is
	// the only place the sidecar can see the content propagation delivered.
	w.docStepMaterialized = string(data)
	return string(data), nil
}

// j7GitEnv is the hermetic git invocation environment for J7's own origin/
// Bob-clone plumbing (mirrors testenv's unexported runGitE/gitOutput, which
// this package cannot reach across the package boundary — see the file doc).
func j7GitEnv() []string {
	return append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
}

// j7Git runs git in dir (or the current process's cwd when dir is empty, for
// the one-shot bare-repo init) with a hermetic environment, returning stdout
// or an error carrying stderr.
func j7Git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = j7GitEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s (in %q): %w: %s", strings.Join(args, " "), dir, err, stderr.String())
	}
	return stdout.String(), nil
}

// j7NewBareOrigin creates a fresh bare git repo (branch "main") under the
// environment's Root, standing in for the team's shared remote (GitHub/
// GitLab/etc. in reality) that both Carol's and Bob's checkouts push/pull.
func j7NewBareOrigin(w *World) (string, error) {
	root, err := os.MkdirTemp(w.env.Root, "j7-origin-*")
	if err != nil {
		return "", err
	}
	bare := filepath.Join(root, "team.git")
	if _, err := j7Git("", "init", "--bare", "-b", "main", bare); err != nil {
		return "", err
	}
	return bare, nil
}
