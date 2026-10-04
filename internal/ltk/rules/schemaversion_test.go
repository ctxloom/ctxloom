package rules

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/schemaver"
)

func current() string { return schemaver.Key + ": " + strconv.Itoa(configKind.Current()) + "\n" }

func TestParse_LoadsEveryAcceptedVersionSpelling(t *testing.T) {
	for name, doc := range map[string]string{
		"current":         current() + "rules: []\n",
		"legacy key":      "version: 1\nrules: []\n",
		"keyless":         "rules: []\n",
		"empty":           "",
		"comment-only":    "# nothing yet\n",
		"bare document":   "---\n",
		"defaults only":   current() + "defaults:\n  on_parse_error: allow\n",
		"legacy, a rule":  "version: 1\nrules:\n  - id: a\n    match: { command: [rm] }\n    message: no\n",
		"current, a rule": current() + "rules:\n  - id: a\n    match: { command: [rm] }\n    message: no\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(doc)); err != nil {
				t.Fatalf("Parse(%q): %v", doc, err)
			}
		})
	}
}

// The version gate runs on the raw bytes BEFORE the removed-form check and
// the strict decode: a newer file is refused as newer, not for carrying a
// key or form this binary does not know — which is exactly what a newer
// format would carry.
func TestParse_NewerIsRefusedBeforeAnythingElseLooks(t *testing.T) {
	newer := schemaver.Key + ": " + strconv.Itoa(configKind.Current()+1) + "\n" +
		"from_the_future: true\n" +
		"rules:\n  - id: a\n    match: { path: [x] }\n    message: no\n"
	_, err := Parse([]byte(newer))
	if !errors.Is(err, schemaver.ErrNewer) {
		t.Fatalf("want ErrNewer, got %v", err)
	}
	var ve *schemaver.VersionError
	if !errors.As(err, &ve) || ve.Found != configKind.Current()+1 || ve.Current != configKind.Current() {
		t.Fatalf("want a VersionError naming both numbers, got %#v", err)
	}
}

func TestParse_UnreadableVersionIsRefusedAsSuch(t *testing.T) {
	_, err := Parse([]byte(schemaver.Key + ": banana\nrules: []\n"))
	if !errors.Is(err, schemaver.ErrUnreadable) {
		t.Fatalf("want ErrUnreadable, got %v", err)
	}
}

// A typo is still a typo: tolerating schema_version must not loosen the
// strict decode for anything else.
func TestParse_StrictDecodeStillRefusesUnknownKeys(t *testing.T) {
	if _, err := Parse([]byte(current() + "rulez: []\n")); err == nil {
		t.Fatal("an unknown top-level key must still be refused")
	}
}

func TestLoad_ReportsTheMigration(t *testing.T) {
	dir := t.TempDir()
	for name, tc := range map[string]struct {
		doc     string
		changed bool
	}{
		"legacy key is renamed": {"version: 1\nrules: []\n", true},
		"keyless is stamped":    {"rules: []\n", true},
		"current is untouched":  {current() + "rules: []\n", false},
	} {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(dir, name+".yaml")
			if err := os.WriteFile(p, []byte(tc.doc), 0o600); err != nil {
				t.Fatal(err)
			}
			_, r, err := Load(p)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got := len(r.Applied) > 0; got != tc.changed {
				t.Fatalf("changed = %v, want %v (applied %v)", got, tc.changed, r.Applied)
			}
			if r.To != configKind.Current() {
				t.Fatalf("To = %d, want %d", r.To, configKind.Current())
			}
			if !tc.changed && string(r.Data) != tc.doc {
				t.Fatalf("a current file must come back byte-identical, got %q", r.Data)
			}
		})
	}
}
