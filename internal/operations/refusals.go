package operations

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"time"

	"github.com/spf13/afero"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/remote"
)

// THE RECORD OF WHAT UPGRADE WOULD NOT DO.
//
// `remote upgrade` refuses to advance a pin onto content whose publisher
// signature does not verify (see verifyAdvance), keeps the last verified pin,
// and says so. Because it refuses, nothing is withheld afterwards — the kept
// pin's content verifies fine — so every after-the-fact inspector truthfully
// reports a healthy project and the refusal lives only in the transient stdout
// of the sync that produced it. Close the terminal and the fact is gone; a
// teammate who did not run the sync never learns a revision exists at all.
//
// This file is the durable half. UpgradeDependencies writes the round's
// refusals here; `ctxloom doctor` reads them back and reports them as an
// advisory about the SIGNATURES UPSTREAM.
//
// WHEN A RECORD CLEARS — two independent mechanisms, because one of them is
// not enough:
//
//  1. WHOLESALE REPLACEMENT. Every UpgradeDependencies round that reaches its
//     lockfile write also rewrites this file with THAT round's refusals, and a
//     round with none DELETES it. The file therefore says what the last
//     upgrade said and nothing older: a refusal fixed upstream disappears the
//     next time anyone upgrades, without anybody having to remember to clear
//     it. It cannot accumulate, and there is no "manual clear" verb to forget
//     to run.
//
//  2. READ-TIME VALIDATION against the live lockfile (LiveRefusedAdvances). A
//     record claims "the pin for X is being KEPT at <sha>". If the lockfile no
//     longer has X, or X is no longer at that sha, the claim is false however
//     it got that way — `remote lock`, a re-pull, a hand-edited lock.yaml —
//     and the record is dropped rather than reported. Mechanism 1 alone would
//     miss all of those, because none of them runs an upgrade round.
//
// Together they are what stops a STALE record misleading, which is the failure
// this whole advisory could otherwise become: doctor reporting a problem that
// no longer exists is the same disease one level over, and worse than silence,
// because it teaches people to ignore doctor. What neither mechanism can do is
// notice a re-sign nobody has upgraded onto yet — so the advisory is worded as
// an AS-OF statement carrying RefusedAt, and names re-running the upgrade as
// the way to re-check.
//
// The deliberate cost of mechanism 1: an upgrade round that could not reach
// part of the closure (UpgradeResult.Incomplete) still replaces the file, so a
// refusal for an identity that round never re-evaluated is forgotten. That is
// under-reporting — the state before this file existed at all — and it is
// preferred over the alternative, which is a record that survives rounds that
// disagree with it.

// refusalStoreVersion is the only on-disk version this build understands. A
// future format change is reported rather than read as an empty store: this
// record's entire purpose is to keep a fact from evaporating, so misreading it
// as "nothing was refused" would reproduce exactly the silence it exists to
// prevent.
const refusalStoreVersion = 1

// RefusalRecord is one persisted RefusedAdvance: what upgrade declined to move
// to, what it kept instead, and when.
//
// It is a REPORT and never an input to a decision. Nothing gates on it, no
// trust verdict consults it, and RefusedAt in particular is display metadata —
// the same standing every timestamp in this codebase has.
type RefusalRecord struct {
	// Identity is the canonical ref of the item whose pin was not moved.
	Identity string `yaml:"identity"`
	// KeptSHA is the commit the pin stayed at — the last one that verified.
	// It is also the record's own validity check: see LiveRefusedAdvances.
	KeptSHA string `yaml:"kept_sha"`
	// ProposedSHA is the commit the constraint resolved to and that was
	// refused — the revision a user is asking "why is it not here?" about.
	ProposedSHA string `yaml:"proposed_sha"`
	// Detail is the verification failure in the verifier's own words.
	Detail string `yaml:"detail"`
	// RefusedAt is when the round that refused it ran, so the advisory can be
	// read as an as-of statement rather than a claim about right now.
	RefusedAt time.Time `yaml:"refused_at"`
}

// refusalDoc is the on-disk shape.
type refusalDoc struct {
	Version  int             `yaml:"version"`
	Refusals []RefusalRecord `yaml:"refusals"`
}

// refusalStorePath locates the record for cfg, or reports why it cannot.
//
// An EMPTY app path is a FAULT, not a default, for the reason
// admission.Store.configured documents: filepath.Join("", x) == x, so an
// unresolvable project root would silently read and write a
// "cache/refused_advances.yaml" under the process working directory — a file
// belonging to whatever directory the user happened to be standing in, which
// would then be reported as this project's state.
func refusalStorePath(cfg *config.Config) (string, error) {
	if cfg == nil {
		return "", errors.New("no config: there is no project whose refusals these would be")
	}
	appPaths := cfg.GetAppPaths()
	if len(appPaths) == 0 || appPaths[0] == "" {
		return "", errors.New("no .ctxloom directory resolved, refusing to read or write a refusal record relative to the working directory")
	}
	return paths.RefusedAdvancesPath(appPaths[0]), nil
}

// saveRefusedAdvances writes THIS round's refusals over whatever the file held,
// and DELETES the file when the round refused nothing.
//
// The delete is the clearing mechanism and is not an optimization: an upgrade
// that refuses nothing is the evidence that the previous refusal no longer
// applies, and leaving a file behind on that round is how the advisory would
// go stale.
func saveRefusedAdvances(cfg *config.Config, refused []RefusedAdvance) error {
	path, err := refusalStorePath(cfg)
	if err != nil {
		return err
	}
	fsys := getFS(cfg.FS())
	if len(refused) == 0 {
		if rmErr := fsys.Remove(path); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
			return fmt.Errorf("clear %s: %w", path, rmErr)
		}
		return nil
	}
	now := time.Now().UTC()
	recs := make([]RefusalRecord, 0, len(refused))
	for _, r := range refused {
		recs = append(recs, RefusalRecord{
			Identity:    r.Identity,
			KeptSHA:     r.KeptSHA,
			ProposedSHA: r.ProposedSHA,
			Detail:      r.Detail,
			RefusedAt:   now,
		})
	}
	sort.SliceStable(recs, func(i, j int) bool { return recs[i].Identity < recs[j].Identity })
	data, err := yaml.Marshal(refusalDoc{Version: refusalStoreVersion, Refusals: recs})
	if err != nil {
		return fmt.Errorf("marshal refusal records: %w", err)
	}
	if mkErr := fsys.MkdirAll(filepath.Dir(path), 0o755); mkErr != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), mkErr)
	}
	if werr := afero.WriteFile(fsys, path, data, 0o644); werr != nil {
		return fmt.Errorf("write %s: %w", path, werr)
	}
	return nil
}

// LiveRefusedAdvances returns the refusals that are STILL TRUE for cfg's
// project: recorded by the last upgrade round, and still describing the pin
// the lockfile actually holds.
//
// An absent file is the ordinary "nothing has been refused" state and yields
// no records and no error. A file that exists but cannot be read or parsed IS
// an error — the caller must be able to say "I could not check" rather than
// print the same clean bill of health an empty store would produce.
//
// The lockfile check is the staleness guard: a record whose KeptSHA is no
// longer what the lockfile pins is describing a world that has moved on, and
// is dropped. It is deliberately a READ-time filter and writes nothing — an
// inspector that repaired state as a side effect of being run would make
// `doctor` a mutating command.
func LiveRefusedAdvances(cfg *config.Config) ([]RefusalRecord, error) {
	path, err := refusalStorePath(cfg)
	if err != nil {
		return nil, err
	}
	fsys := getFS(cfg.FS())
	data, err := afero.ReadFile(fsys, path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var d refusalDoc
	if uerr := yaml.Unmarshal(data, &d); uerr != nil {
		return nil, fmt.Errorf("parse %s: %w", path, uerr)
	}
	if d.Version != refusalStoreVersion {
		return nil, fmt.Errorf("%s declares version %d, this build understands %d", path, d.Version, refusalStoreVersion)
	}
	pinned, err := lockedSHAsByRef(cfg)
	if err != nil {
		return nil, err
	}
	live := make([]RefusalRecord, 0, len(d.Refusals))
	for _, r := range d.Refusals {
		if sha, ok := pinned[r.Identity]; ok && sha == r.KeptSHA {
			live = append(live, r)
		}
	}
	return live, nil
}

// lockedSHAsByRef reads the active lock once and returns ref → pinned SHA.
// Keyed by ref alone rather than by (type, ref) because a refusal record
// carries no type: only bundles are ever refused (verifyAdvance returns early
// for anything else), and a ref is unique across the lock regardless.
func lockedSHAsByRef(cfg *config.Config) (map[string]string, error) {
	baseDir := getBaseDir(cfg)
	lock, err := remote.NewLockfileManager(baseDir, remote.WithLockfileFS(getFS(cfg.FS()))).Load()
	if err != nil {
		return nil, fmt.Errorf("read the lockfile a refusal record is checked against: %w", err)
	}
	out := map[string]string{}
	for _, e := range lock.AllEntries() {
		out[e.Ref] = e.Entry.SHA
	}
	return out, nil
}
