// Package confpatch writes ctxloom's changes into config files ctxloom does
// not own, and remembers exactly what it wrote so the next write can take it
// back out.
//
// The loop, per target, is:
//
//  1. find what ctxloom PREVIOUSLY applied to this target (its record),
//  2. apply that record's REVERSAL, restoring the user's bytes exactly,
//  3. apply the newly computed set,
//  4. write one new record.
//
// One computation, one apply, one record per target.
//
// The reversal is DIFFED from the before- and after-images rather than reasoned
// backwards from the ops that produced them. That is deliberate and it is what
// makes this possible today: hew's spec (§9.7) defers `hew revert` and its
// inversion rules — "what is the inverse of an `add` with `on_conflict: keep`?"
// — as an unsettled design task, while the diff-both-images half needs no
// inversion rules at all because both images existed. hew.Invert is that half.
//
// What this replaces, and why each alternative is worse, is recorded on the
// taskloom task this implements: a `_ctxloom` marker written into the user's
// file is pollution in a file ctxloom does not own and breaks the moment a user
// copies an entry; enumerating the file to find ctxloom's entries asks the
// foreign file a question only ctxloom's own memory can answer; a ledger of
// managed NAMES knows names, not values, so it cannot restore a value it
// replaced.
package confpatch

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/iox"

	hew "github.com/benjaminabbitt/hew/go"
	"github.com/spf13/afero"
)

// Store is the §9.7 application-record store. Records are home-rooted (see
// paths.HomeRecordsDir) for the same reason HomePathFor's lock sidecars are: the
// target file is FOREIGN, ctxloom does not own it, and so must never leave its
// own state beside it.
//
// A Store is ONE writer's memory. Records are keyed by target alone, so two
// writers sharing a store on one target would each reverse the other's last
// application on the way in; a writer that shares a file with another (taskloom
// manage and ctxloom both write .mcp.json) keeps its own store.
type Store struct {
	fs  afero.Fs
	dir string
	// owner is the executable basename that proves an entry is this writer's
	// own when no record accounts for it (see WithOwnedPaths and heal.go). A
	// BASENAME on purpose: agent.IsManaged compares exec tokens that way, so
	// the same entry is recognized whether it was written by the copy on PATH
	// or one built in a working tree — the two write different absolute
	// paths and neither is foreign.
	owner string
}

// NewStore opens the record store at dir on fs for the writer whose executable
// basename is owner. dir is created lazily, on the first record written, so
// merely constructing a Store touches no disk.
//
// AN ACCEPTABLY SMALL DUPLICATION, KEPT DELIBERATELY: removing it would
// produce MORE code than it deleted. The two-guard-then-construct shape this
// shares with content.NewAferoTreeFS is a constructor idiom, not extractable
// duplication. Each guard's entire value is its package-specific message —
// "confpatch: empty record directory" against "content: empty store root" —
// and the owner guard states a reason no generic validator could. A shared
// helper would have to take the package name and the noun as parameters to
// preserve those messages, so it would be longer than the few lines it
// replaced, and it would couple two unrelated packages to do it.
// reprise:accept-drift
func NewStore(recordFS afero.Fs, dir, owner string) (*Store, error) {
	if recordFS == nil {
		return nil, errors.New("confpatch: nil record filesystem")
	}
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("confpatch: empty record directory")
	}
	if strings.TrimSpace(owner) == "" {
		return nil, errors.New("confpatch: empty owner; the store cannot prove which recordless entries are its writer's own")
	}
	return &Store{fs: recordFS, dir: dir, owner: owner}, nil
}

// Result reports what one Apply did.
type Result struct {
	// RecordPath is the §9.7 record just written.
	RecordPath string
	// Before is the target's bytes as found, BEFORE the reversal of any prior
	// application — the user's file as it stood on disk.
	Before []byte
	// Restored is Before with the prior application reversed: the user's own
	// content, with ctxloom's previous entries removed. It equals Before when
	// there was no prior record.
	Restored []byte
	// After is what was written.
	After []byte
	// Reversed reports whether a prior record was found and reversed.
	Reversed bool
	// AdoptedPaths names entries of ctxloom's own that were taken back out
	// WITHOUT a record accounting for them (see WithOwnedPaths) — ctxloom
	// re-adopting its own leftovers, not a drift.
	AdoptedPaths []string
	// HealedPaths names the entries taken back out by ownership rather than by
	// the recorded reversal, because a second ctxloom had overwritten them (see
	// healOwnedDrift). Empty on every ordinary apply. A caller that can reach a
	// user SHOULD surface it: the file did change under ctxloom, and the reason
	// is worth one line even though it is not an error.
	HealedPaths []string
	// Changed reports whether After differs from Before.
	Changed bool
}

// Build records the caller's ops against the RESTORED document — the user's
// file with ctxloom's previous entries already taken back out.
//
// It receives the document rather than returning a prebuilt TransformList
// because ops cannot be built without one: addressing a nested path whose
// parent is absent is HEW013 no-match, so a caller has to know whether the
// container it writes into exists, and only the restored document can say.
//
// cur is that same content as a READ view (hew.Document). Sel exposes no
// existence query, so cur.Root().Member(name) is how a caller asks "is this
// container there?" before addressing into it.
//
// It returns the number of ops it recorded: an un-operated Doc is a caller bug
// to hew and Transforms() errors on it, so "I deliberately recorded nothing"
// has to be sayable — it is the ordinary case when ctxloom's desired set is
// empty and the reversal alone is the whole change.
type Build func(doc *hew.Doc, cur hew.Document) (recorded int, err error)

// ApplyOption configures one Apply.
type ApplyOption func(*applyConfig)

type applyConfig struct {
	ownedPaths []string
	dryRun     bool
}

// DryRun computes everything Apply would do — the reversal of the prior
// application, the new set, the proof that the new reversal round-trips — and
// writes NEITHER the target nor a record. Result.After is the document a real
// run would have written. A dry run that skipped the proof would show a
// document the real run then refuses, so the proof is kept.
func DryRun() ApplyOption {
	return func(c *applyConfig) { c.dryRun = true }
}

// WithOwnedPaths names the pointers the caller manages in this target.
//
// It exists for the case a RECORD cannot cover: an entry ctxloom itself wrote
// is sitting in the file with no record to reverse it — a cleared record store,
// a machine where ctxloom ran before this store existed, or an entry a user
// copied in by hand. Without this, ctxloom REPLACES that entry in place, and
// hew re-renders the container it edited in its own layout; the reversal then
// cannot reproduce the user's bytes and the write is refused, taking every
// hook and MCP server down with it. Removing ctxloom's own entry first and
// adding it back fresh sidesteps that entirely.
//
// It is not the file-enumeration this package's doc rejects. The caller states
// which pointers it manages, from its own knowledge; ownership at those
// pointers is then proved from the executable the entry runs, never its name,
// so an entry that is not ctxloom's is left exactly where it is.
func WithOwnedPaths(pointers ...string) ApplyOption {
	return func(c *applyConfig) { c.ownedPaths = append(c.ownedPaths, pointers...) }
}

// Apply runs the loop against one target: reverse what ctxloom applied last
// time, apply what build records, and write one record describing it.
//
// Callers that find building ops tedious should NOT reach around this function
// to write the file directly; the record is the whole point, and a write
// without one is the silent no-op this package exists to prevent.
//
// Nothing is written unless every step succeeds. In particular, a reversal that
// will not apply FAILS the whole call rather than being skipped: a reversal
// that no longer fits means the user edited the region ctxloom manages, and
// applying the new set on top of that would clobber their edit. Refusing is the
// documented behaviour hew's own staleness guard exists to produce.
func (s *Store) Apply(targetFS afero.Fs, target string, build Build, opts ...ApplyOption) (Result, error) {
	var res Result
	var cfg applyConfig
	for _, o := range opts {
		o(&cfg)
	}

	if targetFS == nil {
		return res, errors.New("confpatch: nil target filesystem")
	}
	if build == nil {
		return res, errors.New("confpatch: nil build function")
	}
	if strings.TrimSpace(target) == "" {
		return res, errors.New("confpatch: empty target path")
	}

	format, ok := hew.DetectFormat(filepath.Base(target))
	if !ok {
		return res, fmt.Errorf("confpatch: hew does not recognize %s's format from its name, so it cannot be patched byte-preservingly", target)
	}
	binding, ok := hew.Lookup(format)
	if !ok || binding.Applier == nil {
		return res, fmt.Errorf("confpatch: this build has no hew applier for %q, needed to write %s", format, target)
	}
	if binding.Document == nil {
		// A half binding can apply but not read, and a record needs the read
		// side. Refuse rather than write a record that states nothing.
		return res, fmt.Errorf("confpatch: this build has no hew document reader for %q, so a write to %s could not be recorded", format, target)
	}

	err := sessions.WithFileLock(targetFS, target, func() error {
		before, existed, err := readTarget(targetFS, target)
		if err != nil {
			return err
		}
		if !existed {
			before = emptyDocument(format)
		}
		res.Before = before

		// 1+2. Reverse what ctxloom applied here last time.
		//
		// Only when the target still EXISTS. The record store is home-rooted
		// and outlives the file it describes, so an absent target with a live
		// record is ordinary (a regenerated `--target` directory), not
		// suspicious: reversing into the empty document standing in for the
		// missing file fails no-match, and that read as drift — refusing the
		// write entirely. A file that is not there cannot be clobbered and
		// holds no ctxloom entries to take back out, so the previous
		// application is already reversed and the apply goes forward.
		restored := before
		prev, found, err := s.Last(target)
		if err != nil {
			return err
		}
		if existed && found && len(prev.Reversal) > 0 {
			restored, err = applyPatchText(binding, before, []byte(prev.Reversal), target)
			if err != nil {
				// The reversal not fitting means SOMEONE ELSE wrote the region
				// ctxloom manages. Refusing is right when that someone is the
				// user. It is wrong when it was another ctxloom, which happens
				// routinely and is not an edit anyone made: see healOwnedDrift.
				healed, healedPaths, ok := healOwnedDrift(binding, format, target, before, prev, s.owner)
				if !ok {
					return fmt.Errorf("confpatch: %s has drifted since ctxloom last wrote it, so the previous application could not be reversed; refusing to write rather than clobber the change: %w", target, err)
				}
				restored, res.HealedPaths = healed, healedPaths
			}
			res.Reversed = true
		}
		// Take out any entry of ctxloom's OWN that the reversal did not
		// account for — see WithOwnedPaths. Replacing such an entry in place is
		// what makes the reversal unrenderable; removing it and adding it back
		// fresh keeps the undo exact.
		if len(cfg.ownedPaths) > 0 {
			// No Recorded value: there is no record accounting for these, which
			// is the whole reason this path exists. Executable identity is the
			// only proof available for them.
			candidates := make([]ownedCandidate, 0, len(cfg.ownedPaths))
			for _, ptr := range cfg.ownedPaths {
				candidates = append(candidates, ownedCandidate{Pointer: ptr})
			}
			cleaned, removed, _, cerr := ownedRemovals(binding, format, target, restored, candidates, s.owner)
			if cerr == nil && len(removed) > 0 {
				restored = cleaned
				res.AdoptedPaths = removed
			}
		}
		res.Restored = restored

		// 3. Apply the newly computed set, recorded against the RESTORED
		// document so the caller addresses the user's file as it will actually
		// be written, not as it stood with ctxloom's old entries still in it.
		doc, err := hew.OpenBytes(target, restored, hew.As(format))
		if err != nil {
			return fmt.Errorf("confpatch: open %s for hew: %w", target, err)
		}
		cur, err := binding.Document(target, restored)
		if err != nil {
			return fmt.Errorf("confpatch: parse %s to build against: %w", target, err)
		}
		recorded, err := build(doc, cur)
		if err != nil {
			return fmt.Errorf("confpatch: build the changes for %s: %w", target, err)
		}

		var want hew.TransformList
		after := restored
		if recorded > 0 {
			if want, err = doc.Transforms(); err != nil {
				return fmt.Errorf("confpatch: lower %s's changes to hew transforms: %w", target, err)
			}
			if after, err = doc.Bytes(); err != nil {
				return fmt.Errorf("confpatch: apply ctxloom's changes to %s: %w", target, err)
			}
		}
		res.After = after
		res.Changed = string(after) != string(before)

		// A write that changes nothing writes nothing — no target, no record.
		// The prior record still describes the file accurately, so replacing it
		// with an identical one would grow the audit trail without adding a
		// fact to it.
		if !res.Changed {
			return nil
		}

		// 4. Derive this application's reversal from the two images, render it
		// as patch text, and write the record BEFORE the target: a record
		// describing a write that then fails is recoverable noise, whereas a
		// write with no record is exactly the ownership gap this closes.
		reversal, err := renderReversal(format, restored, after, target)
		if err != nil {
			return err
		}

		// PROVE THE REVERSAL BEFORE STORING IT. A reversal is only worth
		// keeping if it actually reverses, and that is checkable here and
		// nowhere else: both images are in hand, in memory, this instant.
		//
		// It is checked rather than trusted because a reversal can SUCCEED and
		// still hand back the wrong bytes — a hew defect did exactly that, and
		// silently, in the two formats written here. And it compounds: the next
		// write renders ITS reversal from the document this one restores, so a
		// bad reversal is inherited by every later write and no later write can
		// notice.
		//
		// Deliberately NOT a digest compared against a previous invocation. The
		// question is whether THIS reversal round-trips, which is a property of
		// the pair being written now — not whether the file changed since last
		// time, which is a legitimate thing for a user's file to do.
		// Only when the file already EXISTED. Creating one from nothing has no
		// document to restore to — a reversal takes ctxloom's entries back out,
		// it cannot express "make this file not exist" — so the round trip
		// legitimately cannot hold there and is not evidence of anything.
		if existed && !bytes.Equal(after, restored) {
			roundTripped, rtErr := applyPatchText(binding, after, reversal, target)
			if rtErr != nil {
				return fmt.Errorf("confpatch: the reversal computed for %s does not apply to the document it was derived from, so ctxloom could not take its own entries back out later; refusing to write: %w", target, rtErr)
			}
			if !bytes.Equal(roundTripped, restored) {
				// BYTE-EXACT ON PURPOSE, and there are TWO distinct ways to
				// reach it. Both are LEGITIMATE refusals — the file is the
				// user's, and handing it back other than as they wrote it is
				// not restoring it.
				//
				//  1. ORDER. hew's emitters write a map's keys sorted, so a Set
				//     on a container the document spelled in another order
				//     permutes it, and the reversal brings the content back
				//     without the position.
				//  2. STYLE. hew's applier re-renders a container it edited in
				//     its own layout rather than the document's. Observed
				//     against claude.ClaudeCodeHookWriter.applyMCP: a member
				//     patch under /mcpServers/<name> comes back with identical
				//     keys in identical order, collapsed from the document's
				//     indented form onto ONE line.
				//
				// Do not assume (1). It is the intuitive cause and it was the
				// only one named here, which is why (2) went undiagnosed: the
				// error text sent readers looking for a permutation that was
				// not there. Diff the two images before theorising.
				//
				// Do NOT relax this to a parsed or semantic comparison to make
				// either case pass. A parsed comparison cannot see order OR
				// layout, so it would accept exactly what this exists to catch,
				// and the deviation would then be inherited by every later
				// reversal rendered from this document.
				return fmt.Errorf("confpatch: the reversal computed for %s applies but does not restore the document it was derived from byte for byte, so ctxloom's entries could not be taken back out cleanly; refusing to write rather than store an undo that does not undo (the content is usually correct: compare the two images for a change of member ORDER or of LAYOUT, such as an edited container re-rendered onto one line)", target)
			}
		}

		if cfg.dryRun {
			return nil
		}

		recordPath, err := s.write(target, format, want, restored, after, reversal)
		if err != nil {
			return err
		}
		res.RecordPath = recordPath

		if err := writeTarget(targetFS, target, after); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return res, err
	}
	return res, nil
}

// renderReversal diffs after back to restored and renders the result as .hew
// patch text — the durable artifact the record keeps.
//
// The preamble is REQUIRED, not cosmetic: hew's parser refuses a document
// without the "hew: 1" line (§2.1), so a reversal rendered without it is
// unparseable and the record would carry an undo nothing can apply. It used to
// need an explicit RenderOptions{Preamble: true}; hew's zero value now emits it
// unconditionally, precisely because a zero-value Render that produced output
// its own ParseSingle rejected was a trap. Rendering here and re-parsing in
// applyPatchText is what proves the record's reversal is usable at the moment
// it is written rather than years later when it is needed.
func renderReversal(format hew.FormatID, before, after []byte, target string) ([]byte, error) {
	tl, err := hew.Invert(format, before, after, inversionOptions(target))
	if err != nil {
		return nil, fmt.Errorf("confpatch: derive how to undo the write to %s: %w", target, err)
	}
	out, err := hew.Render(tl, hew.RenderOptions{})
	if err != nil {
		return nil, fmt.Errorf("confpatch: render the reversal of the write to %s: %w", target, err)
	}
	return out, nil
}

// inversionOptions is how ctxloom inverts an application. Both inversions —
// the executable reversal and the record's audit statement — take it, so the
// Context choice below is made once and cannot drift between them.
//
// CONTEXT IS THE WHOLE POINT OF THIS FUNCTION. hew's differ defaults to
// §9.4-R2's sibling radius of 1: every UNCHANGED member adjacent to a changed
// one is emitted as a `test` assertion alongside the mutation. In a file
// ctxloom owns that is free strictness. In a file ctxloom does NOT own it is a
// standing assertion about the USER's content: the reversal for a write to
// /mcpServers/ctxloom asserted the neighbouring /mcpServers/remote-thing
// verbatim, headers and all. The user editing that neighbour — their own
// server, which ctxloom never wrote — then made the reversal itself refuse to
// apply, and every subsequent write aborted as drift, naming a path ctxloom has
// no business in. The file stayed wedged until someone deleted the record.
//
// ContextNone narrows the window to the CHANGED slots alone. It does not
// weaken the guard, and that is the load-bearing claim: hew emits a changed
// slot's own `test` carrying its full before-image regardless of the radius
// (its differ's window and tests functions are the pair to read), so the
// reversal still asserts every member ctxloom itself wrote, and a hand edit to
// one of those still refuses — which is what
// TestDriftRefusesAndLeavesTheTargetUntouched pins. What the radius drops is
// exactly the assertions about members ctxloom did not touch.
//
// It also stops ctxloom copying the user's adjacent secrets into its own
// home-rooted record store, which the radius did as a side effect.
//
// HINTS ARE ASKED FOR EXPLICITLY, and that is not redundant with the above.
// hew splits the two channels: Context governs the value-carrying `test`
// assertions, and HintContext governs non-asserting `~` neighbour lines that
// carry a KEY (or a content digest) and never a value. A hint cannot fail a
// match, so it buys the locator evidence to place a hunk without buying back
// either problem this function exists to avoid — a wedged reversal, or a
// neighbour's Authorization header copied into ctxloom's store.
//
// Left unset it would be silently OFF here: hew carries the ContextNone
// SENTINEL across to the hint channel (hintRadius reads Context when
// HintContext is zero), on the reading that "no context at all" is a body-wide
// request. That reading is right for a caller who means it body-wide. ctxloom
// does not: it narrows the ASSERTING channel because the file is the user's,
// and wants every bit of non-asserting location it can get. Saying so is the
// difference between that and inheriting the answer to a different question.
func inversionOptions(target string) hew.DiffOptions {
	return hew.DiffOptions{
		Target:      target,
		Context:     hew.ContextNone,
		HintContext: hew.HintContextDefault,
	}
}

// applyPatchText parses stored .hew text and applies it. Parsing at APPLY time,
// from the same bytes the record holds, is what makes the stored reversal a
// real artifact rather than a description of one.
func applyPatchText(b hew.Binding, src, patch []byte, target string) ([]byte, error) {
	tl, err := hew.ParseSingle(patch)
	if err != nil {
		return nil, fmt.Errorf("parse the recorded reversal for %s: %w", target, err)
	}
	out, err := b.Applier(src, tl)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// emptyDocument is a format's empty document, used as the pre-image when the
// target does not exist yet. Format-specific and deliberately not one shared
// literal: "{}" is an empty JSON object, but in TOML it is a parse error —
// an empty TOML document is zero bytes.
func emptyDocument(format hew.FormatID) []byte {
	switch format {
	case hew.FormatJSON, hew.FormatJSONC:
		return []byte("{}")
	default:
		return nil
	}
}

func readTarget(fs afero.Fs, target string) ([]byte, bool, error) {
	exists, err := afero.Exists(fs, target)
	if err != nil {
		return nil, false, fmt.Errorf("confpatch: stat %s: %w", target, err)
	}
	if !exists {
		return nil, false, nil
	}
	data, err := afero.ReadFile(fs, target)
	if err != nil {
		return nil, false, fmt.Errorf("confpatch: read %s: %w", target, err)
	}
	return data, true, nil
}

func writeTarget(fs afero.Fs, target string, out []byte) error {
	if dir := filepath.Dir(target); dir != "." {
		if err := fs.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("confpatch: create directory for %s: %w", target, err)
		}
	}
	if err := iox.AtomicWriteFile(fs, target, out, filepath.Base(target)); err != nil {
		return fmt.Errorf("confpatch: write %s: %w", target, err)
	}
	return nil
}
