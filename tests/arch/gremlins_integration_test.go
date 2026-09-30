//go:build arch

package arch

import (
	"bufio"
	"bytes"
	"go/scanner"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestArch_GremlinsRunsMutantsPerPackage pins gremlins to integration mode
// OFF: .gremlins.yaml's unleash.integration must be an explicit false, and no
// gremlins invocation in the repo may turn it back on with --integration/-i.
//
// Invariant: a mutant must only ever run under its own package's tests.
// internal/adapters/spawn's startrunner tests call isolation's
// HostRunner.Kill, which performs the real /proc session sweep UNSCOPED inside
// spawn's test binary. That is safe only because gremlins, with integration
// off, runs each mutant against its own package's tests alone. With
// integration on, an isolation killSession mutant (a negated session filter,
// say) runs under spawn's tests and SIGKILLs every process in the CI
// container — which has already happened once, via isolation's own tests
// before they were scoped.
//
// The key must be PRESENT, not merely absent-and-defaulted: gremlins'
// default is false today, but a default is the upstream's to change on any
// upgrade, and nothing here would notice. The flag overrides the key, so the
// key alone pins nothing.
func TestArch_GremlinsRunsMutantsPerPackage(t *testing.T) {
	root := moduleRoot(t)

	t.Run("config key is explicitly false", func(t *testing.T) {
		raw, err := os.ReadFile(filepath.Join(root, ".gremlins.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		var cfg struct {
			Unleash struct {
				Integration *bool `yaml:"integration"`
			} `yaml:"unleash"`
		}
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			t.Fatalf("parse .gremlins.yaml: %v", err)
		}
		switch integration := cfg.Unleash.Integration; {
		case integration == nil:
			t.Fatal(".gremlins.yaml has no unleash.integration key — set it to false explicitly " +
				"(it must be nested under unleash:, where gremlins reads it)")
		case *integration:
			t.Fatal(".gremlins.yaml sets unleash.integration: true — a mutant in isolation's " +
				"session sweep would then run under other packages' tests and can SIGKILL " +
				"every process on the runner; set it back to false")
		}
	})

	t.Run("no invocation passes the integration flag", func(t *testing.T) {
		sc := scanGremlinsInvocations(t, root)
		// Both shapes exist today; a scan that finds neither kind has stopped
		// looking, and would pass for that reason alone.
		if sc.shellInvocations == 0 || sc.goArgvFiles == 0 {
			t.Fatalf("scan found %d shell invocation(s) and %d Go argv file(s) — both must be non-zero, "+
				"or the scanner no longer recognises how gremlins is launched", sc.shellInvocations, sc.goArgvFiles)
		}
		t.Logf("%d shell invocation(s), %d Go argv file(s), recipes forwarding to gremlins: %v",
			sc.shellInvocations, sc.goArgvFiles, sc.recipes)
		for _, hit := range sc.hits {
			t.Errorf("%s passes gremlins' integration flag — it overrides .gremlins.yaml and runs "+
				"every mutant under every package's tests; remove it", hit)
		}
	})
}

// gremlinsScan is what scanGremlinsInvocations found: each file:line that
// hands gremlins its integration flag, and how many invocations it looked at.
type gremlinsScan struct {
	hits             []string
	shellInvocations int
	goArgvFiles      int
	recipes          map[string]bool
}

// gremlinsScanRoots are where gremlins is launched from. Directories are
// walked whole; globs name the recipe and workflow files.
var gremlinsScanRoots = []string{"justfile", "build/*.justfile", ".github/workflows/*", "scripts", "tests/mutation"}

// justRecipeHeader matches a recipe definition line (not a `:=` assignment).
var justRecipeHeader = regexp.MustCompile(`^@?([A-Za-z_][A-Za-z0-9_-]*)[^:=]*:([^=]|$)`)

// scanGremlinsInvocations finds gremlins invocations two ways, because the
// repo launches it two ways:
//
//   - command lines (recipes, shell, workflows): the tokens after a `gremlins`
//     command word, and after `just <recipe>` for any recipe whose body
//     invokes gremlins — those recipes forward their arguments to it.
//   - Go argv construction: in a Go file holding the "unleash" literal, every
//     string literal, since the argv is assembled across statements.
func scanGremlinsInvocations(t *testing.T, root string) gremlinsScan {
	t.Helper()
	var files []string
	for _, r := range gremlinsScanRoots {
		matches, err := filepath.Glob(filepath.Join(root, r))
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range matches {
			err := filepath.WalkDir(m, func(p string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if !d.IsDir() {
					files = append(files, p)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		}
	}

	var sc gremlinsScan
	var textFiles []string
	for _, f := range files {
		if strings.HasSuffix(f, ".go") {
			scanGoArgv(t, root, f, &sc)
		} else {
			textFiles = append(textFiles, f)
		}
	}

	// Pass 1 derives the wrapping recipes; pass 2 judges every command line.
	recipes := map[string]bool{}
	for _, f := range textFiles {
		if base := filepath.Base(f); base != "justfile" && !strings.HasSuffix(base, ".justfile") {
			continue
		}
		recipe := ""
		for _, cmd := range commandLines(t, f) {
			if m := justRecipeHeader.FindStringSubmatch(cmd[0].line); m != nil {
				recipe = m[1]
				continue
			}
			if recipe != "" && gremlinsArgStart(cmd, nil) >= 0 {
				recipes[recipe] = true
			}
		}
	}
	for _, f := range textFiles {
		rel, _ := filepath.Rel(root, f)
		for _, cmd := range commandLines(t, f) {
			start := gremlinsArgStart(cmd, recipes)
			if start < 0 {
				continue
			}
			sc.shellInvocations++
			for _, tk := range cmd[start:] {
				if isIntegrationFlag(strings.Trim(tk.text, `"'`)) {
					sc.hits = append(sc.hits, rel+":"+strconv.Itoa(tk.lineNo))
				}
			}
		}
	}
	sc.recipes = recipes
	return sc
}

// cmdToken is one whitespace-separated word of a command line, with the
// physical line it sits on.
type cmdToken struct {
	text   string
	lineNo int
	line   string
}

// commandLines splits a text file into logical command lines (joining `\`
// continuations), dropping comment lines.
func commandLines(t *testing.T, path string) [][]cmdToken {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out [][]cmdToken
	var cur []cmdToken
	s := bufio.NewScanner(bytes.NewReader(raw))
	s.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for n := 1; s.Scan(); n++ {
		line := s.Text()
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") && len(cur) == 0 {
			continue
		}
		cont := strings.HasSuffix(trimmed, `\`)
		body := strings.TrimSuffix(trimmed, `\`)
		fields := strings.Fields(body)
		if len(fields) == 0 && len(cur) == 0 {
			continue
		}
		for _, w := range fields {
			cur = append(cur, cmdToken{text: w, lineNo: n, line: line})
		}
		if !cont {
			out = append(out, cur)
			cur = nil
		}
	}
	if err := s.Err(); err != nil {
		t.Fatal(err)
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

// gremlinsArgStart returns the index of the first argument gremlins receives
// on this command line — after a `gremlins` command word, or after
// `just <recipe>` for a recipe in recipes — or -1 when it launches none.
func gremlinsArgStart(cmd []cmdToken, recipes map[string]bool) int {
	for i, tk := range cmd {
		w := strings.Trim(tk.text, `"'`)
		if w == "gremlins" || strings.HasSuffix(w, "/gremlins") {
			return i + 1
		}
		if w == "just" && i+1 < len(cmd) && recipes[strings.Trim(cmd[i+1].text, `"'`)] {
			return i + 2
		}
	}
	return -1
}

// scanGoArgv checks every string literal of a Go file that builds a gremlins
// argv (holds the "unleash" literal).
func scanGoArgv(t *testing.T, root, path string, sc *gremlinsScan) {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	file := fset.AddFile(path, -1, len(src))
	var s scanner.Scanner
	s.Init(file, src, nil, 0)
	type lit struct {
		val  string
		line int
	}
	var lits []lit
	unleash := false
	for {
		pos, tok, text := s.Scan()
		if tok == token.EOF {
			break
		}
		if tok != token.STRING {
			continue
		}
		v, err := strconv.Unquote(text)
		if err != nil {
			continue
		}
		if v == "unleash" {
			unleash = true
		}
		lits = append(lits, lit{v, fset.Position(pos).Line})
	}
	if !unleash {
		return
	}
	sc.goArgvFiles++
	rel, _ := filepath.Rel(root, path)
	for _, l := range lits {
		if isIntegrationFlag(l.val) {
			sc.hits = append(sc.hits, rel+":"+strconv.Itoa(l.line))
		}
	}
}

// isIntegrationFlag reports whether an argv word is gremlins' integration
// flag, in either spelling, bare or with a value.
func isIntegrationFlag(w string) bool {
	return w == "-i" || w == "--integration" ||
		strings.HasPrefix(w, "-i=") || strings.HasPrefix(w, "--integration=")
}
