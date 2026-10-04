package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/ltk/rules"
	"github.com/ctxloom/ctxloom/internal/shared/schemaver"
)

const legacyRules = "# my rules\nversion: 1\nrules: []\n"

func writeRules(t *testing.T, doc string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "rules.yaml")
	if err := os.WriteFile(p, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// runRoot drives the real command tree, so the flag is parsed exactly as the
// binary parses it. Building a fresh tree afterwards resets the process-wide
// switch for whatever runs next.
func runRoot(t *testing.T, args ...string) error {
	t.Helper()
	t.Cleanup(func() { newRootCmd() })
	root := newRootCmd()
	root.SetArgs(args)
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	return root.Execute()
}

func readFile(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRoot_WriteUpgradesIsAPersistentFlag(t *testing.T) {
	if newRootCmd().PersistentFlags().Lookup(schemaver.WriteUpgradesFlag) == nil {
		t.Fatalf("--%s must be a persistent flag on the root", schemaver.WriteUpgradesFlag)
	}
}

func TestCheck_WriteUpgradesPersistsTheMigration(t *testing.T) {
	p := writeRules(t, legacyRules)
	if err := runRoot(t, "check", "--config", p, "--command", "git status", "--format", "json", "--"+schemaver.WriteUpgradesFlag); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := yaml.Unmarshal(readFile(t, p), &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got[schemaver.Key]; !ok {
		t.Errorf("the rewritten file must declare %s, got %v", schemaver.Key, got)
	}
	if _, ok := got["version"]; ok {
		t.Errorf("the legacy key must be gone, got %v", got)
	}
	if bak := readFile(t, p+schemaver.BackupSuffix); string(bak) != legacyRules {
		t.Errorf("the backup must hold the original bytes, got %q", bak)
	}
	// The written file is current: loading it again changes nothing.
	if _, r, err := rules.Load(p); err != nil || len(r.Applied) != 0 {
		t.Errorf("the written file must load as current, applied %v err %v", r.Applied, err)
	}
}

func TestCheck_WithoutWriteUpgradesTheFileIsUntouched(t *testing.T) {
	p := writeRules(t, legacyRules)
	if err := runRoot(t, "check", "--config", p, "--command", "git status", "--format", "json"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, p); string(got) != legacyRules {
		t.Errorf("an older file must load without changing, got %q", got)
	}
	if _, err := os.Stat(p + schemaver.BackupSuffix); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("no backup without --%s: %v", schemaver.WriteUpgradesFlag, err)
	}
}

func TestCheck_NewerConfigIsRefused(t *testing.T) {
	p := writeRules(t, schemaver.Key+": 999\nrules: []\n")
	var out, diag bytes.Buffer
	err := runCheck(&out, &diag, "git status", p, "", "json")
	if !errors.Is(err, schemaver.ErrNewer) {
		t.Fatalf("want ErrNewer, got %v", err)
	}
}

// What ltk writes must already be current, or every fresh install starts
// life needing an upgrade.
func TestScaffoldedTemplatesAreCurrent(t *testing.T) {
	for _, withDefaults := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), ".ltk", "config.yaml")
		if err := scaffoldConfig(afero.NewOsFs(), path, withDefaults, false); err != nil {
			t.Fatal(err)
		}
		_, r, err := rules.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Applied) != 0 {
			t.Errorf("withDefaults=%v: the scaffolded file needed %v", withDefaults, r.Applied)
		}
	}
}
