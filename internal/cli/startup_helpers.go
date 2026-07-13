package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// exitCodeFatalFindings is the exit code for a strict-mode startup abort:
// distinct from 1 (ordinary command errors) and from the wrapped LLM's own
// exit codes on the happy path, so callers/scripts can tell "ctxloom refused
// to launch over collected findings" apart from everything else.
const exitCodeFatalFindings = 3

// loadConfigOrFallback loads ctxloom config via loader, falling back to a
// minimal config rooted at .ctxloom when loading fails. The fallback path
// emits a stderr-style warning to w so users in unconfigured directories
// can still diagnose why their effective config is incomplete.
//
// Used by tools that must keep working without a fully-loaded config
// (`ctxloom remote update`, `ctxloom search`) per CLAUDE.md fault tolerance.
func loadConfigOrFallback(loader func() (*config.Config, error), w io.Writer) *config.Config {
	cfg, err := loader()
	if err != nil {
		// Best-effort warning. This runs in fault-tolerant startup paths
		// (`ctxloom remote update`, `ctxloom search`) that must proceed
		// regardless, so a failed warning write has nowhere to go and is
		// intentionally dropped (captured-but-unchecked via iox.ErrWriter).
		ew := iox.NewErrWriter(w)
		clidiag.Fwarn(ew, "ctxloom", "failed to load config (%v); using minimal default rooted at .ctxloom", err)
		return &config.Config{AppPaths: []string{".ctxloom"}}
	}
	return cfg
}

// printConfigWarnings echoes the warnings config.Load accumulated to w (one
// "ctxloom: warning: ..." line each) AND records each as a fatal finding for
// the strict startup gate. Load downgrades unreadable, malformed, and
// schema-invalid config files (and lossy migrations) to kind-tagged Warnings
// and returns a nil error, so every startup path that consumes a loaded config
// must surface them — otherwise a corrupted config.yaml silently launches an
// empty-context session. A present-but-broken config is fatal-class
// (fail-loudly): `ctxloom run` / `ctxloom mcp` / `ctxloom acp` abort on the
// recorded findings unless --degraded; management commands never check
// findings, so for them this stays pure warning output. Shared by `ctxloom
// run`, `ctxloom mcp`, and the GetConfig-based command entrypoints.
func printConfigWarnings(w io.Writer, warnings []config.Warning) {
	// Best-effort reporting on fault-tolerant startup paths; failed writes
	// are intentionally dropped (captured-but-unchecked via iox.ErrWriter).
	ew := iox.NewErrWriter(w)
	for _, warning := range warnings {
		clidiag.Fwarn(ew, "ctxloom", "%s", warning.Text)
		strictness.Record(configWarningClass(warning.Kind), configWarningFixIt(warning.Kind), "%s", warning.Text)
	}
}

// configWarningClass maps a config warning kind to its strictness class.
func configWarningClass(kind config.WarningKind) strictness.Class {
	if kind == config.WarnKindMigrationLossy {
		return strictness.ClassMigration
	}
	return strictness.ClassConfig
}

// configWarningFixIt names the fix for a config warning kind (the finding's
// message already carries the path and error detail).
func configWarningFixIt(kind config.WarningKind) string {
	switch kind {
	case config.WarnKindRead:
		return "make the config file readable, or remove it"
	case config.WarnKindMigrationLossy:
		return "re-add the dropped setting in its new home (ctxloom manage config edit)"
	case config.WarnKindUnknownKey:
		// The message already names the key and (when known) its replacement, so
		// the fix-it only has to say where to make the edit.
		return "remove or rename the key in config.yaml (ctxloom manage config edit)"
	default: // parse / validate
		return "fix the config file (ctxloom manage config edit)"
	}
}

// failOnFindings is the strict startup gate for process-owning entry points
// (`ctxloom run`, `ctxloom mcp`): when strict mode collected fatal findings
// since mark, it prints ALL of them — never just the first — each with its
// fix-it, plus the --degraded escape hatch, and returns an ExitError carrying
// the distinct findings exit code (3). Returns nil in degraded mode or when
// nothing was collected.
func failOnFindings(w io.Writer, mark strictness.Mark) error {
	msg := formatFindings(strictness.Since(mark))
	if msg == "" {
		return nil
	}
	fmt.Fprintln(w, msg)
	return &ExitError{Code: exitCodeFatalFindings}
}

// findingsError is the per-session variant for servers that must keep running
// (`ctxloom acp`): the same full-list rendering, returned as an error for the
// protocol layer to surface to the client instead of exiting the process.
func findingsError(mark strictness.Mark) error {
	msg := formatFindings(strictness.Since(mark))
	if msg == "" {
		return nil
	}
	return errors.New(msg)
}

// formatFindings renders the collected findings block: a header naming the
// count and the escape hatch, then one "[class] message" + "fix:" pair per
// finding. Empty when there is nothing to report or the process is degraded.
func formatFindings(findings []strictness.Finding) string {
	if len(findings) == 0 || strictness.Degraded() {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "ctxloom: aborting startup: %d fatal finding(s); fix them, or rerun with --degraded (env CTXLOOM_DEGRADED=1) to launch anyway:", len(findings))
	for _, f := range findings {
		fmt.Fprintf(&b, "\n  - [%s] %s", f.Class, f.Message)
		if f.FixIt != "" {
			fmt.Fprintf(&b, "\n    fix: %s", f.FixIt)
		}
	}
	return b.String()
}

// reportCompanions probes the built-in companion binaries (taskloom, ltk) on
// PATH and logs each one found with its self-reported version, so a boot
// transcript shows exactly which companion versions the session was wired
// with. A present binary that fails the probe (predates `version --format json`,
// wedged, not actually the companion) gets a warning but stays wired — PATH
// presence is the gating signal, the version is reporting (CLAUDE.md fault
// tolerance). Missing binaries stay silent here: the bundle resolvers emit
// the one-shot install hint when they skip those entries.
//
// Both `ctxloom mcp` startup and `ctxloom run` call this so the surface is
// the same regardless of how the session was started.
func reportCompanions(w io.Writer) {
	// Best-effort reporting on fault-tolerant startup paths; failed writes
	// are intentionally dropped (captured-but-unchecked via iox.ErrWriter).
	ew := iox.NewErrWriter(w)
	for _, st := range config.ProbeCompanions() {
		switch {
		case st.Path == "":
		case st.Err != nil:
			clidiag.Fwarn(ew, "ctxloom", "companion %s (%s): %v", st.Bin, st.Path, st.Err)
		default:
			ew.Printf("ctxloom: companion %s %s\n", st.Bin, st.Version)
		}
	}
}

// writeSyncSummary prints a consistent summary of a SyncDependenciesResult
// to w. Both `ctxloom mcp` startup and `ctxloom run` use this so users see
// the same surface regardless of how sync was triggered.
//
//   - Successful syncs that installed or updated something get a one-line
//     status message.
//   - Failures get a "completed with N errors" warning plus a per-item
//     breakdown so the user can diagnose which bundle/profile failed and
//     why (important for hard-fail clone errors that used to silently
//     degrade to API).
//
// Each failed item is also recorded as a fatal sync finding for the strict
// startup gate: on the startup path a Failed entry is exactly the fatal class
// — a pinned/configured item that was missing (not satisfiable from cache;
// installed items are "skipped") AND whose fetch failed. A refresh failure
// with a complete cache never lands here and stays a plain warning in both
// modes.
//
// A nil result is a no-op so callers don't have to nil-check.
func writeSyncSummary(w io.Writer, result *operations.SyncDependenciesResult) {
	if result == nil {
		return
	}
	// Best-effort summary. Both `ctxloom mcp` startup and `ctxloom run`
	// call this on a fault-tolerant path that must never block, so a
	// failed write to the summary target is intentionally dropped
	// (captured-but-unchecked via iox.ErrWriter).
	ew := iox.NewErrWriter(w)
	if result.Status != "up_to_date" && result.Installed+result.Updated > 0 {
		ew.Printf("ctxloom: %s\n", result.Message)
	}
	if result.Errors > 0 {
		clidiag.Fwarn(ew, "ctxloom", "sync completed with %d errors", result.Errors)
		for _, item := range result.Failed {
			ew.Printf("ctxloom:   - %s (%s): %s\n", item.Reference, item.Type, item.Error)
			strictness.Record(strictness.ClassSync, "check network/auth and retry (ctxloom remote pull), or drop the reference from its profile",
				"sync: %s (%s) is neither cached nor fetchable: %s", item.Reference, item.Type, item.Error)
		}
	}
}
