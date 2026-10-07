package rules

import "testing"

func cfgWith(rule PathRule) *Config { return &Config{PathRules: []PathRule{rule}} }

// pathRule builds a deny path rule with a message.
func pathRule(id, message string, patterns ...string) PathRule {
	return PathRule{RuleBase: RuleBase{ID: id, Message: message}, Match: PathMatch{Path: patterns}}
}

// A path rule blocks file edits to matching files and ignores command calls.
func TestEvaluatePathBasics(t *testing.T) {
	cfg := cfgWith(pathRule("no-hand-edit-version", "bump via versionator", "VERSION"))

	deny := []string{"VERSION", "/proj/VERSION", "sub/dir/VERSION"}
	for _, p := range deny {
		if EvaluatePath(cfg, p).Allowed {
			t.Errorf("editing %q should be denied", p)
		}
	}
	allow := []string{"VERSION.md", "src/version.go", "README.md", ""}
	for _, p := range allow {
		if !EvaluatePath(cfg, p).Allowed {
			t.Errorf("editing %q should be allowed", p)
		}
	}
}

// Glob patterns match basename and full path.
func TestEvaluatePathGlobs(t *testing.T) {
	cfg := cfgWith(pathRule("no-lockfiles", "don't hand-edit generated files", "*.lock", "dist/*"))
	for _, p := range []string{"go.lock", "a/b/c.lock", "dist/ltk"} {
		if EvaluatePath(cfg, p).Allowed {
			t.Errorf("%q should be denied", p)
		}
	}
	// `*` does not cross `/` (Go path.Match), so dist/* matches one level only.
	for _, p := range []string{"lockfile", "src/dist.go", "dist/sub/x"} {
		if !EvaluatePath(cfg, p).Allowed {
			t.Errorf("%q should be allowed", p)
		}
	}
}

// Path rules carry mode/confirm/suggest like command rules.
func TestEvaluatePathModeAndConfirm(t *testing.T) {
	// confirm mode is repeatable when a window is configured.
	cfg := &Config{
		Defaults: Defaults{RepeatWindowSeconds: 30},
		PathRules: []PathRule{{
			RuleBase: RuleBase{ID: "confirmable", Mode: ModeConfirm, Suggest: "just bump"},
			Match:    PathMatch{Path: []string{"VERSION"}},
		}},
	}
	d := EvaluatePath(cfg, "VERSION")
	if d.Allowed || !d.Confirmable || d.ConfirmWindowSeconds != 30 || d.Suggest != "just bump" {
		t.Fatalf("confirm path rule: %+v", d)
	}

	// disable mode is inert.
	cfg.PathRules[0].Mode = ModeDisable
	if !EvaluatePath(cfg, "VERSION").Allowed {
		t.Error("mode:disable path rule should not fire")
	}
}

// A trailing-slash pattern blocks a whole directory subtree, at any depth and
// regardless of the absolute prefix the editing tool passes.
func TestEvaluatePathDirectorySubtree(t *testing.T) {
	cfg := cfgWith(pathRule("no-vendor-edits", "vendored code is generated", "vendor/"))
	for _, p := range []string{"vendor/x", "vendor/a/b/c.go", "/abs/proj/vendor/a/b"} {
		if EvaluatePath(cfg, p).Allowed {
			t.Errorf("editing %q should be denied (under vendor/)", p)
		}
	}
	for _, p := range []string{"vendors/x", "src/vendor.go", "/abs/vendor.txt"} {
		if !EvaluatePath(cfg, p).Allowed {
			t.Errorf("editing %q should be allowed (not under vendor/)", p)
		}
	}
}

// `**` spans directories; a repo-relative pattern still matches the absolute
// paths the editing tools pass, while `*` stays bounded to one segment.
func TestEvaluatePathDoublestar(t *testing.T) {
	cfg := cfgWith(pathRule("no-generated-go", "generated", "src/**/*.go"))
	for _, p := range []string{"src/a.go", "src/a/b/c.go", "/abs/proj/src/deep/x.go"} {
		if EvaluatePath(cfg, p).Allowed {
			t.Errorf("editing %q should be denied (src/**/*.go)", p)
		}
	}
	for _, p := range []string{"src/a.txt", "other/a.go", "src.go"} {
		if !EvaluatePath(cfg, p).Allowed {
			t.Errorf("editing %q should be allowed", p)
		}
	}
}

// ExpandSubmodules rewrites the @submodules sentinel into a directory subtree per
// submodule path; the rest of the pattern list is preserved.
func TestExpandSubmodules(t *testing.T) {
	cfg := cfgWith(pathRule("no-submodule-edits", "submodules are pinned", ".gitmodules", "@submodules"))
	_ = cfg.ExpandSubmodules([]string{"libs/foo", "third_party/bar/"})

	for _, p := range []string{"libs/foo/x.c", "/abs/third_party/bar/deep/y.h", "/p/.gitmodules"} {
		if EvaluatePath(cfg, p).Allowed {
			t.Errorf("editing %q should be denied", p)
		}
	}
	for _, p := range []string{"libs/other/x.c", "third_party/x"} {
		if !EvaluatePath(cfg, p).Allowed {
			t.Errorf("editing %q should be allowed", p)
		}
	}
}

// With no submodules, the sentinel is dropped; a rule left with no patterns is
// inert (matches nothing) rather than matching everything.
func TestExpandSubmodulesEmpty(t *testing.T) {
	cfg := cfgWith(pathRule("subs", "", "@submodules"))
	_ = cfg.ExpandSubmodules(nil)
	if !EvaluatePath(cfg, "anything/at/all.go").Allowed {
		t.Error("an unexpanded @submodules rule must match nothing")
	}
	// An unexpanded sentinel (ExpandSubmodules never called) is also inert.
	raw := cfgWith(pathRule("subs", "", "@submodules"))
	if !EvaluatePath(raw, "anything/at/all.go").Allowed {
		t.Error("a literal @submodules pattern must match nothing")
	}
}

// A malformed glob in match.path is a config error, not a silently dead rule.
func TestPathRuleRejectsBadGlob(t *testing.T) {
	_, err := Parse([]byte("schema_version: 1\npath_rules:\n  - id: bad\n    match: { path: [\"a[\"] }\n    message: m\n"))
	if err == nil {
		t.Error("an unterminated character class should be a validation error")
	}
}

func TestPathRuleParses(t *testing.T) {
	cfg, err := Parse([]byte("schema_version: 1\npath_rules:\n  - id: v\n    match: { path: [VERSION] }\n    message: use versionator\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.PathRules) != 1 || len(cfg.Rules) != 0 {
		t.Errorf("a path_rules entry must decode as a path rule: %+v", cfg)
	}
}
