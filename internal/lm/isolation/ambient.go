package isolation

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"sync"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/filelock"
)

// AmbientFile is one file whose ORIGIN is the user's real host home and which
// is COPIED INTO an instance config home at instance time. ONE WAY, ALWAYS:
// nothing ctxloom writes ever goes back to the real home, which stays the
// durable truth of the user's engine configuration.
//
// The set of these per engine is an ALLOW-LIST, never a deny-list — see
// AmbientSet.
type AmbientFile struct {
	// HostRel is the path under the user's real host home, slash-separated
	// (e.g. ".claude/.credentials.json").
	HostRel string
	// DestRel is the path under the instance home root, slash-separated. It
	// carries the engine's own leaf (e.g. "claude/.credentials.json",
	// ".codex/auth.json"), so one instance root hosts every engine without
	// collision.
	DestRel string
	// Mode is the mode the COPY is written at — 0600 for credential material,
	// regardless of the source file's own mode. Never widened.
	Mode fs.FileMode
	// Required reports whether this file's absence means "there is nothing to
	// authenticate with" (the fail-loud case) rather than "this optional extra
	// is not configured" (best-effort).
	Required bool
}

// AmbientSet returns engine's ambient allow-list, keyed by REGISTERED backend
// name ("claude-code", "codex", "kiro", "opencode"). It returns nil both for an
// unregistered engine and for a registered one whose set is DECLARED EMPTY —
// kiro, whose credentials live in a global SQLite store no home var relocates.
// The two are told apart by AmbientEngineNames, which lists exactly the
// registered ones; the roster arch gate asserts every registered backend has an
// explicit entry, empty or not.
//
// ALLOW-LIST, NEVER DENY-LIST. Under a deny-list, a file the engine vendor adds
// tomorrow is copied by DEFAULT, and the default direction of a mistake there
// is a confidentiality leak: claude's `.claude.json` carries the user's own
// mcpServers registrations, codex's config.toml carries theirs. Listing what
// crosses — by name, one line each — is what makes D4 and D5 decisions rather
// than accidents.
func AmbientSet(engine string) []AmbientFile {
	spec, ok := credentialSeedSpecFor(engine)
	if !ok || spec.sourceFiles == nil {
		return nil
	}
	home, err := hostHomeDir()
	if err != nil || home == "" {
		home = credentialSeedSourceFileSentinelHome
	}
	files := spec.sourceFiles(home)
	out := make([]AmbientFile, 0, len(files))
	for _, f := range files {
		rel, relErr := filepath.Rel(home, f.host)
		if relErr != nil {
			rel = f.host
		}
		out = append(out, AmbientFile{
			HostRel:  filepath.ToSlash(rel),
			DestRel:  filepath.ToSlash(filepath.Join(spec.destSubdir, f.destName)),
			Mode:     ambientCredentialMode,
			Required: f.required,
		})
	}
	return out
}

// ambientCredentialMode is the mode every copied ambient file lands at:
// owner-only. The destination holds live credential bytes even when the source
// file's own mode is laxer, so this is restated on the copy rather than
// inherited — see copyCredentialFile, which enforces it.
const ambientCredentialMode fs.FileMode = 0o600

// AmbientEngineNames returns the backend names with an EXPLICIT ambient
// declaration — the same roster credentialSeedSpecs carries, exposed under the
// ambient name so a caller asking "does this engine have a declared set?" does
// not have to know the set is stored in the credential-seed registry.
func AmbientEngineNames() []string { return CredentialSeedEngineNames() }

// AmbientRequest is one ambient copy-in: which engine, into which instance
// home, for which working directory.
type AmbientRequest struct {
	// Engine is the REGISTERED backend name ("claude-code", "codex", ...).
	Engine string
	// InstanceHome is the config-home ROOT to copy into — a per-session in-tree
	// instance (paths.SessionHomePath) or a per-agent worktree config home.
	// Each engine's own leaf is appended under it.
	InstanceHome string
	// WorkDir is the absolute project directory the run works in, passed
	// through to the engine's generated config so a workspace-trust answer can
	// be keyed to the directory the run actually uses. Empty is tolerated (the
	// engine skips its per-project half and says so).
	WorkDir string
}

// AmbientCopyReport is what one CopyAmbient call did.
type AmbientCopyReport struct {
	// Copied counts the ambient files whose bytes landed in the instance.
	Copied int
	// MissingOptional counts allow-listed, non-Required files absent from the
	// host.
	MissingOptional int
	// SkippedEnv reports that the engine's envTrigger (ANTHROPIC_API_KEY,
	// OPENAI_API_KEY, ...) already carries usable auth, so there was nothing to
	// copy. Not an error; the caller still points the engine at the instance.
	SkippedEnv bool
	// NoSource reports the FAIL-LOUD case: this engine relocates credentials
	// with its home var, no envTrigger is set, and the required host
	// credential is absent — an engine launched at this instance would start
	// logged out. It is returned as a DECISION rather than a Go error because
	// the two axes handle it differently (the in-tree axis refuses the
	// relocation; the worktree axis records a degradable ClassIsolation
	// finding), and an error would force both to parse one.
	NoSource bool
	// NoSourceReason is the ready-to-surface, actionable message for NoSource —
	// naming only fixes that work (authenticate the engine, or set its API-key
	// var). Empty unless NoSource.
	NoSourceReason string
	// Generated lists the paths the ENGINE's own instance-config writer wrote
	// (claude's .claude.json, codex's config.toml base). Empty for an engine
	// with a declared-empty contribution.
	Generated []string
	// Warnings carries the engine's fail-loud notices (host-file schema drift,
	// a precedence file shadowing what was written). CopyAmbient prints them;
	// they are also returned so a test can assert on them.
	Warnings []string
}

// instanceConfigWriters is the engine-owned instance-config generator per
// registered backend, populated once at init by internal/lm/backends — the one
// package that can see both this registry and internal/{claude,codex,kiro}.
//
// This indirection is not decoration. This package cannot import the engine
// packages (each of them reaches internal/acp, which imports this one — a real
// cycle) and cannot import internal/lm/backends either (backends imports this
// package). But the WORKTREE axis resolves its engine by NAME, inside this
// package, with no engine value in hand — so without a name-keyed registry the
// engine write-config directive would reach only the in-tree axis, and D8's
// "one mechanism for both axes" would be a mechanism for one.
var (
	instanceConfigMu      sync.RWMutex
	instanceConfigWriters = map[string]agent.InstanceConfigWriter{}
)

// RegisterInstanceConfigWriter installs engine's own instance-config generator.
// Called from internal/lm/backends' init, once per backend that declares one;
// re-registering a name replaces it. An engine with no registration contributes
// its ambient FILE copies and no generated config — which is also what an
// isolation-only test binary sees, since nothing links backends into it.
func RegisterInstanceConfigWriter(engine string, w agent.InstanceConfigWriter) {
	assertCanonicalEngineKey("instanceConfigWriters", engine)
	instanceConfigMu.Lock()
	defer instanceConfigMu.Unlock()
	if w == nil {
		delete(instanceConfigWriters, engine)
		return
	}
	instanceConfigWriters[engine] = w
}

// instanceConfigWriterFor returns engine's registered generator, or nil. It
// resolves through the repo-wide alias table, so an aliased engine name reaches
// the same writer the canonical one does — a miss here means the engine
// generates no instance config, never that it was spelled differently.
func instanceConfigWriterFor(engine string) agent.InstanceConfigWriter {
	instanceConfigMu.RLock()
	defer instanceConfigMu.RUnlock()
	return instanceConfigWriters[agent.CanonicalEngineName(engine)]
}

// credentialProjectors is the engine-owned ambient-credential projector per
// registered backend, populated once at init by internal/lm/backends alongside
// instanceConfigWriters — for the identical name-keyed-indirection reason (this
// package cannot import the engine packages, and the worktree axis resolves its
// engine by NAME with no engine value in hand). An engine with no registered
// projector has its ambient credential files copied byte-for-byte; only claude
// registers one today (to strip its single-use refresh token — see
// agent.CredentialProjector and internal/claude.NewCredentialProjector).
var (
	credentialProjectorMu sync.RWMutex
	credentialProjectors  = map[string]agent.CredentialProjector{}
)

// RegisterCredentialProjector installs engine's ambient-credential projector.
// Called from internal/lm/backends' init, once per backend that declares one;
// re-registering a name replaces it, and a nil projector deletes the entry (so
// a test can restore the pre-existing registration). An engine with no
// registration copies its ambient credential files verbatim.
func RegisterCredentialProjector(engine string, p agent.CredentialProjector) {
	assertCanonicalEngineKey("credentialProjectors", engine)
	credentialProjectorMu.Lock()
	defer credentialProjectorMu.Unlock()
	if p == nil {
		delete(credentialProjectors, engine)
		return
	}
	credentialProjectors[engine] = p
}

// credentialProjectorFor returns engine's registered projector, or nil. It
// resolves through the repo-wide alias table, so an aliased engine name reaches
// the same projector the canonical one does — a miss here means the engine's
// ambient credentials are copied verbatim, never that it was spelled
// differently.
func credentialProjectorFor(engine string) agent.CredentialProjector {
	credentialProjectorMu.RLock()
	defer credentialProjectorMu.RUnlock()
	return credentialProjectors[agent.CanonicalEngineName(engine)]
}

// CopyAmbient performs THE ambient copy-in — the one one-way transfer from the
// user's real host home into an instance config home, shared by both axes that
// have one (D8, ruled: share the MECHANISM, keep the locations
// split — worktree homes stay home-rooted under
// ~/.ctxloom/sessions/<harp>/ephemeral/, in-tree instances live at
// <project>/.ctxloom/state/<harp>/home).
//
// It does exactly two things, in order:
//
//  1. copies req.Engine's ALLOW-LISTED ambient files (AmbientSet) out of the
//     real host home, at 0600, into the instance;
//  2. asks the ENGINE to generate its own instance config (the engine
//     write-config directive) — claude's field-scoped `.claude.json`, codex's
//     `config.toml` base minus the elided sections. Every byte-level edit of a
//     vendor's format happens inside that vendor's package; this function only
//     decides WHICH files and classes cross.
//
// ONE WAY. The real host home is READ and never written; tests/arch's
// real-home byte-identity gate is what proves it, because a path assertion can
// only say where ctxloom MEANT to write.
//
// The whole operation is serialized under the project filelock keyed to
// req.InstanceHome, so two runs sharing ONE session instance (a coordinator and
// its in-tree delegated child, which inherits the harp) cannot interleave their
// load-modify-write of the same config file. An instance home outside any
// .ctxloom tree cannot be keyed and proceeds unlocked — that is only ever the
// harpless worktree fallback under the OS temp dir, whose home is per-AGENT and
// therefore has no second writer to race.
func CopyAmbient(req AmbientRequest) (AmbientCopyReport, error) {
	spec, ok := credentialSeedSpecFor(req.Engine)
	if !ok {
		return AmbientCopyReport{}, fmt.Errorf("ambient copy-in: backend %q has no declared ambient set (internal error)", req.Engine)
	}
	if req.InstanceHome == "" {
		return AmbientCopyReport{}, fmt.Errorf("ambient copy-in for %s: no instance home to copy into (internal error)", spec.engine)
	}
	unlock := lockInstanceHome(req.InstanceHome)
	defer unlock()
	return copyAmbientLocked(spec, req)
}

// copyAmbientLocked is CopyAmbient's body, with the instance lock already held.
func copyAmbientLocked(spec credentialSeedSpec, req AmbientRequest) (AmbientCopyReport, error) {
	var rep AmbientCopyReport

	if spec.sourceFiles != nil {
		result, err := hostCredentialSeed(spec, req.InstanceHome, credentialProjectorFor(req.Engine))
		if err != nil {
			return rep, err
		}
		switch result {
		case seedSkippedEnv:
			rep.SkippedEnv = true
		case seedNoSource:
			rep.NoSource = true
			rep.NoSourceReason = noAmbientSourceReason(spec)
			// Nothing to authenticate with: do NOT generate a config for an
			// instance the caller is about to refuse. Reporting the decision is
			// this call's whole remaining job.
			return rep, nil
		case seedOK:
			rep.Copied, rep.MissingOptional = ambientCopyCounts(spec)
		}
	}

	writer := instanceConfigWriterFor(req.Engine)
	if writer == nil {
		return rep, nil
	}
	hostHome, err := hostHomeDir()
	if err != nil {
		hostHome = ""
	}
	engineRep, err := writer.WriteInstanceConfig(agent.InstanceConfigRequest{
		HostHome:     hostHome,
		InstanceHome: req.InstanceHome,
		WorkDir:      req.WorkDir,
	})
	rep.Generated = engineRep.Wrote
	rep.Warnings = engineRep.Warnings
	for _, w := range rep.Warnings {
		clidiag.Warn("ctxloom", "%s instance config: %s", spec.engine, w)
	}
	if err != nil {
		return rep, err
	}
	return rep, nil
}

// ambientCopyCounts reports how many of spec's ambient files were present on
// the host and how many OPTIONAL ones were absent, after a successful seed.
// Recomputed from the same source-file descriptor the seed used rather than
// threaded back out of it, so the counting cannot claim a copy the seed did not
// make.
func ambientCopyCounts(spec credentialSeedSpec) (copied, missingOptional int) {
	home, err := hostHomeDir()
	if err != nil || home == "" {
		return 0, 0
	}
	for _, f := range spec.sourceFiles(home) {
		switch {
		case fileExists(f.host):
			copied++
		case !f.required:
			missingOptional++
		}
	}
	return copied, missingOptional
}

// noAmbientSourceReason builds the fail-loud message for "nothing seedable":
// no API-key env var and no host credential file. It names ONLY fixes that
// work — authenticating the engine, or setting its key — and deliberately not
// "(or pass --degraded)": on the in-tree path that flag is consulted by the
// CALLER's strictness finding, never by the code that surfaces this string, and
// an error naming an escape hatch that does not exist sends the user round a
// loop that cannot terminate.
func noAmbientSourceReason(spec credentialSeedSpec) string {
	return fmt.Sprintf(
		"no %s and no host ~/%s credentials found to authenticate this run — run `%s` or set %s",
		spec.envTrigger, primaryAmbientHostRel(spec), spec.loginHint, spec.envTrigger)
}

// primaryAmbientHostRel is the slash-separated, home-relative path of spec's
// REQUIRED credential file — the one whose absence is the fail-loud case — for
// use in a message. Falls back to the first entry when no file is marked
// required (no current spec does).
func primaryAmbientHostRel(spec credentialSeedSpec) string {
	if spec.sourceFiles == nil {
		return ""
	}
	home, err := hostHomeDir()
	if err != nil || home == "" {
		home = credentialSeedSourceFileSentinelHome
	}
	rel := func(f seedFile) string {
		r, relErr := filepath.Rel(home, f.host)
		if relErr != nil {
			return f.host
		}
		return filepath.ToSlash(r)
	}
	files := spec.sourceFiles(home)
	for _, f := range files {
		if f.required {
			return rel(f)
		}
	}
	if len(files) > 0 {
		return rel(files[0])
	}
	return ""
}

// lockInstanceHome takes the project filelock for instanceHome and returns the
// release. A home that cannot be keyed to a lock location (one outside any
// .ctxloom tree) returns a no-op release rather than failing the run — see
// CopyAmbient's doc for why that case has no second writer.
func lockInstanceHome(instanceHome string) func() {
	lockPath, err := paths.ProjectPathFor(instanceHome)
	if err != nil {
		return func() {}
	}
	unlock, err := filelock.Lock(lockPath)
	if err != nil {
		clidiag.Warn("ctxloom", "ambient copy-in: could not take the instance lock %s (%v); proceeding unserialized", lockPath, err)
		return func() {}
	}
	return unlock
}
