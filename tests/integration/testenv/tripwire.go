package testenv

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// The spawn gate (AssertChildEnvSandboxed) stops a child being handed the real
// home; this tripwire checks afterwards that nothing got there anyway. A suite
// reads the real home's engine and ctxloom registries before it runs and again
// after, and fails when a name appeared that points under one of its own temp
// roots: the trace a test child leaves when it ran against the real home. Real
// sessions run on the same machine while a suite does, so an unrelated new
// entry is not this suite's and is ignored; and a machine with none of these
// files (CI) simply has nothing to compare.
//
// Only NAMES are read: registry keys and directory entries. The files carry
// credentials (~/.claude.json holds the account's login), so no value is
// kept, logged or printed. The one field read beyond a name is a NEW session
// record's project_dir, because a session is named by a harp, and a harp says
// nothing about where it ran.

// homeSource is one place in a home a run leaves names.
type homeSource string

const (
	// The projects map claude keeps in its global config, keyed by project
	// path; with CLAUDE_CONFIG_DIR set to <home>/.claude it keeps it there
	// instead (claude.InstanceConfigFileName names both files).
	srcClaudeProjects       homeSource = "~/.claude.json projects"
	srcClaudeConfigProjects homeSource = "~/.claude/.claude.json projects"
	// claude's per-project transcript directories (claude.TranscriptsDirName),
	// each named for a project path with every non-alphanumeric byte made '-'.
	srcClaudeTranscripts homeSource = "~/.claude/projects"
	// ctxloom's session records and coordinator roots, named by harp.
	srcSessions homeSource = "~/.ctxloom/sessions"
	srcCoord    homeSource = "~/.ctxloom/coord"
	// ctxloom's project registry (taskspaths.ProjectRegistryPath): one entry
	// per project path.
	srcProjectRegistry homeSource = "~/.ctxloom/projects/index.yaml"
)

// homeState is the names each source held at one reading. A source that could
// not be read (not merely absent) is recorded in unreadable instead.
type homeState struct {
	names      map[homeSource]map[string]bool
	unreadable map[homeSource]error
}

// readHomeState reads every source's names under home. An absent file or
// directory is an empty source, never an error.
func readHomeState(home string) homeState {
	ctxloomHome := filepath.Join(home, paths.AppDirName)
	readers := map[homeSource]func() ([]string, error){
		srcClaudeProjects:       func() ([]string, error) { return claudeProjectKeys(filepath.Join(home, ".claude.json")) },
		srcClaudeConfigProjects: func() ([]string, error) { return claudeProjectKeys(filepath.Join(home, ".claude", ".claude.json")) },
		srcClaudeTranscripts:    func() ([]string, error) { return dirEntryNames(filepath.Join(home, ".claude", "projects")) },
		srcSessions:             func() ([]string, error) { return dirEntryNames(filepath.Join(ctxloomHome, paths.SessionsDir)) },
		srcCoord:                func() ([]string, error) { return dirEntryNames(filepath.Join(ctxloomHome, paths.CoordDirName)) },
		srcProjectRegistry: func() ([]string, error) {
			return registryProjectPaths(filepath.Join(ctxloomHome, "projects", "index.yaml"))
		},
	}
	st := homeState{names: map[homeSource]map[string]bool{}, unreadable: map[homeSource]error{}}
	for src, read := range readers {
		names, err := read()
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			st.unreadable[src] = err
			continue
		}
		set := make(map[string]bool, len(names))
		for _, n := range names {
			set[n] = true
		}
		st.names[src] = set
	}
	return st
}

// claudeProjectKeys returns the keys of the projects map in the claude config
// file at path. The values are decoded only as far as the JSON decoder must to
// skip them, and dropped.
func claudeProjectKeys(path string) ([]string, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // a fixed path under the guarded home
	if err != nil {
		return nil, err
	}
	var doc struct {
		Projects map[string]json.RawMessage `json:"projects"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		// The decoder's message can quote the bytes it choked on; report the
		// file, never its contents.
		return nil, fmt.Errorf("%s does not parse as JSON", path)
	}
	keys := make([]string, 0, len(doc.Projects))
	for k := range doc.Projects {
		keys = append(keys, k)
	}
	return keys, nil
}

// registryProjectPaths returns the project paths ctxloom's registry at path
// lists.
func registryProjectPaths(path string) ([]string, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // a fixed path under the guarded home
	if err != nil {
		return nil, err
	}
	var reg struct {
		Projects []struct {
			Path string `yaml:"path"`
		} `yaml:"projects"`
	}
	if err := yaml.Unmarshal(raw, &reg); err != nil {
		return nil, fmt.Errorf("%s does not parse as YAML", path)
	}
	out := make([]string, 0, len(reg.Projects))
	for _, p := range reg.Projects {
		out = append(out, p.Path)
	}
	return out, nil
}

func dirEntryNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names, nil
}

// sessionProjectDir is the project_dir of the session record named harp under
// home, or "" when there is none.
func sessionProjectDir(home, harp string) string {
	raw, err := os.ReadFile(filepath.Join(home, paths.AppDirName, paths.SessionsDir, harp, paths.SessionSidecarFileName)) //nolint:gosec // a session record under the guarded home
	if err != nil {
		return ""
	}
	var rec struct {
		ProjectDir string `yaml:"project_dir"`
	}
	if yaml.Unmarshal(raw, &rec) != nil {
		return ""
	}
	return rec.ProjectDir
}

// suiteTempRoots are the path prefixes everything a suite creates lives under:
//
//   - os.TempDir()/ctxloom-integration-: every TestEnvironment's Root
//     (NewTestEnvironment), in both suites;
//   - GOTMPDIR, when set, and os.TempDir()/Test otherwise: where t.TempDir()
//     allocates (paths.UnsandboxedHomeError's testTempRoots explains why
//     GOTMPDIR wins);
//   - /var/tmp/ctxloom-: the justfile's go_tmp and mutation_tmp defaults,
//     which hold the go tool's scratch and every mutant's checkout.
//
// Each is also given symlink-resolved, since a child records the path it
// was started in as the kernel reports it.
func suiteTempRoots() []string {
	tmp := os.TempDir()
	roots := []string{
		filepath.Join(tmp, strings.TrimSuffix(envRootPattern, "*")),
		filepath.Join(tmp, "Test"),
		"/var/tmp/ctxloom-",
	}
	if g := os.Getenv("GOTMPDIR"); g != "" {
		roots = append(roots, filepath.Clean(g)+string(filepath.Separator))
	}
	for _, r := range roots {
		dir, leaf := filepath.Split(r)
		if real, err := filepath.EvalSymlinks(dir); err == nil && filepath.Clean(real) != filepath.Clean(dir) {
			resolved := filepath.Join(real, leaf)
			if strings.HasSuffix(r, string(filepath.Separator)) {
				resolved += string(filepath.Separator)
			}
			roots = append(roots, resolved)
		}
	}
	return roots
}

// underTempRoot reports whether path starts with any of roots. A root is a
// prefix, not a directory: ctxloom-integration- names the start of a
// directory's name.
func underTempRoot(path string, roots []string) bool {
	if path == "" {
		return false
	}
	p := filepath.Clean(path)
	for _, r := range roots {
		if strings.HasPrefix(p, r) || p == strings.TrimSuffix(r, string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// claudeDirName is the transcript directory name claude gives the project at
// path: every byte that is not an ASCII letter or digit becomes '-'.
func claudeDirName(path string) string {
	b := []byte(path)
	for i, c := range b {
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z') {
			b[i] = '-'
		}
	}
	return string(b)
}

// homeLeaks returns, for each source, every name in after that was not in
// before and points under one of roots, described for a human. home is where
// a new session's record is read from to learn where it ran. A source the
// first reading could not read is skipped: every name in it would look new,
// and a home that already holds old traces would fail every run.
func homeLeaks(home string, before, after homeState, roots []string) []string {
	transcriptRoots := make([]string, len(roots))
	for i, r := range roots {
		transcriptRoots[i] = claudeDirName(r)
	}
	var leaks []string
	for src, names := range after.names {
		prior, read := before.names[src]
		if !read {
			continue
		}
		for name := range names {
			if prior[name] {
				continue
			}
			switch src {
			case srcClaudeProjects, srcClaudeConfigProjects, srcProjectRegistry:
				if underTempRoot(name, roots) {
					leaks = append(leaks, fmt.Sprintf("%s: %s", src, name))
				}
			case srcClaudeTranscripts:
				for _, r := range transcriptRoots {
					if strings.HasPrefix(name, r) {
						leaks = append(leaks, fmt.Sprintf("%s: %s", src, name))
						break
					}
				}
			case srcSessions, srcCoord:
				if dir := sessionProjectDir(home, name); underTempRoot(dir, roots) {
					leaks = append(leaks, fmt.Sprintf("%s: %s (project %s)", src, name, dir))
				}
			}
		}
	}
	sort.Strings(leaks)
	return leaks
}

// GuardRealHome runs a suite — run is its testing.M's Run — between two
// readings of the real home (guardedHome) and returns its exit code, made
// non-zero, with the reason on stderr, when the second reading holds a name
// the first did not that points under one of the suite's temp roots. Both
// suites' TestMains call it.
func GuardRealHome(run func() int) int {
	home := guardedHome()
	if home == "" {
		return run()
	}
	return guardHome(home, suiteTempRoots(), run, os.Stderr)
}

// guardHome is GuardRealHome against an explicit home and roots, reporting to
// w.
func guardHome(home string, roots []string, run func() int, w io.Writer) int {
	before := readHomeState(home)
	code := run()
	after := readHomeState(home)
	for _, st := range []homeState{before, after} {
		for src, err := range st.unreadable {
			_, _ = fmt.Fprintf(w, "real-home tripwire: could not read %s, so it was not checked: %v\n", src, err)
		}
	}
	leaks := homeLeaks(home, before, after, roots)
	if len(leaks) == 0 {
		return code
	}
	_, _ = fmt.Fprintf(w, "\nFAIL: this run left traces of a test in the REAL home %s:\n", home)
	for _, l := range leaks {
		_, _ = fmt.Fprintf(w, "  %s\n", l)
	}
	_, _ = fmt.Fprintf(w, "A child process ran against the real home instead of a sandbox: some spawn bypassed testenv's gate "+
		"(AssertChildEnvSandboxed) or was handed a real-home path. Nothing was removed; the entries above are the developer's to clean.\n")
	if code == 0 {
		code = 1
	}
	return code
}
