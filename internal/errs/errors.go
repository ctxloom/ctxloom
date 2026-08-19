// Package errs provides sentinel errors for common failure cases.
// These errors enable programmatic error handling with errors.Is().
package errs

import "errors"

// NotFound errors indicate that a requested resource doesn't exist.
var (
	// ErrBundleNotFound indicates a bundle could not be located.
	ErrBundleNotFound = errors.New("bundle not found")

	// ErrFragmentNotFound indicates a fragment could not be located.
	ErrFragmentNotFound = errors.New("fragment not found")

	// ErrCommandNotFound indicates a command could not be located.
	ErrCommandNotFound = errors.New("command not found")

	// ErrSkillNotFound indicates an Agent Skill package could not be located.
	ErrSkillNotFound = errors.New("skill not found")

	// ErrBadItemRef indicates a well-formed "#<kind>/<name>" selector named a
	// kind the caller does not serve — e.g. "bundle#fragments/x" handed to a
	// command reader. Distinct from a malformed selector (which
	// bundles.ParseItemAsk reports as its own parse error): this is a
	// selector that parsed fine and simply asked for the wrong thing.
	ErrBadItemRef = errors.New("bad item reference")

	// ErrProfileNotFound indicates a profile could not be located.
	ErrProfileNotFound = errors.New("profile not found")

	// ErrFragmentWithheld indicates a fragment resolved but the per-item trust
	// gate (trust rework, TR5) withheld it: it exists but is not exposed because
	// the effective-content trust cascade denied it (untrusted, blacklisted, or
	// fail-closed on an evaluation error). Distinct from ErrFragmentNotFound so a
	// caller can tell "missing" from "exists-but-withheld" via errors.Is.
	ErrFragmentWithheld = errors.New("fragment withheld by trust gate")

	// ErrCommandWithheld indicates a command resolved but the per-item trust
	// gate (trust rework, TR5) withheld it. See ErrFragmentWithheld.
	ErrCommandWithheld = errors.New("command withheld by trust gate")

	// ErrSkillWithheld indicates an Agent Skill package resolved but the
	// per-item trust gate withheld it (or its on-disk tree failed manifest
	// verification against a signed bundle.yaml entry). See ErrFragmentWithheld.
	ErrSkillWithheld = errors.New("skill withheld by trust gate")

	// ErrNoVersionResolver indicates a per-commit-version resolution was
	// requested (multi-version coexistence, trust rework, TR5) but the loader
	// has no version resolver wired. A pinned historical version cannot be
	// materialized without one, so the version is withheld (fail-closed).
	ErrNoVersionResolver = errors.New("no bundle version resolver configured")

	// ErrRemoteNotFound indicates a remote could not be located.
	ErrRemoteNotFound = errors.New("remote not found")

	// ErrRemoteContentNotFound indicates a file, directory, or ref does not
	// exist on a remote (forge 404). Fetchers wrap it so callers can detect
	// "content removed on remote" via errors.Is instead of matching error text.
	// Its message names its subject, like every other sentinel here: a wrap
	// renders the sentinel's own text last, so a bare "not found" ended every
	// message in this family with a word that says nothing about what was
	// looked for.
	ErrRemoteContentNotFound = errors.New("remote content not found")

	// ErrRemoteNotMaterialized indicates a configured remote has no local clone
	// yet, so read-only operations that refuse to fetch on demand (list, search,
	// fetch from the cache) cannot see its content. Callers warn the user — with
	// next steps to materialize — and skip that remote rather than failing or
	// silently omitting it. Detect via errors.Is.
	ErrRemoteNotMaterialized = errors.New("remote not materialized locally")
)

// Configuration errors indicate problems with configuration.
var (
	// ErrCircularInheritance indicates a cycle in profile inheritance.
	ErrCircularInheritance = errors.New("circular profile inheritance detected")

	// ErrProfileDepthExceeded indicates profile inheritance nested past the
	// maximum allowed depth — a structural misconfiguration kept fatal (unlike a
	// merely missing/corrupt parent) so it is not masked by warn-and-continue.
	ErrProfileDepthExceeded = errors.New("profile inheritance depth exceeds maximum")
)

// ErrCancelled indicates an install/pull was deliberately abandoned — a user
// declined a confirmation prompt, or a retracted version was refused. Distinct
// from context.Canceled (a cancelled context); callers use errors.Is to treat
// a cancellation as "skipped" rather than a hard failure.
var ErrCancelled = errors.New("cancelled")
