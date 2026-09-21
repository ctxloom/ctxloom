package isolation

import (
	"fmt"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/gofrs/flock"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/lockwait"
)

// AmbientFile is one file whose ORIGIN is the user's real host home and which
// is PLACED INTO an instance config home at instance time.
//
// CREDENTIAL material is placed by the provisioner and kept in step with
// the host file for as long as the run lives: the instance holds the
// engine's declared projection of the host bytes (claude withholds the
// single-use refresh token, as its own session seeding does), re-applied on
// every host change. The direction is host TO instance only — ctxloom never
// writes the real home, and neither does the instance through this path.
//
// The set of these per engine is an ALLOW-LIST, never a deny-list — see
// AmbientSet.
type AmbientFile struct {
	// HostRel is the path under the user's real host home, slash-separated
	// (e.g. ".claude/.credentials.json").
	HostRel string
	// DestRel is the path under the instance home root, slash-separated. It
	// carries the engine's own leaf (e.g. "claude/.credentials.json",
	// ".claude/.credentials.json"), so one instance root hosts every engine without
	// collision.
	DestRel string
	// Mode is the mode the placement is written at — 0600 for credential material,
	// regardless of the source file's own mode. Never widened.
	Mode fs.FileMode
	// Required reports whether this file's absence means "there is nothing to
	// authenticate with" (the fail-loud case) rather than "this optional extra
	// is not configured" (best-effort).
	Required bool
}

// AmbientSet returns engine's ambient allow-list, by engine name. It returns
// nil both for an engine the facts accessor does not know and for one whose
// seed is DECLARED ABSENT — an engine whose credentials live in a global
// store no home var relocates. The two are told apart by AmbientEngineNames,
// which lists exactly the known ones: every one has an explicit declaration,
// provided or absent, on its home (EngineFacts.Home).
//
// ALLOW-LIST, NEVER DENY-LIST. Under a deny-list, a file the engine vendor adds
// tomorrow is copied by DEFAULT, and the default direction of a mistake there
// is a confidentiality leak: claude's `.claude.json` carries the user's own
// mcpServers registrations, and each engine's own config carries theirs. Listing what
// crosses — by name, one line each — is what makes D4 and D5 decisions rather
// than accidents.
func AmbientSet(engine string) []AmbientFile {
	seed, ok := credentialSeedFor(engine)
	if !ok {
		return nil
	}
	out := make([]AmbientFile, 0, len(seed.Files))
	for _, f := range seed.Files {
		out = append(out, AmbientFile{
			HostRel:  f.HostRelHome,
			DestRel:  filepath.ToSlash(filepath.Join(seed.Subdir, f.DestName)),
			Mode:     ambientCredentialMode,
			Required: f.Required,
		})
	}
	return out
}

// ambientCredentialMode is the mode every placed ambient file lands at:
// owner-only. The destination holds live credential bytes even when the source
// file's own mode is laxer, so this is restated on placement rather than
// inherited — see tightenSeedDestinations, which enforces it on a destination
// that already existed, and the provisioners, which create at this mode.
const ambientCredentialMode fs.FileMode = 0o600

// AmbientEngineNames returns the backend names with an EXPLICIT ambient
// declaration, provided or absent — the credential-seed roster, exposed under
// the ambient name so a caller asking "does this engine have a declared set?"
// does not have to know where the declaration is stored.
func AmbientEngineNames() []string { return CredentialSeedEngineNames() }

// AmbientRequest is one ambient copy-in: which engine, into which instance
// home, for which working directory.
type AmbientRequest struct {
	// Engine is the REGISTERED backend name ("claude-code", ...).
	Engine string
	// InstanceHome is the config-home ROOT to copy into — a per-session
	// instance (paths.HarpSessionHome) or a per-agent worktree config home.
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
	// Copied counts the ambient files that reached the instance. Not
	// necessarily by copying — see Delivery for what the transfer actually
	// was; this is the count of declared files the host had to give.
	Copied int
	// MissingOptional counts allow-listed, non-Required files absent from the
	// host.
	MissingOptional int
	// SkippedEnv reports that the engine's EnvTrigger already carries usable
	// auth, so there was nothing to copy. Not an error; the caller still
	// points the engine at the instance.
	SkippedEnv bool
	// NoSource reports the FAIL-LOUD case: this engine relocates credentials
	// with its home var, no EnvTrigger is set, and the required host
	// credential is absent — an engine launched at this instance would start
	// logged out. It is returned as a DECISION rather than a Go error so the
	// caller can refuse the relocation in its own words (a fail-loud finding
	// with the fix named) instead of parsing one back out of an error.
	NoSource bool
	// NoSourceReason is the ready-to-surface, actionable message for NoSource —
	// naming only fixes that work (authenticate the engine, or set its API-key
	// var). Empty unless NoSource.
	NoSourceReason string
	// Delivery is HOW the credential material was placed — shared by IDENTITY
	// (a mount) or by REPLICATION (two files kept in step). It is reported
	// because the two FAIL DIFFERENTLY: replication has a rotation window a
	// mount does not, and whoever debugs a rejected token a year from now
	// needs to know which one this run got. DeliveryUnset when nothing was
	// placed (SkippedEnv, NoSource, or a declared-absent seed).
	Delivery Delivery
	// Mechanism names the implementation that placed it ("container-mount",
	// "namespace-mount", "replication") — Delivery answers "what guarantees do
	// I have?", this answers "which code do I go read?".
	Mechanism string
	// provisioned holds whatever the provisioning left RUNNING, so Close can
	// stop it. Unexported: the only thing a caller may do with it is close it,
	// and an exported Result invites a second caller to re-read the delivery
	// from a field that is already reported above.
	provisioned Result
	// Generated lists the paths the ENGINE's own instance-config writer wrote
	// (claude's .claude.json, for instance). Empty for an engine
	// with a declared-empty contribution.
	Generated []string
	// Warnings carries the engine's fail-loud notices (host-file schema drift,
	// a precedence file shadowing what was written). CopyAmbient prints them;
	// they are also returned so a test can assert on them.
	Warnings []string
}

// Close stops whatever the provisioning left running — a replicator's
// watchers and their goroutines. Safe on a zero report and safe to call twice.
//
// It is the RUN's to call, at the run's end, and nothing else's: the
// replication is what carries a host token refresh into the instance (the
// old token is revoked the moment the host rotates it, and the instance
// holds no refresh token of its own). Closed early, a live engine is left on
// a dead token; never closed, every launch leaks a watcher pair into the
// process that prepared it. The seam that prepares a controlled home hands
// this back as the preparation's release (backends.InTreeAgentHomeSpec
// .Prepare), and the cell folds it into its Cleanup.
func (r AmbientCopyReport) Close() error { return r.provisioned.Close() }

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
//     write-config directive) — claude's field-scoped `.claude.json`, another engine's
//     `config.toml` base minus the elided sections. Every byte-level edit of a
//     vendor's format happens inside that vendor's package; this function only
//     decides WHICH files and classes cross.
//
// ONE WAY. The real host home is READ and never written by this call;
// tests/arch's real-home byte-identity gate is what proves it, because a
// path assertion can only say where ctxloom MEANT to write. What the call
// leaves RUNNING keeps that direction: the credential replication it returns
// on the report carries the host's rotations into the instance, projected,
// and an instance write is overwritten rather than propagated.
//
// The whole operation is serialized under the project lock keyed to
// req.InstanceHome, so two runs sharing ONE session instance (a coordinator and
// its in-tree delegated child, which inherits the harp) cannot interleave their
// load-modify-write of the same config file. An instance home outside any
// .ctxloom tree cannot be keyed and proceeds unlocked — that is only ever the
// harpless worktree fallback under the OS temp dir, whose home is per-AGENT and
// therefore has no second writer to race.
func CopyAmbient(req AmbientRequest) (AmbientCopyReport, error) {
	declared, ok := credentialSeedDeclared(req.Engine)
	if !ok {
		return AmbientCopyReport{}, fmt.Errorf("ambient copy-in: backend %q has no declared ambient set (internal error)", req.Engine)
	}
	engine := req.Engine
	if req.InstanceHome == "" {
		return AmbientCopyReport{}, fmt.Errorf("ambient copy-in for %s: no instance home to copy into (internal error)", engine)
	}
	unlock := lockInstanceHome(req.InstanceHome)
	defer unlock()
	return copyAmbientLocked(engine, declared, req)
}

// copyAmbientLocked is CopyAmbient's body, with the instance lock already
// held. A declared-absent seed skips the copy half entirely — the engine
// said it has nothing seedable — and still runs the engine's own
// instance-config generation.
func copyAmbientLocked(name string, declared engine.Declared[engine.CredentialSeed], req AmbientRequest) (AmbientCopyReport, error) {
	var rep AmbientCopyReport

	if seed, ok := declared.Get(); ok {
		result, provisioned, err := hostCredentialSeed(name, seed, req.InstanceHome)
		if err != nil {
			return rep, err
		}
		rep.provisioned = provisioned
		rep.Delivery, rep.Mechanism = provisioned.Delivery, provisioned.Mechanism
		switch result {
		case seedSkippedEnv:
			rep.SkippedEnv = true
		case seedNoSource:
			rep.NoSource = true
			rep.NoSourceReason = noAmbientSourceReason(seed)
			// Nothing to authenticate with: do NOT generate a config for an
			// instance the caller is about to refuse. Reporting the decision is
			// this call's whole remaining job.
			return rep, nil
		case seedOK:
			rep.Copied, rep.MissingOptional = ambientCopyCounts(seed)
		}
	}

	writer := instanceConfigWriterFor(name)
	if writer == nil {
		return rep, nil
	}
	hostHome, err := hostHomeDir()
	if err != nil {
		hostHome = ""
	}
	engineRep, err := writer.WriteInstanceConfig(engine.InstanceConfigRequest{
		HostHome:     hostHome,
		InstanceHome: req.InstanceHome,
		WorkDir:      req.WorkDir,
	}, nil)
	rep.Generated = engineRep.Wrote
	rep.Warnings = engineRep.Warnings
	for _, w := range rep.Warnings {
		clidiag.Warn("ctxloom", "%s instance config: %s", name, w)
	}
	if err != nil {
		return rep, err
	}
	return rep, nil
}

// ambientCopyCounts reports how many of seed's ambient files were present on
// the host and how many OPTIONAL ones were absent, after a successful seed.
// Recomputed from the same declaration the seed used rather than threaded
// back out of it, so the counting cannot claim a copy the seed did not make.
func ambientCopyCounts(seed engine.CredentialSeed) (copied, missingOptional int) {
	home, err := hostHomeDir()
	if err != nil || home == "" {
		return 0, 0
	}
	for _, f := range resolveSeedFiles(seed, home) {
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
func noAmbientSourceReason(seed engine.CredentialSeed) string {
	return fmt.Sprintf(
		"no %s and no host ~/%s credentials found to authenticate this run — run `%s` or set %s",
		seed.EnvTrigger, primaryAmbientHostRel(seed), seed.LoginHint, seed.EnvTrigger)
}

// primaryAmbientHostRel is the slash-separated, home-relative path of seed's
// REQUIRED credential file — the one whose absence is the fail-loud case — for
// use in a message. engine.HomeSpec.Validate refuses a seed with no required
// file, so the fallback to the first entry is for an unvalidated value only.
func primaryAmbientHostRel(seed engine.CredentialSeed) string {
	for _, f := range seed.Files {
		if f.Required {
			return f.HostRelHome
		}
	}
	if len(seed.Files) > 0 {
		return seed.Files[0].HostRelHome
	}
	return ""
}

// lockFileMode and lockDirMode are the modes this instance-home lock's
// sidecar and its parent directory are created with, before umask — not
// group- or world-WRITABLE, matching every other lock site in this project
// (see internal/core/agent/rmw_lock.go's identically-reasoned pair).
const (
	lockFileMode = 0o644
	lockDirMode  = 0o755
)

// lockInstanceHome takes the lock for instanceHome and returns the release.
// The instance is a member of the home-rooted session dir (paths.HarpSessionHome),
// so its sidecar goes to the home locks store (paths.HomePathFor) — the
// session dir itself holds only paths.HarpMembers. A home that cannot be
// keyed to a lock location, or an acquisition failure, returns a no-op
// release rather than failing the run — see CopyAmbient's doc for why that
// case has no second writer.
func lockInstanceHome(instanceHome string) func() {
	lockPath, err := paths.HomePathFor(instanceHome)
	if err != nil {
		return func() {}
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), lockDirMode); err != nil {
		clidiag.Warn("ctxloom", "ambient copy-in: could not prepare the instance lock directory for %s (%v); proceeding unserialized", lockPath, err)
		return func() {}
	}
	fl := flock.New(lockPath, flock.SetPermissions(lockFileMode))
	stop := lockwait.Watch(lockPath)
	err = fl.Lock()
	stop()
	if err != nil {
		clidiag.Warn("ctxloom", "ambient copy-in: could not take the instance lock %s (%v); proceeding unserialized", lockPath, err)
		return func() {}
	}
	return func() { _ = fl.Unlock() }
}
