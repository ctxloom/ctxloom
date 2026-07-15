//go:build acceptance

// J10: "the ctxloom-doctor Agent Skill reaches every engine's native skill
// surface" (j10_agent_skill.feature). The doctor skill is authored INLINE
// here (like skill.feature authors "reviewer") so this journey does not
// depend on the ctxloom-default bundle repo. Reuses steps_skill.go's
// splitSkillRef/skillPackageDir and steps_j5.go's j5FileContains/j5ReadJSON
// rather than re-implementing skill-ref parsing or file/JSON reads.
//
// codex row correction (found by RUNNING it, not just reading source): the
// design brief this journey was built from assumed codex's skills surface
// is GLOBAL on every path ($CODEX_HOME/skills) and that a `profile
// materialize --backend codex` invocation would need $CODEX_HOME redirected
// to observe it. Live-verified false: on the static `profile materialize`
// CLI path, codex.NewSurfaces (internal/codex/surfaces.go) binds Skills to
// a closure with no homeOverride, so it cell-scopes under --target exactly
// like its Commands surface already does (j5_multi_engine.feature's own
// codex row) — see engineSkillMDPath's doc below for the exact citation.
package acceptance

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/cucumber/godog"
)

// doctorSkillMD is the "ctxloom-doctor" skill's authored SKILL.md: frontmatter
// carrying DOCTOR-SKILL-MARKER-7d4e21 in its description (the primary,
// progressive-disclosure payload every engine's loader reads first) plus a
// body of four independently-greppable DOCTOR-CHECK-* sections — the same
// marker vocabulary internal/cli/doctor_cmd.go's `ctxloom doctor` command
// emits, so a skill body and the CLI's own output read as one language.
const doctorSkillMD = `---
name: ctxloom-doctor
description: "DOCTOR-SKILL-MARKER-7d4e21 - Validate a ctxloom setup: dependencies, agents, version currency, hooks, and trust store; run before relying on isolation or signing."
---

# ctxloom doctor

Validate a ctxloom setup before relying on isolation or signing. If a
` + "`ctxloom doctor`" + ` command is available, run it and interpret each
DOCTOR-CHECK-* line it emits — the checks below use the SAME markers, so this
skill's job is to run the deterministic command and explain its output, not
duplicate the checking logic.

## DOCTOR-CHECK-DEPS-a1: required binaries on PATH

Confirm ` + "`ssh`" + ` and ` + "`ssh-keygen`" + ` are on PATH (needed for
signing), that every configured engine's own client binary resolves (claude,
codex, kiro-cli, agy, opencode — whichever this project actually uses), and
that a container runtime (docker or podman) is reachable if any agent uses
` + "`runtime: container`" + `.

## DOCTOR-CHECK-AGENTS-b2: agents resolve cleanly

Run ` + "`ctxloom agent list`" + ` and confirm every configured agent
resolves: its composed profiles load without error and its engine/runtime
are recognized values.

## DOCTOR-CHECK-VERSION-c3: version currency

Best-effort and skill-guided (there is no automated update check): compare
the output of ` + "`ctxloom version`" + ` against the newest tag on the
project's configured remote, and suggest an upgrade if it is behind.

## DOCTOR-CHECK-HOOKS-TRUST-d4: hooks and trust store

Confirm hooks are installed for the engine(s) actually in use (` + "`ctxloom manage install`" + `
writes them) and that the trust store is sane: at least one signer is
present and there is no dangling trust entry (see the ` + "`signer`" + `
subcommands for the trust-store roster).
`

func j10Of(w *World) *j10State {
	if w.j10 == nil {
		w.j10 = &j10State{}
	}
	return w.j10
}

// j10State is J10's fixture state: the currently-materialized engine row
// (Outline) and its --target dir.
type j10State struct {
	engine string
	target string
}

func registerJ10Steps(ctx *godog.ScenarioContext) {
	ctx.Step(`^Alice authors the ctxloom-doctor skill's full body in "([^"]*)"$`, func(c context.Context, ref string) error {
		w := worldFrom(c)
		bundle, name, err := splitSkillRef(ref)
		if err != nil {
			return err
		}
		path := filepath.Join(skillPackageDir(w, bundle, name), "SKILL.md")
		return os.WriteFile(path, []byte(doctorSkillMD), 0o644)
	})

	ctx.Step(`^Carol materializes the "([^"]*)" profile for (\S+)$`, func(c context.Context, profile, engine string) error {
		w := worldFrom(c)
		j10 := j10Of(w)
		j10.engine = engine
		j10.target = "out-" + engine
		return runOK(w, "profile", "materialize", profile, "--target", j10.target, "--backend", engine)
	})

	// engineSkillMDPath returns the --target-relative path to the
	// materialized ctxloom-doctor SKILL.md, per the per-engine table
	// j10_agent_skill.feature documents. codex's OWN skills surface is
	// documented GLOBAL ($CODEX_HOME/skills — internal/codex/skillfiles.go's
	// WriteSkillFiles, used by the LIVE run/launch path), but on the STATIC
	// `profile materialize` CLI path codex.NewSurfaces binds Skills to an
	// inline closure that — with no homeOverride, exactly like its Commands
	// closure — falls back to `target := dir` and writes cell-scoped under
	// cellScopedSkillsDir(dir) (internal/codex/surfaces.go's NewSurfaces),
	// landing at <target>/.codex/skills/, the SAME cell-scoping its commands
	// surface already uses (j5_multi_engine.feature's own codex row:
	// <target>/.codex/prompts/). Confirmed live (a real `profile materialize
	// --backend codex` run, with $CODEX_HOME redirected to a fixture dir,
	// wrote NOTHING there and wrote the skill under --target instead) —
	// globality is realized only on the live run/launch path, not here.
	engineSkillMDPath := func(w *World) (string, error) {
		j10 := j10Of(w)
		switch j10.engine {
		case "claude-code":
			return filepath.Join(j10.target, ".claude", "skills", "ctxloom-doctor", "SKILL.md"), nil
		case "kiro":
			return filepath.Join(j10.target, ".kiro", "skills", "ctxloom-doctor", "SKILL.md"), nil
		case "antigravity":
			return filepath.Join(j10.target, ".agents", "skills", "ctxloom-doctor", "SKILL.md"), nil
		case "opencode":
			return filepath.Join(j10.target, ".opencode", "skill", "ctxloom-doctor", "SKILL.md"), nil
		case "codex":
			return filepath.Join(j10.target, ".codex", "skills", "ctxloom-doctor", "SKILL.md"), nil
		default:
			return "", fmt.Errorf("j10: unknown engine %q for the skill surface", j10.engine)
		}
	}

	ctx.Step(`^the (\S+) skill surface carries the doctor skill's marker "([^"]*)"$`, func(c context.Context, engine, marker string) error {
		w := worldFrom(c)
		rel, err := engineSkillMDPath(w)
		if err != nil {
			return err
		}
		return j5FileContains(w, rel, marker)
	})

	ctx.Step(`^opencode\.json registers the skills surface$`, func(c context.Context) error {
		w := worldFrom(c)
		j10 := j10Of(w)
		rel := filepath.Join(j10.target, "opencode.json")
		doc, err := j5ReadJSON(w, rel)
		if err != nil {
			return err
		}
		skills, ok := doc["skills"].(map[string]any)
		if !ok {
			return fmt.Errorf("%s: no %q table; parsed: %+v", rel, "skills", doc)
		}
		paths, ok := skills["paths"].([]any)
		if !ok {
			return fmt.Errorf("%s: skills.paths is not an array; parsed: %+v", rel, skills)
		}
		var found bool
		var have []string
		for _, p := range paths {
			s, _ := p.(string)
			have = append(have, s)
			if s == ".opencode/skill" {
				found = true
			}
		}
		w.docStepMaterialized = fmt.Sprintf("%s -> skills.paths: %v", rel, have)
		if !found {
			return fmt.Errorf("%s's skills.paths %v does not register \".opencode/skill\"", rel, have)
		}
		return nil
	})
}
