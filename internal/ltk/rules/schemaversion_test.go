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

func TestParse_LoadsTheCurrentGeneration(t *testing.T) {
	for name, doc := range map[string]string{
		"no rules":      current() + "rules: []\n",
		"defaults only": current() + "defaults:\n  on_parse_error: allow\n",
		"a rule":        current() + "rules:\n  - id: a\n    match: { command: [rm] }\n    message: no\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(doc)); err != nil {
				t.Fatalf("Parse(%q): %v", doc, err)
			}
		})
	}
}

// A file that declares no generation is refused, never guessed at: `version`
// is not a spelling of schemaver.Key, and an empty or comment-only file
// declares nothing.
func TestParse_RefusesAFileThatDeclaresNoGeneration(t *testing.T) {
	for name, doc := range map[string]string{
		"keyless":       "rules: []\n",
		"version key":   "version: 1\nrules: []\n",
		"empty":         "",
		"comment-only":  "# nothing yet\n",
		"bare document": "---\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(doc))
			var ve *schemaver.VersionError
			if !errors.Is(err, schemaver.ErrTooOld) || !errors.As(err, &ve) || ve.Found != 0 {
				t.Fatalf("Parse(%q): want ErrTooOld at generation 0, got %v", doc, err)
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

func TestLoad_CurrentIsUntouched(t *testing.T) {
	doc := current() + "rules: []\n"
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	_, r, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(r.Applied) > 0 || r.To != configKind.Current() || string(r.Data) != doc {
		t.Fatalf("a current file must come back byte-identical and unmigrated, got %+v", r)
	}
}

// A rules file that is not YAML is its parse failure, not a version fault.
func TestParse_MalformedIsAParseFailureNotAVersionFault(t *testing.T) {
	_, err := Parse([]byte("rules: [unterminated\n"))
	if err == nil {
		t.Fatal("want a parse failure, got nil")
	}
	var ve *schemaver.VersionError
	if errors.As(err, &ve) {
		t.Fatalf("want the rules file's parse failure, got a version fault: %v", err)
	}
}
