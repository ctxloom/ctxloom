package agent

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/afero"
)

// DeliveryStatus is a route's currency verdict.
type DeliveryStatus string

const (
	// StatusDelivered: what the route carries matches what was composed.
	StatusDelivered DeliveryStatus = "delivered"
	// StatusStale: the route carries ctxloom content that no longer matches.
	StatusStale DeliveryStatus = "stale"
	// StatusMissing: the route exists and carries nothing.
	StatusMissing DeliveryStatus = "missing"
	// StatusEphemeral: the route does not PERSIST — it delivers per run, so
	// there is nothing on disk to diff and "stale" would be a lie. A
	// system-prompt append is the case: the next run carries the current
	// composition whatever the last one carried, so reporting it stale sends a
	// user to fix something that is already correct.
	StatusEphemeral DeliveryStatus = "ephemeral"
)

// Currency is one route's answer about what it carries.
type Currency struct {
	Status DeliveryStatus
	// Detail explains any status that is not plainly good, in the route's own
	// terms — a path for a file, a hash for a per-run append.
	Detail string
}

// DeliveryState is what ONE delivery route currently carries and how it answers
// for currency.
//
// It is POLYMORPHIC because routes differ in kind, not merely in path: a
// managed-marker file has bytes to diff, while a launch-time system-prompt
// append has only the hash of the last run. A single struct of {path, present,
// content} would bake the file-shaped answer in and could not represent the
// second at all.
type DeliveryState interface {
	// Route names the delivery mechanism for a human: "CLAUDE.md",
	// "system prompt (per run)".
	Route() string
	// Currency reports whether what this route carries matches intended — the
	// context as currently composed.
	Currency(intended string) Currency
}

// StateReader is the OPTIONAL read half of a Delivery.
//
// It is optional rather than a method on Delivery for the same reason
// content.TrustGated is an optional interface rather than a nullable field: a
// route that cannot be observed is then structurally absent from a report,
// instead of forcing every caller to remember a "not applicable" case. Reporting
// an unobservable route as "missing" is the failure mode this avoids — it is the
// kind of false alarm that trains users to ignore a status command.
//
// It is deliberately NOT a parallel hierarchy to Delivery. The SAME object that
// applied the context answers for it, which is what makes a status report
// truthful by construction: there is no second code path that can disagree with
// the one that delivered.
type StateReader interface {
	State(dir string) (DeliveryState, error)
}

// FileDeliveryState is the DeliveryState of a route that owns a human-editable
// file with a ctxloom-managed marker section.
//
// It compares the MANAGED SECTION only. Content outside the markers is the
// user's and must never make a current surface read as stale — that is the
// whole point of the marker convention on the write side, and the read side has
// to honour the same boundary or it reports drift the user cannot act on.
type FileDeliveryState struct {
	// Rel is the surface's path relative to the project dir.
	Rel string
	// Found reports that the file exists at all.
	Found bool
	// HasSection reports that a ctxloom-managed section is present in it.
	HasSection bool
	// Managed is the managed section's bytes ("" when absent).
	Managed string
}

func (s FileDeliveryState) Route() string { return s.Rel }

// Currency compares the managed section against the composed context.
func (s FileDeliveryState) Currency(intended string) Currency {
	switch {
	case !s.Found:
		return Currency{Status: StatusMissing, Detail: fmt.Sprintf("%s does not exist", s.Rel)}
	case !s.HasSection:
		return Currency{Status: StatusMissing,
			Detail: fmt.Sprintf("%s exists but carries no ctxloom-managed section", s.Rel)}
	case strings.TrimSpace(s.Managed) == strings.TrimSpace(intended):
		return Currency{Status: StatusDelivered}
	default:
		return Currency{Status: StatusStale,
			Detail: fmt.Sprintf("%s carries a managed section that no longer matches the composed context", s.Rel)}
	}
}

// ReadManagedContext is the shared read-side counterpart to
// WriteManagedContext: it reports what a managed-marker file currently holds.
//
// Every native-file route goes through it rather than splitting the markers
// itself, for the same reason WriteManagedContext exists — three backends
// reimplementing the merge is how the claude data-loss bug happened, and three
// reimplementing the SPLIT would be the same mistake pointed the other way.
func ReadManagedContext(fsys afero.Fs, path, rel string) (FileDeliveryState, error) {
	raw, err := afero.ReadFile(fsys, path)
	if err != nil {
		if os.IsNotExist(err) {
			return FileDeliveryState{Rel: rel}, nil
		}
		return FileDeliveryState{Rel: rel}, fmt.Errorf("read %s: %w", path, err)
	}
	section, ok := managedSection(string(raw))
	return FileDeliveryState{Rel: rel, Found: true, HasSection: ok, Managed: section}, nil
}

// managedSection returns the bytes BETWEEN the managed markers.
func managedSection(content string) (string, bool) {
	begin := strings.Index(content, ManagedContextBegin)
	if begin < 0 {
		return "", false
	}
	rest := content[begin+len(ManagedContextBegin):]
	end := strings.Index(rest, ManagedContextEnd)
	if end < 0 {
		// An opening marker with no close is a TRUNCATED surface, not an absent
		// one. Saying "no section" would render a half-written file as merely
		// undelivered; it is a real fault and the caller must see the difference.
		return "", false
	}
	return strings.Trim(rest[:end], "\n"), true
}

// OwnedFileDeliveryState is the DeliveryState of a route that owns its file
// OUTRIGHT — no marker convention, because nothing hand-authored ever lives in
// it.
//
// It is a separate type from FileDeliveryState rather than a flag on it
// because the two answer "delivered?" from different evidence: the marker file
// compares a SECTION and must ignore everything around it, while a
// ctxloom-owned file compares the WHOLE payload. Reading an owned file through
// FileDeliveryState would find no markers and report every delivered kiro or
// opencode surface as missing — the exact false alarm StateReader's doc warns
// about.
type OwnedFileDeliveryState struct {
	// Rel is the surface's path relative to the project dir.
	Rel string
	// Found reports that the file exists at all.
	Found bool
	// Content is the ctxloom-written payload, with any writer-added frame
	// (kiro's steering front matter) already stripped by the reader — so it
	// compares against the composed context directly.
	Content string
}

func (s OwnedFileDeliveryState) Route() string { return s.Rel }

// Currency compares the whole owned payload against the composed context.
func (s OwnedFileDeliveryState) Currency(intended string) Currency {
	switch {
	case !s.Found:
		return Currency{Status: StatusMissing, Detail: fmt.Sprintf("%s does not exist", s.Rel)}
	case strings.TrimSpace(s.Content) == strings.TrimSpace(intended):
		return Currency{Status: StatusDelivered}
	default:
		return Currency{Status: StatusStale,
			Detail: fmt.Sprintf("%s no longer matches the composed context", s.Rel)}
	}
}

// ReadOwnedContext is ReadManagedContext's sibling for a wholly ctxloom-owned
// context file: it reports what the file currently holds, verbatim. A caller
// whose writer frames the payload (kiro's `inclusion: always` front matter)
// strips that frame off the returned Content before comparing — the frame is
// the writer's, so only the writer's package knows it.
//
// An absent file is NOT an error: it is the missing verdict, which is the whole
// question a currency read is asked.
func ReadOwnedContext(fsys afero.Fs, path, rel string) (OwnedFileDeliveryState, error) {
	raw, err := afero.ReadFile(fsys, path)
	if err != nil {
		if os.IsNotExist(err) {
			return OwnedFileDeliveryState{Rel: rel}, nil
		}
		return OwnedFileDeliveryState{Rel: rel}, fmt.Errorf("read %s: %w", path, err)
	}
	return OwnedFileDeliveryState{Rel: rel, Found: true, Content: string(raw)}, nil
}
