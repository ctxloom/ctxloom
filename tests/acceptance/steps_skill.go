//go:build acceptance

// Agent Skill journeys (skill.feature): the true SKILL.md package surface
// (Part B, skill-command-split.plan.md), distinct from the renamed slash
// "command" surface command.feature already covers. Every scenario here
// drives the real `ctxloom skill` CLI and asserts real, on-disk payload
// (bundle.yaml manifest bytes, materialized files with their POSIX modes,
// signature verification outcomes) — never a bare exit-code or
// substring-of-a-key-name.
//
// Bundles here are always DIRECTORY-FORM (bundle.yaml + skills/<name>/):
// `ctxloom skill create`/`sync`/`import` all reject a single-file bundle
// (loud validation error), and `ctxloom bundle create` (steps_fixture.go's
// fixture) only ever writes a single-file bundle — so, like J4's own "team"
// bundle, the bundle.yaml is written directly rather than through that
// fixture.
//
// Signing here mirrors steps_j2_common.go/signing_acceptance.go's approach
// (a generated ed25519 TestSigner + signing.Sign / TrustSigner) rather than
// driving `ctxloom skill export --sign`: that flag resolves a key through a
// real ssh-agent (internal/signing/agentkey), which is not something a
// hermetic acceptance run can assume is present. Signing the manifest bytes
// directly with a TestSigner is the exact preimage
// bundles.PublisherSkillSignatureVerifier verifies against
// (pkg.Manifest.Serialize()), so `ctxloom skill import --sig` is exercised
// for real either way.
package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"
	"github.com/spf13/afero"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/tests/integration/testenv"
)

// dirFormBundleYAML is the minimal directory-form bundle.yaml this file
// seeds: no fragments/commands needed, just enough for the loader to resolve
// the bundle by name (via "<name>/bundle.yaml") and for `ctxloom skill
// create`/`import` to accept it as directory-form (basename "bundle.yaml").
func dirFormBundleYAML(name string) string {
	return fmt.Sprintf("version: \"1.0.0\"\ndescription: %s bundle\n", name)
}

// skillBundleDir returns the absolute on-disk directory a bundle's
// directory-form content lives in.
func skillBundleDir(w *World, bundle string) string {
	return filepath.Join(w.env.ProjectDir, ".ctxloom", "content", "bundles", bundle)
}

// skillPackageDir returns the absolute on-disk directory for one skill
// package — the default "skills/<name>" layout every scenario here uses (none
// authors a custom `path:`).
func skillPackageDir(w *World, bundle, name string) string {
	return filepath.Join(skillBundleDir(w, bundle), "skills", name)
}

// splitSkillRef splits a "bundle#skills/name" reference into its bundle and
// skill name, erroring loudly on any other shape rather than silently
// misresolving a path.
func splitSkillRef(ref string) (bundle, name string, err error) {
	b, n, ok := strings.Cut(ref, "#skills/")
	if !ok {
		return "", "", fmt.Errorf("skill ref %q: want \"bundle#skills/name\"", ref)
	}
	return b, n, nil
}

// skillSignerFor lazily generates (and caches, per-scenario) a fresh ed25519
// TestSigner keyed by a human name ("Trent", "Mallory") — the same identity
// must be produced whether this name is first seen via a signing step or a
// trust step, in whichever order the Gherkin uses them.
func skillSignerFor(w *World, name string) (*testenv.TestSigner, error) {
	if w.skillSigners == nil {
		w.skillSigners = map[string]*testenv.TestSigner{}
	}
	if s, ok := w.skillSigners[name]; ok {
		return s, nil
	}
	signer, err := testenv.GenerateTestSigner()
	if err != nil {
		return nil, fmt.Errorf("generate signer for %q: %w", name, err)
	}
	w.skillSigners[name] = signer
	return signer, nil
}

func registerSkillSteps(ctx *godog.ScenarioContext) {
	// --- Given/And: authoring fixtures (setup — no evidence required) --------

	ctx.Step(`^Alice's project has a directory-form bundle "([^"]*)"$`, func(c context.Context, name string) error {
		w := worldFrom(c)
		if !w.env.FileExists(".ctxloom/config.yaml") {
			if err := w.env.InitGitRepo(); err != nil {
				return err
			}
			if err := writeMinimalConfig(w.env); err != nil {
				return err
			}
		}
		return w.env.WriteFile(".ctxloom/content/bundles/"+name+"/bundle.yaml", dirFormBundleYAML(name))
	})

	ctx.Step(`^a directory-form bundle "([^"]*)" exists$`, func(c context.Context, name string) error {
		w := worldFrom(c)
		return w.env.WriteFile(".ctxloom/content/bundles/"+name+"/bundle.yaml", dirFormBundleYAML(name))
	})

	// Writes a scripts/run.sh file directly (ctxloom skill create only
	// scaffolds SKILL.md) with the executable bit set and a caller-chosen
	// marker in its body, so later steps can assert both the mode AND real
	// content survived materialization/export/import.
	ctx.Step(`^Alice adds an executable scripts/run\.sh carrying the marker "([^"]*)" to the skill "([^"]*)"$`, func(c context.Context, marker, ref string) error {
		w := worldFrom(c)
		bundle, name, err := splitSkillRef(ref)
		if err != nil {
			return err
		}
		path := filepath.Join(skillPackageDir(w, bundle, name), "scripts", "run.sh")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("create scripts dir: %w", err)
		}
		body := "#!/bin/sh\n# " + marker + "\necho " + marker + "\n"
		return os.WriteFile(path, []byte(body), 0o755)
	})

	ctx.Step(`^profile "([^"]*)" curates skill "([^"]*)"$`, func(c context.Context, profile, ref string) error {
		w := worldFrom(c)
		path := ".ctxloom/profiles/" + profile + ".yaml"
		body, err := w.env.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read profile %q: %w", profile, err)
		}
		body += fmt.Sprintf("skills:\n  - %s\n", ref)
		return w.env.WriteFile(path, body)
	})

	ctx.Step(`^(\S+)'s key signs the skill "([^"]*)" over its current manifest, into "([^"]*)"$`, func(c context.Context, signerName, ref, sigFile string) error {
		w := worldFrom(c)
		bundle, name, err := splitSkillRef(ref)
		if err != nil {
			return err
		}
		pkg, err := bundles.ParseSkillPackage(afero.NewOsFs(), skillPackageDir(w, bundle, name), 0)
		if err != nil {
			return fmt.Errorf("parse skill %q for signing: %w", ref, err)
		}
		signer, err := skillSignerFor(w, signerName)
		if err != nil {
			return err
		}
		armored, err := signing.Sign(pkg.Manifest.Serialize(), signer.Signer, signing.NamespacePublish)
		if err != nil {
			return fmt.Errorf("sign %q: %w", ref, err)
		}
		return os.WriteFile(filepath.Join(w.env.ProjectDir, sigFile), armored, 0o644)
	})

	ctx.Step(`^(\S+) is a trusted publisher for this project$`, func(c context.Context, name string) error {
		w := worldFrom(c)
		signer, err := skillSignerFor(w, name)
		if err != nil {
			return err
		}
		return w.env.TrustSigner(signer, strings.ToLower(name)+"@example.com", true)
	})

	ctx.Step(`^the skill "([^"]*)"'s SKILL\.md is modified after signing$`, func(c context.Context, ref string) error {
		w := worldFrom(c)
		bundle, name, err := splitSkillRef(ref)
		if err != nil {
			return err
		}
		path := filepath.Join(skillPackageDir(w, bundle, name), "SKILL.md")
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		return os.WriteFile(path, append(data, []byte("\n<!-- TAMPERED-AFTER-SIGNING-by-Mallory -->\n")...), 0o644)
	})

	// --- Then: payload assertions (every one sets w.docStepMaterialized with
	// real, observed evidence — living-docs' evidence gate requires it) -------

	ctx.Step(`^the bundle manifest for "([^"]*)" records real hashes and modes for "([^"]*)" and "([^"]*)"$`,
		func(c context.Context, ref, skillMDPath, scriptPath string) error {
			w := worldFrom(c)
			bundle, name, err := splitSkillRef(ref)
			if err != nil {
				return err
			}
			body, err := w.env.ReadFile(".ctxloom/content/bundles/" + bundle + "/bundle.yaml")
			if err != nil {
				return fmt.Errorf("read bundle.yaml: %w", err)
			}
			var doc struct {
				Skills map[string]struct {
					Files map[string]struct {
						SHA256 string `yaml:"sha256"`
						Mode   string `yaml:"mode"`
					} `yaml:"files"`
				} `yaml:"skills"`
			}
			if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
				return fmt.Errorf("parse bundle.yaml: %w", err)
			}
			skill, ok := doc.Skills[name]
			if !ok {
				return fmt.Errorf("bundle.yaml has no skills.%s entry; content:\n%s", name, body)
			}
			var evidence []string
			for _, p := range []string{skillMDPath, scriptPath} {
				f, ok := skill.Files[p]
				if !ok {
					return fmt.Errorf("skills.%s.files has no entry for %q; recorded: %+v", name, p, skill.Files)
				}
				// bundles.hashContent's canonical form is "sha256:<64 hex chars>".
				if !strings.HasPrefix(f.SHA256, "sha256:") || len(f.SHA256) != len("sha256:")+64 {
					return fmt.Errorf("skills.%s.files[%q].sha256 = %q, does not look like a real \"sha256:<hex>\" digest", name, p, f.SHA256)
				}
				if f.Mode == "" {
					return fmt.Errorf("skills.%s.files[%q].mode is empty", name, p)
				}
				evidence = append(evidence, fmt.Sprintf("%s: sha256=%s mode=%s", p, f.SHA256, f.Mode))
			}
			w.docStepMaterialized = fmt.Sprintf(".ctxloom/content/bundles/%s/bundle.yaml -> skills.%s.files\n%s",
				bundle, name, strings.Join(evidence, "\n"))
			// scripts/ entries are executables — the mode's exec bit is
			// load-bearing (skill-command-split.plan.md §3.1) and must be
			// recorded, not merely present.
			if skill.Files[scriptPath].Mode != "0755" {
				return fmt.Errorf("skills.%s.files[%q].mode = %q, want \"0755\"", name, scriptPath, skill.Files[scriptPath].Mode)
			}
			if skill.Files[skillMDPath].Mode != "0644" {
				return fmt.Errorf("skills.%s.files[%q].mode = %q, want \"0644\"", name, skillMDPath, skill.Files[skillMDPath].Mode)
			}
			return nil
		})

	ctx.Step(`^the skill list output includes "([^"]*)" from bundle "([^"]*)"$`, func(c context.Context, name, bundle string) error {
		w := worldFrom(c)
		out := w.env.LastOutput()
		w.docStepMaterialized = out
		if !strings.Contains(out, bundle) || !strings.Contains(out, name) {
			return fmt.Errorf("`ctxloom skill list` output does not mention bundle %q / skill %q; output:\n%s", bundle, name, out)
		}
		return nil
	})

	// Dedicated (not the shared generic steps_mcp.go "the resource contains"
	// step) so this Then carries its own @doc-capture evidence — a bare reuse
	// of the generic step here would set nothing, tripping the living-docs
	// evidence gate (every Then in an @doc-tagged feature must observe
	// something real).
	ctx.Step(`^the skill resource contains "([^"]*)"$`, func(c context.Context, want string) error {
		w := worldFrom(c)
		w.docStepMaterialized = w.lastRes
		if !strings.Contains(w.lastRes, want) {
			return fmt.Errorf("skill resource does not contain %q; content:\n%s", want, w.lastRes)
		}
		return nil
	})

	ctx.Step(`^the skill show output carries the frontmatter description "([^"]*)"$`, func(c context.Context, want string) error {
		w := worldFrom(c)
		out := w.env.LastOutput()
		w.docStepMaterialized = out
		if !strings.Contains(out, want) {
			return fmt.Errorf("`ctxloom skill show` output does not carry description %q; output:\n%s", want, out)
		}
		return nil
	})

	// Generic payload check reused across every materialize/export/import
	// scenario: a project-relative file's real content contains marker.
	// j4FileContains (steps_j4.go) already does exactly this — real read,
	// real error, real @doc-capture excerpt — so this is a thin step-text
	// wrapper over it rather than a duplicate implementation.
	ctx.Step(`^"([^"]*)" carries the marker "([^"]*)"$`, func(c context.Context, rel, marker string) error {
		return j4FileContains(worldFrom(c), rel, marker)
	})

	ctx.Step(`^"([^"]*)" is executable$`, func(c context.Context, rel string) error {
		w := worldFrom(c)
		full := filepath.Join(w.env.ProjectDir, rel)
		info, err := os.Stat(full)
		if err != nil {
			w.docStepMaterialized = fmt.Sprintf("stat %s: %v", rel, err)
			return fmt.Errorf("stat %q: %w", rel, err)
		}
		mode := info.Mode().Perm()
		w.docStepMaterialized = fmt.Sprintf("%s: mode %04o", rel, mode)
		if mode&0o111 == 0 {
			return fmt.Errorf("%q is not executable (mode %04o)", rel, mode)
		}
		return nil
	})

	ctx.Step(`^"([^"]*)" does not exist$`, func(c context.Context, rel string) error {
		w := worldFrom(c)
		full := filepath.Join(w.env.ProjectDir, rel)
		_, err := os.Stat(full)
		exists := err == nil
		w.docStepMaterialized = fmt.Sprintf("stat %s: exists=%v", rel, exists)
		if exists {
			return fmt.Errorf("%q unexpectedly exists", rel)
		}
		return nil
	})

	ctx.Step(`^"([^"]*)" carries the "([^"]*)" skill's content, not the builtin command's prose$`, func(c context.Context, dir, name string) error {
		w := worldFrom(c)
		rel := filepath.Join(dir, "SKILL.md")
		body, err := w.env.ReadFile(rel)
		if err != nil {
			return fmt.Errorf("read %q: %w", rel, err)
		}
		w.docStepMaterialized = fmt.Sprintf("%s:\n%s", rel, strings.TrimSpace(body))
		if !strings.Contains(body, "SKILL-MARKER-"+name+"-") {
			return fmt.Errorf("materialized %q does not carry the skill's own marker — got:\n%s", rel, body)
		}
		// discover.md's own distinctive line (resources/commands/discover.md) —
		// its presence would mean the builtin command won instead of the skill.
		if strings.Contains(body, "Scan the current project and discover matching ctxloom content") {
			return fmt.Errorf("materialized %q still carries the builtin command's prose — the skill did not win:\n%s", rel, body)
		}
		return nil
	})

	ctx.Step(`^ctxloom warned that the skill won over the command of the same name$`, func(c context.Context) error {
		w := worldFrom(c)
		out := w.env.LastOutput()
		var line string
		for _, l := range strings.Split(out, "\n") {
			if strings.Contains(l, "wins over command of the same name") {
				line = l
				break
			}
		}
		w.docStepMaterialized = line
		if line == "" {
			return fmt.Errorf("materialize output does not contain the skill-wins warning; output:\n%s", out)
		}
		return nil
	})

	ctx.Step(`^the import reports the signature as (\S+)$`, func(c context.Context, want string) error {
		w := worldFrom(c)
		if code := w.env.LastExitCode(); code != 0 {
			return fmt.Errorf("`ctxloom skill import` exited %d, want 0; output:\n%s", code, w.env.LastOutput())
		}
		var doc map[string]any
		out := w.env.LastOutput()
		if err := json.Unmarshal([]byte(out), &doc); err != nil {
			return fmt.Errorf("parse `ctxloom skill import --format json` output: %w; output:\n%s", err, out)
		}
		state, _ := doc["signature_state"].(string)
		w.docStepMaterialized = fmt.Sprintf("signature_state: %s", state)
		if want == "verified" {
			if state != "verified" {
				return fmt.Errorf("signature_state = %q, want %q", state, want)
			}
			return nil
		}
		if !strings.HasPrefix(state, want) {
			return fmt.Errorf("signature_state = %q, want prefix %q", state, want)
		}
		return nil
	})

	ctx.Step(`^the imported files under "([^"]*)" match the originally-authored "([^"]*)", byte for byte$`, func(c context.Context, importedRef, origRef string) error {
		w := worldFrom(c)
		importedBundle, name, err := splitSkillRef(importedRef)
		if err != nil {
			return err
		}
		origBundle, origName, err := splitSkillRef(origRef)
		if err != nil {
			return err
		}
		importedDir := skillPackageDir(w, importedBundle, name)
		origDir := skillPackageDir(w, origBundle, origName)

		var evidence []string
		for _, rel := range []string{"SKILL.md", "scripts/run.sh"} {
			origData, err := os.ReadFile(filepath.Join(origDir, filepath.FromSlash(rel)))
			if err != nil {
				return fmt.Errorf("read original %s/%s: %w", origDir, rel, err)
			}
			gotData, err := os.ReadFile(filepath.Join(importedDir, filepath.FromSlash(rel)))
			if err != nil {
				return fmt.Errorf("read imported %s/%s: %w", importedDir, rel, err)
			}
			// Comparing two empty reads is trivially "identical" --
			// a vacuously true comparison on empty input. Require real content
			// before trusting the byte-for-byte claim.
			if len(origData) == 0 {
				return fmt.Errorf("original %s/%s is 0 bytes -- nothing to compare against", origDir, rel)
			}
			if string(origData) != string(gotData) {
				return fmt.Errorf("imported %s does not match the originally-authored bytes:\n--- original ---\n%s\n--- imported ---\n%s", rel, origData, gotData)
			}
			evidence = append(evidence, fmt.Sprintf("%s: %d bytes, identical", rel, len(gotData)))
		}
		w.docStepMaterialized = strings.Join(evidence, "\n")
		return nil
	})
}
