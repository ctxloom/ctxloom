package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/codex"
	"github.com/ctxloom/ctxloom/internal/paths"
	harpid "github.com/ctxloom/ctxloom/internal/shared/harp"
)

// doctorCodexHomeMarker is the DOCTOR-CHECK-* vocabulary entry for codex's
// home-keyed surfaces — the report D6 rules on.
const doctorCodexHomeMarker = "DOCTOR-CHECK-CODEXHOME-n4"

// doctorCheckCodexHome answers the one question codex's declared absence
// creates and nothing else in this report can: "my hooks and MCP servers are
// not in my project — so WHERE are they?"
//
// It reports BOTH homes, because the honest answer is two places (D6, ruled):
//
//	(a) the REAL host home ($CODEX_HOME, else ~/.codex). This is the more
//	    useful half: `host` is the default, so an agent with no binding, an
//	    undeclared one, or an explicit `config_home: host` runs against exactly
//	    this directory (D2). ctxloom never WRITES it — reading it is a
//	    different act, and reporting where a user's own configuration lives is
//	    the whole point of a diagnostic.
//	(b) the most recent PER-SESSION INSTANCE under .ctxloom/state/<harp>/home,
//	    if one is on disk. This half is genuinely arguable and was nearly
//	    dropped, for a good reason: a stale instance is a photograph of a
//	    session that has ended, and a reader who mistakes it for live
//	    configuration will edit a directory that is about to be deleted. It is
//	    reported anyway because seeing what ctxloom actually generated is the
//	    only way to check the generation — and the mislead is answered by
//	    LABELLING it, every time, with the harp it belonged to, how old it is,
//	    and that the next session rebuilds it from scratch.
//
// PURELY READ-ONLY, and deliberately so: doctor never creates a home, and an
// absent instance is the NORMAL state (no session is running), not a finding.
// The whole check is doctorInfo — there is nothing here to fix.
func doctorCheckCodexHome(projectDir string) doctorCheck {
	parts := []string{
		fmt.Sprintf("codex reads hooks, MCP servers, prompts and skills only from $CODEX_HOME, and ctxloom writes no durable project copy (%s)", codex.LaunchOnlySettingsReason),
		doctorCodexHostHomeLine(),
	}

	instance, err := doctorMostRecentCodexInstance(projectDir)
	switch {
	case err != nil:
		// "I could not look" is not "there is nothing there".
		parts = append(parts, fmt.Sprintf("could not scan this project for per-session instances: %v", err))
	case instance.path == "":
		parts = append(parts, fmt.Sprintf(
			"no per-session instance under %s — one is created at launch for an agent whose binding declares `config_home: project`, and rebuilt fresh next session",
			filepath.Join(paths.AppDirName, paths.StateDir, "<harp>", paths.SessionHomeDirName)))
	default:
		parts = append(parts, fmt.Sprintf(
			"most recent per-session instance: %s (harp %s, last written %s) — NOT live configuration: it belongs to that session and is rebuilt fresh next session",
			instance.path, instance.harp, doctorAgeString(time.Since(instance.modTime))))
	}
	return doctorCheck{Marker: doctorCodexHomeMarker, Status: doctorInfo, Detail: strings.Join(parts, "; ")}
}

// doctorCodexHostHomeLine reports half (a): where codex itself resolves its
// home, and whether anything is there. codex.GlobalHome is codex's OWN
// precedence ($CODEX_HOME, else ~/.codex), so this names the directory codex
// would actually read rather than a guess at it.
func doctorCodexHostHomeLine() string {
	home, err := codex.GlobalHome()
	if err != nil {
		return fmt.Sprintf("host home (used by any agent with no binding, an undeclared one, or `config_home: host`): UNRESOLVABLE (%v)", err)
	}
	state := "present"
	if info, serr := os.Stat(home); serr != nil {
		state = "not created yet"
	} else if !info.IsDir() {
		state = "present but NOT a directory"
	}
	return fmt.Sprintf("host home (used by any agent with no binding, an undeclared one, or `config_home: host`): %s (%s)", home, state)
}

// codexInstanceRef is one per-session codex home found on disk.
type codexInstanceRef struct {
	harp    string
	path    string
	modTime time.Time
}

// doctorMostRecentCodexInstance finds the newest per-session codex home in this
// project, by the instance directory's own mtime. An empty path means none
// exists, which is the normal case and never an error.
//
// RECENCY IS BY MTIME, NOT BY NAME. Harps are random word triples with no
// ordering, and the session index is a separate store this read deliberately
// does not consult: the question here is "what is ON DISK", and an instance
// whose harp the index has forgotten is precisely the one a reader most needs
// to see. Ties are broken by harp so the report is stable rather than
// dependent on directory order.
//
// It CREATES NOTHING — not the state dir, not the instance, not a parent. Every
// path here is stat'd or read, never made.
func doctorMostRecentCodexInstance(projectDir string) (codexInstanceRef, error) {
	var newest codexInstanceRef
	if projectDir == "" {
		return newest, nil
	}
	stateDir := paths.StatePath(filepath.Join(projectDir, paths.AppDirName))
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		if os.IsNotExist(err) {
			return newest, nil
		}
		return newest, fmt.Errorf("read %s: %w", stateDir, err)
	}
	// Sorted so the tie-break below is deterministic; os.ReadDir already sorts
	// by name, but relying on that silently is how a report starts reshuffling
	// the day it is replaced.
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	for _, e := range entries {
		if !e.IsDir() || harpid.Validate(e.Name()) != nil {
			continue
		}
		// The codex leaf specifically: state/<harp>/home also hosts claude's and
		// kiro's instances, and reporting one of those as codex's would be worse
		// than reporting nothing.
		dir := filepath.Join(stateDir, e.Name(), paths.SessionHomeDirName, codex.ConfigDirName)
		info, serr := os.Stat(dir)
		if serr != nil || !info.IsDir() {
			continue
		}
		if newest.path == "" || info.ModTime().After(newest.modTime) {
			newest = codexInstanceRef{harp: e.Name(), path: dir, modTime: info.ModTime()}
		}
	}
	return newest, nil
}

// doctorAgeString renders an age the way a person reads one — the coarsest unit
// that is still true, never a Go duration string (2h13m47.9s is a measurement,
// not an answer to "is this instance stale?").
func doctorAgeString(d time.Duration) string {
	if d < 0 {
		// A clock skew or a file stamped in the future. Saying so beats
		// rendering a negative age as though it were an age.
		return "in the future (clock skew?)"
	}
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
