package engine

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// This file is the vocabulary an engine's Home() and Container() speak: how
// its global config home relocates into a session home and how the engine
// authenticates there, and how a containerized run of it is built and
// authenticated.
// The engine package authors VALUES of these types; the cells adapter reads
// them off the Engine it was handed and never imports the engine. The types
// live on the port, not in the adapter, because an engine package must be
// able to author them without linking the isolation machinery.
//
// Every optional part is a Declared slot, so "this engine has no X" is a
// stated value with a reason, never a nil that reads as either.

// HomeSpec says how the engine's config home relocates into the session
// home. The zero value is the NULL OBJECT: no var relocates anything, no
// instance config is generated — an engine that keeps no engine-global state
// returns it and the cells adapter has nothing to do.
type HomeSpec struct {
	// Vars are the env vars that relocate the home, and shared isolation
	// binds EVERY one of them (BindHome). Vars[0] names the session home
	// itself: its Subdir is the home's own leaf (launch.SessionHome). Each
	// further var names a directory beneath that home, at its Subdir, which
	// isolation creates owner-only before the engine starts. An engine whose
	// whole home moves with one var has one entry; an engine that splits
	// config, data and the like across separate vars (the XDG family)
	// contributes one entry per var.
	Vars []HomeVar
	// Auth is the engine's authentication capability (modes, the env each
	// mode launches with, minting), or Absent with the reason for an engine
	// that needs no credential. Nothing is ever copied into a session home
	// to authenticate it. Undecided is legal ONLY on the zero spec; Validate
	// refuses it once a var is declared.
	Auth Declared[Auth]
	// InstanceConfig is the engine's own generator of its top-level config
	// file inside a session home the cells adapter provisioned; nil when the
	// engine has no config file of its own.
	InstanceConfig InstanceConfigWriter
	// TranscriptStoreRel is where, relative to the session home (the first
	// var's Subdir), the engine keeps its native conversation history; ""
	// when it keeps none worth keeping. The cells adapter keeps that history
	// OUT of the disposable home: the directory lives under the session's
	// native/ and the home holds a relative link to it, so deleting the home
	// on Close never deletes the history. A clean relative slash path,
	// because the same link is followed inside a Linux container.
	TranscriptStoreRel string
}

// HomeVar is one env-var-to-subdir mapping. For Vars[0] the Subdir is the
// session home's own leaf, one path segment, and it is load-bearing: an
// engine that composes its own home path from a project-dir-shaped value
// must land on this exact directory, so Subdir must match whatever leaf the
// engine's own resolution appends. For every further var it is a clean
// relative slash path beneath the session home, nested or not (".xdg/config"
// is as valid as "config"), joined with '/' inside a container.
type HomeVar struct {
	Name   string
	Subdir string
	// Merge, set on a further var that names an XDG base directory, makes
	// its directory a MERGED tree: the user's own content for that base with
	// the app dirs the engine owns shadowed by the session's own. nil is a
	// directory of the session's alone.
	Merge *XDGMerge
}

// XDGMerge declares a further home var as an XDG base the session shares
// with the user (HomeVar.Merge). The engine and every tool it spawns then
// see one tree, under the var's Subdir, made of:
//   - each name in Owns: a directory of the session's own, created
//     owner-only, whatever the user keeps under that name;
//   - on a host run, every other top-level entry of the user's base
//     (UserXDGBase) linked in by shared isolation: the entries as they
//     stood when the run started, their contents live. A host run is
//     unsandboxed, so this exposes nothing it could not already read;
//   - in a container, nothing more: a container is given none of the
//     user's XDG content, so its tree holds the owned dirs alone.
type XDGMerge struct {
	// Owns are the top-level names under the base the engine keeps for
	// itself, each one path segment.
	Owns []string
}

// xdgBaseDefaults is the XDG Base Directory spec's default for each base
// directory var, as slash path segments under the user's home. Only these
// can be merged: no other var has a default to resolve the user's base by.
var xdgBaseDefaults = map[string][]string{
	"XDG_CONFIG_HOME": {".config"},
	"XDG_DATA_HOME":   {".local", "share"},
	"XDG_STATE_HOME":  {".local", "state"},
	"XDG_CACHE_HOME":  {".cache"},
}

// UserXDGBase resolves the user's own directory for the XDG base var name,
// per the XDG Base Directory spec: the var's value (getenv) when it is an
// absolute path, else the spec's default under home. A relative value is
// invalid by the spec and ignored. ok is false for a var the spec gives no
// default, or when the default is needed and home is "".
func UserXDGBase(name string, getenv func(string) string, home string) (string, bool) {
	def, ok := xdgBaseDefaults[name]
	if !ok {
		return "", false
	}
	if v := getenv(name); filepath.IsAbs(v) {
		return filepath.Clean(v), true
	}
	if home == "" {
		return "", false
	}
	return filepath.Join(append([]string{home}, def...)...), true
}

// BindHome resolves vars against the session home as the engine sees
// it: Vars[0] binds the home itself and each further var its Subdir beneath
// it, joined on each side as present.Presentation.Beneath joins (the host's
// own separators in place, '/' in a container). nil when there is no var or
// the home has no engine side (an unreachable root names no path).
//
// It is the ONE rule for where a declared var points: isolation places
// every run's vars by it, and the conformance suite hands an engine
// bindings made by it.
func BindHome(vars []HomeVar, home present.Root) []HomeBinding {
	if len(vars) == 0 || home.Engine == "" {
		return nil
	}
	out := make([]HomeBinding, 0, len(vars))
	out = append(out, HomeBinding{Var: vars[0].Name, Path: home.Engine})
	at := present.Presentation{HostPath: home.Host, EnginePath: home.Engine}
	for _, v := range vars[1:] {
		out = append(out, HomeBinding{Var: v.Name, Path: at.Beneath(v.Subdir).EnginePath})
	}
	return out
}

// Relocates reports whether the spec moves anything: the zero spec does not.
func (h HomeSpec) Relocates() bool { return len(h.Vars) > 0 }

// Validate refuses a non-zero spec the cells adapter could not act on
// correctly. The zero spec is valid: it declares nothing.
func (h HomeSpec) Validate() error {
	if !h.Relocates() {
		return h.validateWithoutHome()
	}
	if err := h.validateVars(); err != nil {
		return err
	}
	if r := h.TranscriptStoreRel; r != "" && !isContainerRel(r) {
		return fmt.Errorf("HomeSpec: TranscriptStoreRel %q is not a clean relative slash path below the session home; the same link is followed inside a Linux container, so build it with path, never filepath", r)
	}
	return h.validateAuth()
}

// validateWithoutHome refuses auth or a history store on a spec that
// relocates nothing: both are facts about a session home it does not have.
func (h HomeSpec) validateWithoutHome() error {
	if _, ok := h.Auth.Get(); ok {
		return errors.New("HomeSpec: auth with no home var; an engine that relocates nothing declares no auth here")
	}
	if h.TranscriptStoreRel != "" {
		return errors.New("HomeSpec: TranscriptStoreRel with no home var; a history store is relative to a session home this engine does not have")
	}
	return nil
}

// validateVars requires every home var to name its var and subdir, once
// each: Vars[0]'s Subdir one clean segment (the home's own leaf), every
// further one a clean relative slash path beneath the home that neither
// holds nor sits inside the history store's link (validateVarPath).
func (h HomeSpec) validateVars() error {
	seen := map[string]bool{}
	for i, v := range h.Vars {
		if v.Name == "" {
			return fmt.Errorf("HomeSpec: Vars[%d].Name is empty", i)
		}
		if seen[v.Name] {
			return fmt.Errorf("HomeSpec: Vars[%d] declares %s again; one var gets one path", i, v.Name)
		}
		seen[v.Name] = true
		if err := h.validateVarPath(i, v.Subdir); err != nil {
			return err
		}
		if err := h.validateMerge(i, v); err != nil {
			return err
		}
	}
	return nil
}

// validateMerge checks a merged var (HomeVar.Merge): a further var naming
// an XDG base, owning one-segment names once each, with no other var's
// directory inside its tree (it would collide with a user entry there).
func (h HomeSpec) validateMerge(i int, v HomeVar) error {
	if v.Merge == nil {
		return nil
	}
	if i == 0 {
		return fmt.Errorf("HomeSpec: Vars[0] (%s) is merged, but it names the session home itself; declare the XDG base as a further var beneath it", v.Name)
	}
	if _, ok := xdgBaseDefaults[v.Name]; !ok {
		return fmt.Errorf("HomeSpec: Vars[%d] (%s) is merged, but it is not an XDG base directory var; only those have a user directory the spec resolves", i, v.Name)
	}
	owned := map[string]bool{}
	for _, o := range v.Merge.Owns {
		if o == "" || o == "." || o == ".." || strings.ContainsAny(o, `/\`) {
			return fmt.Errorf("HomeSpec: Vars[%d] (%s) Owns %q, which is not one path segment; an owned name is a top-level entry of the base", i, v.Name, o)
		}
		if owned[o] {
			return fmt.Errorf("HomeSpec: Vars[%d] (%s) Owns %q twice", i, v.Name, o)
		}
		owned[o] = true
	}
	for j, other := range h.Vars {
		if j != 0 && j != i && pathWithin(other.Subdir, v.Subdir) {
			return fmt.Errorf("HomeSpec: Vars[%d].Subdir %q lies inside the merged base %s (%q); its top level holds the user's entries and the owned dirs alone", j, other.Subdir, v.Name, v.Subdir)
		}
	}
	return nil
}

// validateVarPath checks Vars[i].Subdir for its place in the home: the
// first is the home's leaf, every further one a directory beneath it.
func (h HomeSpec) validateVarPath(i int, sub string) error {
	switch {
	case sub == "":
		return fmt.Errorf("HomeSpec: Vars[%d].Subdir is empty", i)
	case i == 0:
		return validateHomeLeaf(sub)
	}
	return h.validateBeneathHome(i, sub)
}

// validateHomeLeaf requires Vars[0].Subdir to be one clean path segment.
func validateHomeLeaf(sub string) error {
	if strings.Contains(sub, "/") || !isContainerRel(sub) || sub == "." || sub == ".." {
		return fmt.Errorf("HomeSpec: Vars[0].Subdir %q is not one path segment; it is the session home's own leaf", sub)
	}
	return nil
}

// validateBeneathHome requires a further var's Subdir to be a clean relative
// slash path strictly beneath the home, clear of the history store's link.
func (h HomeSpec) validateBeneathHome(i int, sub string) error {
	if !isContainerRel(sub) || sub == "." {
		return fmt.Errorf("HomeSpec: Vars[%d].Subdir %q is not a clean relative slash path beneath the session home", i, sub)
	}
	if r := h.TranscriptStoreRel; r != "" && (pathWithin(sub, r) || pathWithin(r, sub)) {
		return fmt.Errorf("HomeSpec: Vars[%d].Subdir %q overlaps TranscriptStoreRel %q; isolation creates the one as a directory and links the other", i, sub, r)
	}
	return nil
}

// pathWithin reports whether the clean slash path p is root or beneath it.
func pathWithin(p, root string) bool { return p == root || strings.HasPrefix(p, root+"/") }

// validateAuth requires Auth decided, and valid when present.
func (h HomeSpec) validateAuth() error {
	if !h.Auth.Decided() {
		return errors.New("HomeSpec: Auth is undeclared; provide the engine's auth or declare it absent with the reason")
	}
	if a, ok := h.Auth.Get(); ok {
		if err := validateAuth(a); err != nil {
			return fmt.Errorf("HomeSpec: %w", err)
		}
	}
	return nil
}

// InstanceConfigWriter generates the engine's own top-level config file
// inside a provisioned session home: the engine decides what a single byte
// of it says; the cells adapter decides that one is generated at all.
type InstanceConfigWriter interface {
	WriteInstanceConfig(req InstanceConfigRequest, root safefs.Root) (InstanceConfigReport, error)
}

// InstanceConfigRequest is what the writer is handed: the host user's real
// home (the ambient values it may copy), the session home it writes into —
// the directory the engine's home var names, placed by launch.SessionHome,
// with no leaf of the writer's own appended — the run's working directory,
// the engine's verdict on its repository: only a trusted one may have
// the engine's trust answer written for it — and the run's auth mode: only
// the human's own login carries the login's credential half.
type InstanceConfigRequest struct {
	HostHome     string
	InstanceHome string
	WorkDir      string
	Trust        WorkspaceTrust
	Auth         AuthMode
}

// RepoTrust is an engine's verdict on a repository: whether the human
// trusted it to run its own executable surfaces. The engine reads its OWN
// record of that answer (claude: the workspace-trust flag in its config),
// so ctxloom keeps no trust store of its own and never answers for the
// human. An error is no verdict; the caller treats it as untrusted.
type RepoTrust interface {
	Verdict(fs afero.Fs, q TrustQuery) (WorkspaceTrust, error)
}

// TrustQuery is what a verdict is taken over: the host user's real home,
// where the engine keeps the human's own answers, and the run's working
// directory as THIS process sees it — the repository is walked there, where
// it can be read. HostPath names a directory of that walk in the host path
// space: a process in a container reads its own view, and the answers it
// reads may have been recorded in either. nil is identity (this process
// shares the host's paths).
type TrustQuery struct {
	HostHome string
	WorkDir  string
	HostPath func(dir string) (string, error)
}

// Keys are the names an answer for dir may be recorded under, each once: dir
// itself, as a claude running in this process's view records it, and its host
// name (HostPath), as the human's claude on the host does. An answer under
// either counts. A dir with no host name has only its own.
func (q TrustQuery) Keys(dir string) []string {
	keys := []string{dir}
	if q.HostPath == nil {
		return keys
	}
	if host, err := q.HostPath(dir); err == nil && host != dir {
		keys = append(keys, host)
	}
	return keys
}

// InstanceConfigReport is what it wrote and what it skipped.
type InstanceConfigReport struct {
	Wrote    []string
	Warnings []string
}

// ContainerSpec says how a containerized run of the engine is built. How it
// authenticates is not a container question: the run's Credentials
// (Auth.Credentials) are satisfied by whichever environment runs it. Engine.Container returns it, or refuses with ErrUnsupported
// when the engine has no image — so a container binding fails at Resolve,
// never later.
type ContainerSpec struct {
	// Install is the engine's composable RUN-layer Containerfile fragment:
	// its own official installer on an arbitrary base, hard-gated by a
	// validate step. nil = no known installer (the engine cannot be composed
	// into an agent image; only an overlay onto a base that already ships it
	// works).
	Install []byte
	// ValidateCommand is the in-image command that proves the client runs
	// (`<client> --version`). Required whenever Install is set.
	ValidateCommand string
	// OverlayDirs are the project-relative managed-config DIRECTORIES the
	// engine's writers target under the run's cwd, shadowed by scratch
	// overlays so the host project stays clean. Directories only.
	OverlayDirs []string
	// InPlaceFiles are the project-relative FILES the engine's writers
	// rewrite in place in the bind-mounted project during a container run —
	// not overlaid, because a single-file overlay breaks the writers' atomic
	// rename. Host processes write the same files, so each one's lock is the
	// only lock that crosses the container boundary.
	InPlaceFiles []string
}

// Validate refuses a container declaration that cannot be built or resolved.
func (c ContainerSpec) Validate() error {
	if len(c.Install) > 0 && c.ValidateCommand == "" {
		return errors.New("ContainerSpec: Install is set but ValidateCommand is empty; an install fragment must be gated by a command that proves the client runs")
	}
	return nil
}

// isContainerRel is a clean, relative, slash-separated path that stays below
// its base — the only shape path.Join(home, rel) resolves inside the home.
func isContainerRel(r string) bool {
	return !strings.Contains(r, `\`) && !path.IsAbs(r) && path.Clean(r) == r &&
		r != ".." && !strings.HasPrefix(r, "../")
}

// TranscriptReader is one version-scoped reader of the engine's own
// transcript store, as the port sees it: the version range it covers. The
// transcript adapter that consumes readers knows their richer shape; the
// port only says which exist.
type TranscriptReader interface {
	Versions() (min, max string)
}

// HookCodec is the engine's hook wire, both directions, in the port's
// neutral terms. ctxloom's hook verbs (`ctxloom hook <verb> --engine <name>`)
// never read or write an engine's native payload themselves: the hooks
// approach that delivered the hook named the firing engine in its args, the
// verb resolves that engine's codec through the registry, and the codec is
// the only code that knows the native shape. An engine that fires no hooks
// returns a codec whose Decode and Encode refuse with ErrUnsupported —
// unreachable, since no payload arrives.
type HookCodec interface {
	// Decode parses one native hook payload. event is the unified event the
	// hook was registered under (the native payload's own event name, when it
	// carries one, wins). A payload that does not parse is an error: a hook
	// that silently treats it as empty is the failure this seam exists to
	// end.
	Decode(event string, payload []byte) (HookEvent, error)
	// Encode renders a neutral response as the native answer to a hook
	// registered under the unified event: the bytes for stdout and the
	// process exit status the engine reads. A response the engine has no
	// native form for on that event (context on an event that carries none,
	// a block where the engine cannot block) is an error, never a guess.
	Encode(event string, r HookResponse) (HookReply, error)
	// ContextLimit is the most context, in bytes, one hook answer may carry
	// before the engine stops delivering it whole. 0 declares no limit.
	ContextLimit() int
	// InvokedSkill reports the skill a native tool call invoked: the engine's
	// own tool and input shape say whether a call ran a skill and which. It
	// answers for a hook payload's tool and for a transcript's tool_use
	// record alike, since both carry the native tool name and input.
	InvokedSkill(tool string, input []byte) (string, bool)
}

// HookEvent is one native hook payload, decoded into the port's terms. A
// field the event does not carry is zero.
type HookEvent struct {
	// Event is the unified event name.
	Event string
	// NativeSession is the engine's own session key.
	NativeSession string
	// Transcript is the path of the engine's own transcript of the session.
	Transcript string
	// Source is why a session_start fired (SessionSource*).
	Source string
	// Prompt is the submitted prompt, on turn_start.
	Prompt string
	// Tool is the native name of the tool a tool event is about.
	Tool string
	// ToolInput and ToolResponse are the tool call's native input and
	// response, raw: their shape is per tool.
	ToolInput    []byte
	ToolResponse []byte
	// Skill is the skill the tool call invoked (InvokedSkill), "" for none.
	Skill string
	// Path is the file a file-editing tool targeted, "" for none.
	Path string
}

// The reasons a session_start fires, in the port's vocabulary. An engine's
// codec maps its native reasons onto these; one it cannot map is carried
// verbatim.
const (
	SessionSourceStartup = "startup"
	SessionSourceResume  = "resume"
	SessionSourceClear   = "clear"
	SessionSourceCompact = "compact"
)

// HookResponse is what a hook verb answers, in the port's terms.
type HookResponse struct {
	// Context is model-visible context added at the event.
	Context string
	// Notice is shown to the user, not the model.
	Notice string
	// Block stops what the event was about to do; Reason says why.
	Block  bool
	Reason string
}

// Empty reports a response that says nothing.
func (r HookResponse) Empty() bool { return r == HookResponse{} }

// HookReply is a response rendered for one engine: what the hook process
// writes to stdout and the status it exits with.
type HookReply struct {
	Stdout []byte
	Exit   int
}
