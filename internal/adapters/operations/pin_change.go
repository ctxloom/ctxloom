package operations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"reflect"
	"sort"
	"strings"

	"github.com/pmezard/go-difflib/difflib"

	"github.com/ctxloom/ctxloom/internal/adapters/content/remotetree"
	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/collections"
	"github.com/ctxloom/ctxloom/internal/shared/gitutil"
)

// PinChange is what moving (or first creating) one pin brings in: every item
// added, removed or modified, and every file. It is the disclosure `deps
// upgrade` shows before --yes applies it, and that pull, init and startup show
// for the first pins they create.
type PinChange struct {
	Identity string `json:"identity"`
	URL      string `json:"url"`
	// FromSHA is "" for a first pin, whose Items and Files are then the
	// bundle's whole initial content.
	FromSHA     string       `json:"from_sha"`
	ToSHA       string       `json:"to_sha"`
	FromVersion string       `json:"from_version"`
	ToVersion   string       `json:"to_version"`
	Items       []ItemChange `json:"items"`
	Files       []FileChange `json:"files"`
}

// ChangeKind is how one item or file differs between two pins.
type ChangeKind string

const (
	ChangeAdded    ChangeKind = "added"
	ChangeRemoved  ChangeKind = "removed"
	ChangeModified ChangeKind = "modified"
)

// ItemChange is one bundle item that differs between two pins.
type ItemChange struct {
	// Kind is hook, mcp, skill, command, fragment or profile.
	Kind   string     `json:"kind"`
	Name   string     `json:"name"`
	Change ChangeKind `json:"change"`
	// Exec is what a hook or MCP server runs, before and after; nil for every
	// other kind.
	Exec *ExecDelta `json:"exec,omitempty"`
}

// ExecSpec is what a hook or MCP server executes or dials.
//
// Env and Headers carry each value's fingerprint (valueFingerprint), never the
// value: they may hold credentials.
type ExecSpec struct {
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// ExecDelta is an executable item's spec at the old pin (nil when added) and
// at the new one (nil when removed).
type ExecDelta struct {
	Before *ExecSpec `json:"before"`
	After  *ExecSpec `json:"after"`
}

// FileChange is one file of the bundle's tree that differs between two pins.
type FileChange struct {
	Path   string     `json:"path"`
	Change ChangeKind `json:"change"`
	// Diff is a unified diff, set only for scripts: a file committed or
	// declared executable, or one under a scripts/ directory.
	Diff string `json:"diff,omitempty"`
}

// diffPin computes what moving p's pin from prior (has) to p.Hash brings in;
// without a prior pin it is everything the bundle carries at p.Hash.
//
// It reads the trees WITHOUT publisher verification. This is a disclosure of
// bytes, not an exposure of them, and an unsigned bundle has to be disclosed
// exactly as fully as a signed one.
func diffPin(ctx context.Context, cfg *config.Config, p PinnedRef, prior remote.LockEntry, has bool) (PinChange, error) {
	pc := PinChange{Identity: string(p.Identity), URL: p.URL, ToSHA: p.Hash, ToVersion: p.Version}
	if has {
		pc.FromSHA, pc.FromVersion = prior.SHA, prior.Version
	}
	ref, err := remote.ParseReference(string(p.Identity))
	if err != nil {
		return pc, err
	}
	factory := NewCachedFetcherFactory(cfg)
	auth := remote.LoadAuth(ProjectAppDir(cfg))
	toTree, toBundle, err := readPinTree(ctx, factory, auth, ref, p.Hash)
	if err != nil {
		return pc, err
	}
	var fromTree map[string]remote.TreeFile
	fromBundle := &bundles.Bundle{}
	if pc.FromSHA != "" {
		if fromTree, fromBundle, err = readPinTree(ctx, factory, auth, ref, prior.SHA); err != nil {
			return pc, err
		}
	}
	pc.Items = diffBundleItems(fromBundle, toBundle)
	pc.Files = diffTreeFiles(fromTree, toTree)
	return pc, nil
}

// readPinTree reads the bundle ref names at sha: its files and its items.
func readPinTree(ctx context.Context, factory remote.FetcherFactory, auth remote.AuthConfig, ref *remote.Reference, sha string) (map[string]remote.TreeFile, *bundles.Bundle, error) {
	c, err := remote.FetchRef(ctx, factory, auth, ref, sha, remotetree.PullTreeFetcher)
	if err != nil {
		return nil, nil, err
	}
	tree, err := remotetree.OpenFetchedBundle(ctx, path.Base(c.Root), c.Tree, ref.URL)
	if err != nil {
		return nil, nil, fmt.Errorf("opening %s at %s: %w", ref.String(), sha, err)
	}
	b, err := bundles.ReadTree(ctx, tree)
	if err != nil {
		return nil, nil, fmt.Errorf("reading %s at %s: %w", ref.String(), sha, err)
	}
	return c.Tree, b, nil
}

// diffBundleItems compares the two bundles map by map, in a fixed kind order.
func diffBundleItems(from, to *bundles.Bundle) []ItemChange {
	var out []ItemChange
	out = append(out, diffItems("hook", hookMap(from.Hooks), hookMap(to.Hooks), hookExec)...)
	out = append(out, diffItems("mcp", from.MCP, to.MCP, mcpExec)...)
	out = append(out, diffItems[bundles.BundleSkill]("skill", from.Skills, to.Skills, nil)...)
	out = append(out, diffItems[bundles.BundleCommand]("command", from.Commands, to.Commands, nil)...)
	out = append(out, diffItems[bundles.BundleFragment]("fragment", from.Fragments, to.Fragments, nil)...)
	out = append(out, diffItems[bundles.BundleProfile]("profile", from.Profiles, to.Profiles, nil)...)
	return out
}

// diffItems lists, by sorted name, the items of one kind that differ. exec,
// when non-nil, gives an item's ExecSpec.
func diffItems[V any](kind string, from, to map[string]V, exec func(V) *ExecSpec) []ItemChange {
	var out []ItemChange
	for _, name := range unionKeys(from, to) {
		f, inFrom := from[name]
		t, inTo := to[name]
		change, differs := changeOf(inFrom, inTo, reflect.DeepEqual(f, t))
		if !differs {
			continue
		}
		out = append(out, ItemChange{Kind: kind, Name: name, Change: change, Exec: execDelta(exec, f, inFrom, t, inTo)})
	}
	return out
}

// unionKeys is a's keys sorted, then b's keys a lacks, sorted.
func unionKeys[V any](a, b map[string]V) []string {
	keys := collections.SortedKeys(a)
	for _, k := range collections.SortedKeys(b) {
		if _, ok := a[k]; !ok {
			keys = append(keys, k)
		}
	}
	return keys
}

// changeOf classifies a key present inFrom and/or inTo; differs is false when
// it is in both and the same.
func changeOf(inFrom, inTo, same bool) (change ChangeKind, differs bool) {
	switch {
	case !inFrom:
		return ChangeAdded, true
	case !inTo:
		return ChangeRemoved, true
	case same:
		return "", false
	default:
		return ChangeModified, true
	}
}

// execDelta is an item's ExecSpec on each side it exists on; nil when exec is
// nil or neither side runs anything.
func execDelta[V any](exec func(V) *ExecSpec, f V, inFrom bool, t V, inTo bool) *ExecDelta {
	if exec == nil {
		return nil
	}
	var d ExecDelta
	if inFrom {
		d.Before = exec(f)
	}
	if inTo {
		d.After = exec(t)
	}
	if d.Before == nil && d.After == nil {
		return nil
	}
	return &d
}

// hookMap keys a bundle's hooks by HookEntry.ID.
func hookMap(h bundles.BundleHooks) map[string]bundles.BundleHook {
	out := map[string]bundles.BundleHook{}
	for _, e := range h.Entries() {
		out[e.ID()] = e.Hook
	}
	return out
}

// hookExec is what a hook runs; a hook that runs no command has none.
func hookExec(h bundles.BundleHook) *ExecSpec {
	if h.Command == "" {
		return nil
	}
	return &ExecSpec{Command: h.Command, Args: h.Args}
}

// mcpExec is what an MCP server runs or dials. Env and header VALUES are
// fingerprinted (valueFingerprint) here, at the source, so no rendering of a
// disclosure — text or JSON — can carry a raw secret; the names stay visible.
func mcpExec(m bundles.BundleMCP) *ExecSpec {
	return &ExecSpec{Command: m.Command, Args: m.Args, Env: fingerprintValues(m.Env), URL: m.URL, Headers: fingerprintValues(m.Headers)}
}

// fingerprintValues is m with every value replaced by its valueFingerprint.
func fingerprintValues(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = valueFingerprint(v)
	}
	return out
}

// valueFingerprint stands in for a value that may be a secret: the first 8 hex
// characters of its SHA-256, as "<a1b2c3d4>". Equal values match and a changed
// value reads as changed, without the value itself ever being shown.
func valueFingerprint(v string) string {
	sum := sha256.Sum256([]byte(v))
	return "<" + hex.EncodeToString(sum[:])[:8] + ">"
}

// diffTreeFiles lists, by sorted path, the files that differ between two
// trees; a nil from is an empty tree.
func diffTreeFiles(from, to map[string]remote.TreeFile) []FileChange {
	var out []FileChange
	for _, p := range unionKeys(from, to) {
		f, inFrom := from[p]
		t, inTo := to[p]
		same := string(f.Data) == string(t.Data) && isExecutable(f) == isExecutable(t)
		change, differs := changeOf(inFrom, inTo, same)
		if !differs {
			continue
		}
		fc := FileChange{Path: p, Change: change}
		if (inFrom && isScript(p, f)) || (inTo && isScript(p, t)) {
			fc.Diff = unifiedDiff(p, string(f.Data), string(t.Data))
		}
		out = append(out, fc)
	}
	return out
}

func isExecutable(f remote.TreeFile) bool { return f.DeclaredExecutable || f.CommittedExecutable }

// isScript is a file whose bytes may be run: executable, or under scripts/.
func isScript(p string, f remote.TreeFile) bool {
	return isExecutable(f) || strings.HasPrefix(p, "scripts/") || strings.Contains(p, "/scripts/")
}

func unifiedDiff(p, before, after string) string {
	text, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A: difflib.SplitLines(before), B: difflib.SplitLines(after),
		FromFile: "a/" + p, ToFile: "b/" + p, Context: 3,
	})
	if err != nil {
		return fmt.Sprintf("(diff unavailable: %v)", err)
	}
	return text
}

// pinChanges discloses every entry of after that before does not hold at the
// same commit: a first pin, or a moved one. An entry whose content cannot be
// read is still listed, by its header, and the reason is warned.
func pinChanges(ctx context.Context, cfg *config.Config, before, after *remote.Lockfile) []PinChange {
	entries := after.AllEntries()
	sort.Slice(entries, func(i, j int) bool { return entries[i].Ref < entries[j].Ref })
	var out []PinChange
	for _, e := range entries {
		prior, has := before.GetEntry(e.Type, e.Ref)
		if has && prior.SHA == e.Entry.SHA {
			continue
		}
		p := PinnedRef{Identity: e.Ref, Hash: e.Entry.SHA, URL: e.Entry.URL, Type: e.Type, Version: e.Entry.Version}
		pc, err := diffPin(ctx, cfg, p, prior, has)
		if err != nil {
			clidiag.Warn("ctxloom", "could not list what %s at %s brings in: %v", e.Ref, gitutil.ShortSHA(e.Entry.SHA), err)
		}
		out = append(out, pc)
	}
	return out
}
