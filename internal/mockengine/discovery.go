package mockengine

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// Resolver supplies the ambient facts the discovery walk resolves probe roots
// against — this process's real cwd and home, and its environment. It is an
// interface-free struct so a test can drive the walk over a temp workspace
// without touching the real process environment.
type Resolver struct {
	// Cwd is the working directory ScopeCwd probes resolve against.
	Cwd string
	// Home is the $HOME ScopeHome / ScopeEnvDir-fallback probes resolve against.
	Home string
	// Getenv reads an environment variable (os.Getenv in production).
	Getenv func(string) string
}

func (r Resolver) getenv(k string) string {
	if r.Getenv == nil {
		return ""
	}
	return r.Getenv(k)
}

// Walk resolves and probes every context surface the engine's CLI declaration
// lists, in declaration order, returning one ProbeRecord per probe — INCLUDING
// present:false rows for absent paths and for flag-value probes whose flag was
// not in argv. It is the single discovery engine every personality shares: it
// asks L1 (the EngineCLI) where things live and never restates per-backend
// knowledge. The one rule it does encode — that a --settings value beginning
// with '{' is an inline JSON literal rather than a path — is L1's own
// ValuePathOrJSON contract, read off the declared flag, not a claude fact
// hardcoded here.
func Walk(cli agent.EngineCLI, argv agent.ParsedArgv, res Resolver) []ProbeRecord {
	out := make([]ProbeRecord, 0, len(cli.Probes))
	for i, p := range cli.Probes {
		out = append(out, probeOne(i, p, cli, argv, res))
	}
	return out
}

// ObserveEnv reads the engine's DECLARED environment contract off the child's
// actual environment: one record per SetEnv variable (ctxloom promised to set
// it) and one per StripEnv variable (ctxloom promised to remove it), in
// declaration order.
//
// This is the same inversion the probe walk performs, applied to the other half
// of the delivery contract. agent.EngineCLI declares StripEnv precisely because
// "a strip that silently stopped happening is otherwise invisible" — and until
// U079-F05 nothing read either list, so invisible is exactly what it was. A
// variable that is set but EMPTY is recorded as such: a delivery that ran and
// passed nothing is this project's characteristic bug, not a delivery.
// lookup is the TWO-VALUE environment reader, because "unset" and "set to the
// empty string" must not collapse here of all places. It is never defaulted to
// the real process environment behind a caller's back: a walk that quietly
// reads the developer's own environment is the same false green as the $HOME
// probe fallback (U079-F04).
func ObserveEnv(cli agent.EngineCLI, lookup func(string) (string, bool)) []EnvRecord {
	if lookup == nil {
		lookup = func(string) (string, bool) { return "", false }
	}
	out := make([]EnvRecord, 0, len(cli.SetEnv)+len(cli.StripEnv))
	for _, name := range cli.SetEnv {
		out = append(out, observeEnvOne(name, EnvExpectSet, lookup))
	}
	for _, name := range cli.StripEnv {
		out = append(out, observeEnvOne(name, EnvExpectStripped, lookup))
	}
	return out
}

// observeEnvOne records one variable.
func observeEnvOne(name, expect string, lookup func(string) (string, bool)) EnvRecord {
	val, ok := lookup(name)
	return EnvRecord{Name: name, Expect: expect, Present: ok, Empty: ok && val == ""}
}

// probeOne resolves and observes a single probe.
func probeOne(order int, p agent.CLIProbe, cli agent.EngineCLI, argv agent.ParsedArgv, res Resolver) ProbeRecord {
	rec := ProbeRecord{
		Order: order,
		Kind:  string(p.Kind),
		Scope: string(p.Scope),
		Rel:   p.Rel,
		Dir:   p.Dir,
		Note:  p.Note,
	}

	switch p.Scope {
	case agent.ScopeFlagValue:
		return probeFlagValue(rec, p, cli, argv)
	case agent.ScopeCwd:
		rec.Root = res.Cwd
		return observePath(rec, filepath.Join(res.Cwd, p.Rel), p.Dir)
	case agent.ScopeHome:
		rec.Root = res.Home
		return observePath(rec, filepath.Join(res.Home, p.Rel), p.Dir)
	case agent.ScopeEnvDir:
		root := res.getenv(p.EnvVar)
		if root == "" {
			// The fallback is RECORDED, not silent. ctxloom points CODEX_HOME
			// at a per-run directory; a run where it did not is a run where
			// this surface was never delivered, and the fallback root is the
			// DEVELOPER's own ~/.codex — which statts present:true and hashes
			// their personal config. Without this marker the two runs render
			// identically in the digest, so the host path greens on evidence
			// ctxloom never produced (U079-F04).
			root = filepath.Join(res.Home, p.EnvHomeDefault)
			rec.Fallback = true
			rec.Note = joinNote(rec.Note, "env "+p.EnvVar+" unset — fell back to $HOME/"+p.EnvHomeDefault)
		}
		rec.Root = root
		return observePath(rec, filepath.Join(root, p.Rel), p.Dir)
	default:
		// No resolver for this scope, so nothing was ever looked at. Marked
		// rather than left as a bare present:false, which would read as an
		// established absence.
		rec.Unresolved = true
		rec.Note = "unknown probe scope " + string(p.Scope)
		return rec
	}
}

// probeFlagValue handles a ScopeFlagValue probe: its path comes from a declared
// flag's argv value. The flag being ABSENT is a first-class observation
// (present:false), because that is what "the driver did not deliver this
// surface on this run" looks like. When the flag's declared shape allows inline
// JSON (claude's --settings under SkipSetup) and the value begins with '{', the
// value IS the content — hashed directly, never statted as a path.
func probeFlagValue(rec ProbeRecord, p agent.CLIProbe, cli agent.EngineCLI, argv agent.ParsedArgv) ProbeRecord {
	rec.Root = "flag:" + p.Flag
	val, ok := argv.Value(p.Flag)
	if !ok {
		// The surface has no location on this run at all — distinct from a
		// located path that turned out to be empty, and the digest must be able
		// to tell them apart.
		rec.Present = false
		rec.Unresolved = true
		rec.Note = joinNote(rec.Note, "flag "+p.Flag+" absent from argv")
		return rec
	}
	flag, _ := cli.LookupFlag(p.Flag)
	if inlineJSON(flag.Value, val) {
		rec.Present = true
		b := []byte(val)
		rec.Size = int64(len(b))
		rec.SHA256 = hashBytes(b)
		rec.Head = head(b)
		rec.Note = joinNote(rec.Note, "inline JSON literal in argv (not a file)")
		return rec
	}
	rec.Note = joinNote(rec.Note, "path from flag "+p.Flag)
	return observePath(rec, val, p.Dir)
}

// inlineJSON reports whether a flag value is a literal JSON document rather than
// a path, per L1's declared value shape. Only shapes that ADMIT inline JSON are
// eligible, and only when the token actually begins with '{' — so a plain path
// under a path-or-json flag still statts as a path.
func inlineJSON(shape agent.ValueShape, val string) bool {
	switch shape {
	case agent.ValueJSON:
		return true
	case agent.ValuePathOrJSON:
		return strings.HasPrefix(strings.TrimSpace(val), "{")
	default:
		return false
	}
}

// observePath statts and hashes a concrete path, filling Present/Size/SHA256 (a
// file) or Entries (a directory). An absent path yields Present=false with no
// hash — never an error, never a dropped record.
func observePath(rec ProbeRecord, path string, dir bool) ProbeRecord {
	rec.Path = path
	info, err := os.Stat(path)
	if err != nil {
		rec.Present = false
		if !os.IsNotExist(err) {
			// "Not there" and "I could not look" arrive through the same error
			// return. Only the first establishes absence; reporting the second
			// as absence makes the instrument that exists to prove delivery
			// report a delivered surface as undelivered (U079-F06).
			rec.Unreadable = true
			rec.Note = joinNote(rec.Note, "unstattable: "+err.Error())
		}
		return rec
	}
	if dir || info.IsDir() {
		rec.Dir = true
		entries, walkErr := hashDir(path)
		rec.Present = true
		rec.Entries = entries
		rec.Size = int64(len(entries))
		rec.SHA256 = hashBytes([]byte(canonicalEntries(entries)))
		if walkErr != nil {
			// The listing is INCOMPLETE. Without this the truncated listing
			// hashed cleanly and read as a full, faithful observation.
			rec.Unreadable = true
			rec.Note = joinNote(rec.Note, "incomplete listing: "+walkErr.Error())
		}
		return rec
	}
	b, err := os.ReadFile(path)
	if err != nil {
		// Present on disk but unreadable — report present with no hash and a
		// note, rather than pretending it is absent.
		rec.Present = true
		rec.Unreadable = true
		rec.Note = joinNote(rec.Note, "unreadable: "+err.Error())
		return rec
	}
	rec.Present = true
	rec.Size = int64(len(b))
	rec.SHA256 = hashBytes(b)
	rec.Head = head(b)
	return rec
}

// hashDir walks a directory and returns every regular file beneath it, keyed by
// slash-separated relative path, name-sorted, each hashed. Recursive so nested
// command/skill package files are captured; deterministic so the directory's
// own hash is stable.
func hashDir(root string) ([]EntryRecord, error) {
	var entries []EntryRecord
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		rel = filepath.ToSlash(rel)
		b, readErr := os.ReadFile(path)
		e := EntryRecord{Name: rel}
		if readErr != nil {
			// Listed but unread: say so. An entry with an empty SHA256 and no
			// marker is indistinguishable from one nobody hashed (U079-F07).
			e.Unreadable = true
		} else {
			e.Size = int64(len(b))
			e.SHA256 = hashBytes(b)
		}
		entries = append(entries, e)
		return nil
	})
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, walkErr
}

// canonicalEntries renders a directory's entries as one deterministic line each,
// so a directory surface has a single assertable hash.
func canonicalEntries(entries []EntryRecord) string {
	var b strings.Builder
	for _, e := range entries {
		b.WriteString(e.Name)
		b.WriteByte('|')
		b.WriteString(e.SHA256)
		b.WriteByte('\n')
	}
	return b.String()
}

// joinNote appends to an existing note, keeping the declaration's precedence
// note and the resolution caveat both visible.
func joinNote(existing, add string) string {
	if existing == "" {
		return add
	}
	return existing + "; " + add
}
