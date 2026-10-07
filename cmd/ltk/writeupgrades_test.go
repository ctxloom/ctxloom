package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/ltk/rules"
	"github.com/ctxloom/ctxloom/internal/shared/schemaver"
)

const currentRules = "# my rules\nschema_version: 1\nrules: []\n"

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

// With nothing to migrate, --write-upgrades writes nothing: no rewrite and no
// backup.
func TestCheck_WriteUpgradesLeavesACurrentFileUntouched(t *testing.T) {
	p := writeRules(t, currentRules)
	if err := runRoot(t, "check", "--config", p, "--command", "git status", "--format", "json", "--"+schemaver.WriteUpgradesFlag); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, p); string(got) != currentRules {
		t.Errorf("a current file must not be rewritten, got %q", got)
	}
	if _, err := os.Stat(p + schemaver.BackupSuffix); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("no backup when nothing migrated: %v", err)
	}
}

func TestCheck_WithoutWriteUpgradesTheFileIsUntouched(t *testing.T) {
	p := writeRules(t, currentRules)
	if err := runRoot(t, "check", "--config", p, "--command", "git status", "--format", "json"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, p); string(got) != currentRules {
		t.Errorf("a file must load without changing, got %q", got)
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
