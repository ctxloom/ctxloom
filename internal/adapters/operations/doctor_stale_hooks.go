package operations

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/shared/exectoken"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// doctorFixRemedy is the command that removes what doctor's fixable checks
// find.
const doctorFixRemedy = "ctxloom doctor --fix"

// errNoHookVerbs refuses a stale-hook scan with no verb set: every
// `ctxloom hook` entry would read as stale, and the fix would delete the
// live ones.
var errNoHookVerbs = errors.New("no hook subcommand set was supplied, so no hook entry can be judged stale")

// StaleHookEntry is one engine-settings hook entry that invokes a
// `ctxloom hook <verb>` this build has no subcommand for: a previous
// ctxloom's leftover, which the engine runs at its event and which fails
// there every time.
type StaleHookEntry struct {
	File  string `json:"file"`
	Event string `json:"event"`
	Verb  string `json:"verb"`
}

func (e StaleHookEntry) String() string {
	return fmt.Sprintf("%s: %s runs `ctxloom hook %s`", e.File, e.Event, e.Verb)
}

// DoctorFixRequest scopes one `doctor --fix`.
type DoctorFixRequest struct {
	// HookVerbs is every `ctxloom hook` subcommand this build has, read off
	// the CLI's own command tree by the frontend. Required.
	HookVerbs []string
}

// DoctorFixResult is what one fix removed.
type DoctorFixResult struct {
	Removed []StaleHookEntry `json:"removed"`
}

// DoctorFix removes what doctor's fixable checks find: today, the settings
// hook entries that invoke a `ctxloom hook` verb this build lacks. Each
// entry is taken out of its file byte-preservingly; everything else in the
// file stays.
func DoctorFix(ctx context.Context, app *App, req DoctorFixRequest) (DoctorFixResult, error) {
	cfg, _ := app.Config(ctx)
	removed, err := removeStaleHooks(app.Engines(), doctorProjectDir(cfg), configFS(cfg), req.HookVerbs)
	return DoctorFixResult{Removed: removed}, err
}

// doctorCheckStaleHooks reports every settings hook entry that invokes
// `ctxloom hook <verb>` where verb is not a hook subcommand this build has.
//
// THE RULE IS GENERAL BY DESIGN. It carries no list of retired names: the
// live verbs come from the caller (the CLI's own command tree), so a hook
// renamed tomorrow makes the old spelling's entries findable with no edit
// here. The files are each registered engine's own project and user-global
// settings, as its HookGlobalScope declares them; an engine that declares
// none has no settings file for this check to read.
func doctorCheckStaleHooks(reg engine.Registry, projectDir string, fsys afero.Fs, verbs []string) DoctorCheck {
	const marker = "DOCTOR-CHECK-STALE-HOOKS-n5"
	if len(verbs) == 0 {
		return DoctorCheck{Marker: marker, Status: DoctorInfo, Detail: errNoHookVerbs.Error()}
	}
	var stale []StaleHookEntry
	var unreadable []string
	for _, file := range staleHookFiles(reg, projectDir, &unreadable) {
		scan, err := scanStaleHooks(fsys, file, verbs)
		if err != nil {
			unreadable = append(unreadable, err.Error())
			continue
		}
		stale = append(stale, scan.entries...)
	}
	sort.Strings(unreadable)
	switch {
	case len(stale) > 0:
		named := make([]string, len(stale))
		for i, e := range stale {
			named[i] = e.String()
		}
		detail := fmt.Sprintf(
			"%d settings hook entr(y/ies) invoke a `ctxloom hook` subcommand this ctxloom does not have; the engine runs each at its event and it fails every time: %s",
			len(stale), strings.Join(named, "; "))
		if len(unreadable) > 0 {
			detail += "; could not read: " + strings.Join(unreadable, ", ")
		}
		return DoctorCheck{Marker: marker, Status: DoctorWarn, Detail: detail, Remedy: doctorFixRemedy}
	case len(unreadable) > 0:
		return DoctorCheck{Marker: marker, Status: DoctorWarn, Detail: fmt.Sprintf(
			"%d settings file(s) could not be read, so their ctxloom hook entries are unverified: %s",
			len(unreadable), strings.Join(unreadable, ", "))}
	default:
		return DoctorCheck{Marker: marker, Status: DoctorOK,
			Detail: "every settings hook entry that runs `ctxloom hook` names a subcommand this ctxloom has"}
	}
}

// removeStaleHooks takes every stale entry doctorCheckStaleHooks would
// report out of its file, writing only files that change.
func removeStaleHooks(reg engine.Registry, projectDir string, fsys afero.Fs, verbs []string) ([]StaleHookEntry, error) {
	if len(verbs) == 0 {
		return nil, errNoHookVerbs
	}
	var removed []StaleHookEntry
	var unreadable []string
	var errs []error
	for _, file := range staleHookFiles(reg, projectDir, &unreadable) {
		scan, err := scanStaleHooks(fsys, file, verbs)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if len(scan.pointers) == 0 {
			continue
		}
		if err := rewriteWithout(fsys, file, scan); err != nil {
			errs = append(errs, err)
			continue
		}
		removed = append(removed, scan.entries...)
	}
	for _, u := range unreadable {
		errs = append(errs, errors.New(u))
	}
	return removed, errors.Join(errs...)
}

// rewriteWithout writes file back without scan's pointers, keeping its mode.
func rewriteWithout(fsys afero.Fs, file string, scan staleHookScan) error {
	ptrs := slices.Clone(scan.pointers)
	// Document order reversed: within one array the highest index leaves
	// first, so every earlier pointer still names what it named.
	slices.Reverse(ptrs)
	out, err := removePointers(file, scan.data, ptrs)
	if err != nil {
		return err
	}
	return safefs.WriteFileKeepMode(fsys, file, out, file)
}

// staleHookFiles is every registered engine's project and user-global
// settings file, in registry order, de-duplicated (the two collapse when the
// project IS the home). A path an engine fails to resolve is appended to
// unreadable rather than silently skipped.
func staleHookFiles(reg engine.Registry, projectDir string, unreadable *[]string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, name := range EngineNames(reg) {
		h, ok := agent.HostedIn(reg, name)
		if !ok {
			continue
		}
		scope, ok := h.HookGlobalScope()
		if !ok || scope.Paths == nil {
			continue
		}
		project, global, err := scope.Paths(projectDir)
		if err != nil {
			*unreadable = append(*unreadable, fmt.Sprintf("%s settings (%v)", name, err))
		}
		if projectDir != "" {
			add(project)
		}
		add(global)
	}
	return out
}

// staleHookScan is one settings file's stale entries and the pointers that
// remove them.
type staleHookScan struct {
	data     []byte
	entries  []StaleHookEntry
	pointers []string
}

// scanStaleHooks reads one settings file. An absent file is the common case
// and scans empty.
func scanStaleHooks(fsys afero.Fs, file string, verbs []string) (staleHookScan, error) {
	data, err := afero.ReadFile(fsys, file)
	if err != nil {
		if os.IsNotExist(err) {
			return staleHookScan{}, nil
		}
		return staleHookScan{}, fmt.Errorf("%s (%v)", file, err)
	}
	root, err := decodeSurface(file, data)
	if err != nil {
		return staleHookScan{}, fmt.Errorf("%s (%v)", file, err)
	}
	w := staleHookWalk{file: file, live: map[string]bool{}}
	for _, v := range verbs {
		w.live[v] = true
	}
	_, ptrs := w.visit(root, "")
	return staleHookScan{data: data, entries: w.found, pointers: ptrs}, nil
}

// staleHookWalk finds stale entries anywhere in a decoded settings document.
// It walks the whole document rather than one engine's hook-table path, so
// no engine's table shape is written down here.
type staleHookWalk struct {
	file  string
	live  map[string]bool
	found []StaleHookEntry
}

// visit reports whether node consists ENTIRELY of stale entries (so its
// container should drop it whole) and, when it does not, the pointers below
// it to remove, in document order. A container that held nothing but stale
// entries goes with them — an emptied hook group, an event left with no
// hooks — while the document root, a container that is empty to begin with,
// and anything holding a single live or foreign value always stay.
func (w *staleHookWalk) visit(node any, ptr string) (wholly bool, pointers []string) {
	switch n := node.(type) {
	case map[string]any:
		if verb, ok := w.staleVerb(n); ok {
			w.found = append(w.found, StaleHookEntry{File: w.file, Event: hookEvent(ptr), Verb: verb})
			return true, nil
		}
		keys := make([]string, 0, len(n))
		for k := range n {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return w.children(len(keys), ptr, func(i int) (any, string) {
			return n[keys[i]], ptr + "/" + escapePointer(keys[i])
		})
	case []any:
		return w.children(len(n), ptr, func(i int) (any, string) {
			return n[i], fmt.Sprintf("%s/%d", ptr, i)
		})
	default:
		return false, nil
	}
}

// children folds visit over a container's n children. Scalars neither keep
// a container alive nor condemn it (a group's matcher goes with its hooks);
// only container children vote.
func (w *staleHookWalk) children(n int, ptr string, child func(int) (any, string)) (bool, []string) {
	var pointers []string
	containers, wholly := 0, 0
	for i := 0; i < n; i++ {
		v, p := child(i)
		switch v.(type) {
		case map[string]any, []any:
			containers++
		default:
			continue
		}
		if all, under := w.visit(v, p); all {
			wholly++
			pointers = append(pointers, p)
		} else {
			pointers = append(pointers, under...)
		}
	}
	if ptr != "" && containers > 0 && wholly == containers {
		return true, nil
	}
	return false, pointers
}

// staleVerb reports the verb of an entry whose command runs
// `ctxloom hook <verb>` for a verb not in the live set.
func (w *staleHookWalk) staleVerb(entry map[string]any) (string, bool) {
	cmd, ok := entry["command"].(string)
	if !ok || !exectoken.IsManaged(cmd, agent.CtxloomBinary) {
		return "", false
	}
	args := exectoken.Args(cmd)
	if extra, ok := stringSlice(entry["args"]); ok {
		args = append(args, extra...)
	}
	if len(args) < 2 || args[0] != "hook" || w.live[args[1]] {
		return "", false
	}
	return args[1], true
}

// hookEvent names the event an entry at ptr is registered for: the key below
// the first "hooks" segment, else the first segment.
func hookEvent(ptr string) string {
	segs := strings.Split(strings.TrimPrefix(ptr, "/"), "/")
	for i, s := range segs {
		if s == "hooks" && i+1 < len(segs) {
			return unescapePointer(segs[i+1])
		}
	}
	return unescapePointer(segs[0])
}

func escapePointer(key string) string {
	return strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
}

func unescapePointer(seg string) string {
	return strings.ReplaceAll(strings.ReplaceAll(seg, "~1", "/"), "~0", "~")
}
