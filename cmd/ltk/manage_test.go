package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/ltk/rules"
)

// writeFile's target is the user's real .claude/settings.json /
// .agents/hooks.json — files holding unrelated configuration. An engine bug
// returning zero bytes must not be made durable: iox.WriteFileAtomic would
// faithfully commit the truncation and the caller would print "installed".
func TestWriteFile_RefusesToTruncateSettingsToZeroBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	original := `{"permissions":{"allow":["Bash(ls:*)"]}}`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, empty := range [][]byte{nil, {}} {
		if err := writeFile(path, empty); err == nil {
			t.Fatal("writeFile accepted an empty payload over an existing settings file")
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != original {
			t.Fatalf("the user's settings were damaged: %q", got)
		}
	}
}

// The rule set ltk ships must actually contain rules. loadout_test only ever
// RANGES over defaultCfg.Rules, which is vacuously true on zero rules — so
// nothing today would notice docs/DEFAULTS.md losing its fences and
// extract-defaults emitting a header-only file.
func TestDefaultRules_ShipWithRules(t *testing.T) {
	cfg, err := rules.Parse([]byte(defaultRules))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Rules) == 0 {
		t.Fatal("the shipped default rule set parses to zero rules: `ltk manage install` would install a guard that gates nothing")
	}
}

// ...and scaffoldConfig must refuse to install such a set rather than report
// "wrote rules file" over a guard that gates nothing. rules.Parse tolerates an
// empty document (io.EOF), so a rule-less file is a VALID allow-all config —
// the parser will never object on the user's behalf.
func TestScaffoldConfig_RefusesRuleLessDefaults(t *testing.T) {
	restore := defaultRules
	t.Cleanup(func() { defaultRules = restore })
	defaultRules = "version: 1\n" // parses fine; gates nothing

	dir := t.TempDir()
	path := filepath.Join(dir, ".ltk", "config.yaml")
	if err := scaffoldConfig(path, true, false); err == nil {
		t.Fatal("scaffoldConfig installed a rule set with zero rules and reported success")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("a refused scaffold must not leave a file behind (stat err: %v)", err)
	}

	// --no-default-rules asks for the deliberately empty template: that is a
	// legitimate empty state and must still work.
	if err := scaffoldConfig(path, false, false); err != nil {
		t.Fatalf("--no-default-rules must still scaffold the empty template: %v", err)
	}
}

// scaffoldConfig must never silently clobber an existing rules file: without
// force it keeps the file (no backup); with force it backs the old one up first.
func TestScaffoldConfigPreservesExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".ltk", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	mine := "version: 1\nrules: []  # my edited rules\n"
	if err := os.WriteFile(path, []byte(mine), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := scaffoldConfig(path, true, false); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != mine {
		t.Errorf("non-force scaffold overwrote the user's config:\n%s", got)
	}
	if _, err := os.Stat(path + ".bak"); err == nil {
		t.Error("non-force scaffold should not write a .bak")
	}
}

func TestScaffoldConfigForceBacksUp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".ltk", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	mine := "version: 1\nrules: []  # my edited rules\n"
	if err := os.WriteFile(path, []byte(mine), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := scaffoldConfig(path, true, true); err != nil {
		t.Fatal(err)
	}
	bak, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatalf("force scaffold should back up the old file: %v", err)
	}
	if string(bak) != mine {
		t.Errorf("backup does not match the original:\n%s", bak)
	}
	if got, _ := os.ReadFile(path); string(got) == mine {
		t.Error("force scaffold should have overwritten the config")
	}
}

func TestScaffoldConfigWritesWhenAbsent(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".ltk", "config.yaml")
	if err := scaffoldConfig(path, false, false); err != nil { // minimal template
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("scaffold should create the file when absent: %v", err)
	}
}

// writeFile replaces settings atomically (temp file + rename): the new content
// lands whole, an existing file's mode is preserved, and no temp file is left
// behind — an interrupt mid-write must never truncate the user's settings.
func TestWriteFileAtomicReplace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude", "settings.json")

	if err := writeFile(path, []byte(`{"a":1}`)); err != nil {
		t.Fatalf("initial write (with dir creation): %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(path, []byte(`{"a":2}`)); err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"a":2}` {
		t.Errorf("content = %s", got)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("existing mode not preserved: %v", fi.Mode().Perm())
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

// --print is a dry run: it must not scaffold the rules file (or any other
// filesystem state) while printing the would-be settings.
func TestInstallPrintDoesNotScaffoldRules(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	c := newInstallCmd()
	c.SetArgs([]string{"--engine", "claude-code", "--print"})
	if err := c.Execute(); err != nil {
		t.Fatalf("install --print: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".ltk")); !os.IsNotExist(err) {
		t.Error("install --print scaffolded .ltk/ — a print run must not write")
	}
	if _, err := os.Stat(filepath.Join(dir, ".claude")); !os.IsNotExist(err) {
		t.Error("install --print wrote settings — a print run must not write")
	}
}
