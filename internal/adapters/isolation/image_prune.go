package isolation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// Superseded agent images are never removed on their own: composedImageTagFor
// mints a new tag per ctxloom commit and companion set by design (so versions
// coexist), which leaks one image per build. PlanImagePrune decides which of
// them are dead; ApplyImagePrune removes exactly those. Nothing here runs
// unless a human asks (`ctxloom container prune`, the doctor nudge), because
// many worktrees at different commits share one daemon, and an automatic
// sweep in one would delete another's image between its ensureImage and run.

// imagePruneProbeTimeout caps each listing/inspect/rmi call the sweep makes.
const imagePruneProbeTimeout = 2 * time.Minute

// OwnedImage is one image ctxloom provably built, read from its labels.
type OwnedImage struct {
	ID   string   `json:"id"`
	Refs []string `json:"refs,omitempty"` // repo:tag; empty for a dangling image
	// Kind is the ctxloom.image label; a pre-label image (provenance+engine
	// only) is a composed image by construction and reads as ImageComposed.
	Kind       ImageKind `json:"kind"`
	Engine     string    `json:"engine,omitempty"`
	Slot       string    `json:"slot,omitempty"` // "" for a pre-label image
	Companions string    `json:"companions,omitempty"`
	From       string    `json:"from,omitempty"`
	// OwnershipTag is the ctxloom.tag label: the per-build tag ownership is
	// proved by (ownershipTagFor). "" for a pre-label image.
	OwnershipTag string    `json:"ownership_tag,omitempty"`
	Created      time.Time `json:"created"`
	// Size is the image's UNIQUE-layer bytes as the runtime reports them —
	// what removing it alone frees — and 0 when the runtime reported none.
	Size int64 `json:"size"`
}

// Name is the image's display name: a ref other than its ownership tag (the
// primary tag it was built as, while it still holds it), else its ownership
// tag, else its ID.
func (o OwnedImage) Name() string {
	for _, ref := range o.Refs {
		if ref != o.OwnershipTag {
			return ref
		}
	}
	if len(o.Refs) > 0 {
		return o.Refs[0]
	}
	return o.ID
}

// KeepReason is why an owned image is kept; the zero value, Superseded, means
// it is not, and is what ApplyImagePrune removes.
type KeepReason int

const (
	// Superseded: no rule below holds — the image is dead.
	Superseded KeepReason = iota
	// KeepCurrent: the image a configured agent in this project resolves to.
	KeepCurrent
	// KeepReferenced: some container, running or stopped, uses it.
	KeepReferenced
	// KeepNewestInSlot: the newest image filling its (kind, engine, slot,
	// companions) slot — the protection for other projects, HOMEs and
	// worktrees whose current image this process cannot see.
	KeepNewestInSlot
	// KeepYoung: created within MinAge — a concurrent launch at another
	// commit may be about to run it.
	KeepYoung
	// KeepParent: a kept image names it as the base it was built FROM.
	KeepParent
)

var keepReasonNames = [...]string{"superseded", "current", "referenced", "newest-in-slot", "young", "parent"}

// String is the reason's stable machine name (also its JSON form).
func (r KeepReason) String() string {
	if r < 0 || int(r) >= len(keepReasonNames) {
		return fmt.Sprintf("KeepReason(%d)", int(r))
	}
	return keepReasonNames[r]
}

// MarshalText renders the reason by name, so structured output reads
// "newest-in-slot" rather than an enum ordinal.
func (r KeepReason) MarshalText() ([]byte, error) { return []byte(r.String()), nil }

// ImageVerdict is one owned image and whether (and why) it is kept.
type ImageVerdict struct {
	Image OwnedImage `json:"image"`
	Keep  KeepReason `json:"keep"`
}

// ImagePrunePlan is one runtime's decision over every image it holds that
// ctxloom owns, plus the name-alike images it saw and left alone.
type ImagePrunePlan struct {
	Runtime  string         `json:"runtime"`
	Verdicts []ImageVerdict `json:"verdicts"`
	// Unowned lists ctxloom-agent-* named images carrying no ownership label:
	// reported, never touched (a name proves nothing).
	Unowned []string `json:"unowned,omitempty"`
}

// Superseded returns the images the plan would remove.
func (p ImagePrunePlan) Superseded() []OwnedImage {
	var out []OwnedImage
	for _, v := range p.Verdicts {
		if v.Keep == Superseded {
			out = append(out, v.Image)
		}
	}
	return out
}

// Reclaimable sums the superseded images' unique-layer bytes. It is a floor:
// a layer shared only among superseded images is unique to none of them.
func (p ImagePrunePlan) Reclaimable() int64 {
	var n int64
	for _, img := range p.Superseded() {
		n += img.Size
	}
	return n
}

// ImagePruneOptions parameterize PlanImagePrune.
type ImagePruneOptions struct {
	// Live is the refs this project's configured agents resolve to
	// (LiveImageRef) — kept even when a newer image fills their slot.
	Live []string
	// MinAge keeps anything younger (the image-side grace window).
	MinAge time.Duration
	// Now is the instant ages are measured from.
	Now time.Time
}

// ImagePruneFailure is one removal the runtime refused.
type ImagePruneFailure struct {
	Image OwnedImage
	Err   error
}

// ImagePruneResult is what ApplyImagePrune did.
type ImagePruneResult struct {
	Removed []OwnedImage
	Failed  []ImagePruneFailure
}

// LiveImageRef is the image ref a containerized run of backend resolves to
// with this image configuration — containerFor's own resolution, computed
// without building or probing anything.
func LiveImageRef(rt Runtime, backend string, img ImageConfig) (string, bool) {
	ref := containerFor(rt, backend, img).image
	return ref, ref != ""
}

// PlanImagePrune lists the images rt holds, keeps every one ctxloom does not
// provably own, and decides for each owned one whether any keep rule holds.
// It removes nothing. Host (no binary) has nothing to plan.
//
// OWNERSHIP IS BY LABEL ONLY: ctxloom.image, or — for an image built before
// that label existed — BOTH ctxloom.provenance and ctxloom.engine, a pair only
// ctxloom's generated Containerfile stamps. The name listing below exists only
// so an unlabelled ctxloom-agent-* image can be REPORTED as skipped.
func PlanImagePrune(ctx context.Context, rt Runtime, opts ImagePruneOptions) (ImagePrunePlan, error) {
	plan := ImagePrunePlan{Runtime: rt.Name()}
	if rt.Binary() == "" {
		return plan, nil
	}
	ids, err := listImageCandidates(ctx, rt)
	if err != nil {
		return plan, err
	}
	owned, unowned, err := inspectImageCandidates(ctx, rt, ids)
	if err != nil {
		return plan, err
	}
	referenced, err := referencedImageIDs(ctx, rt)
	if err != nil {
		return plan, err
	}
	applyUniqueSizes(ctx, rt, owned)
	plan.Verdicts = classifyImages(owned, referenced, opts)
	plan.Unowned = unowned
	return plan, nil
}

// ApplyImagePrune removes every superseded image in plan, one `rmi` per image
// naming all its refs — its ownership tag among them, so no tag is left
// holding the image (an owned image always has one: ownership is proved by
// a ref) — never forced, so the runtime
// still refuses an image something started using since the plan was made. A
// failure is recorded with the runtime's own words and the sweep continues.
func ApplyImagePrune(ctx context.Context, rt Runtime, plan ImagePrunePlan) ImagePruneResult {
	var res ImagePruneResult
	for _, img := range plan.Superseded() {
		if _, err := pruneProbe(ctx, rt, rt.imageRemoveArgs(img.Refs...)); err != nil {
			res.Failed = append(res.Failed, ImagePruneFailure{Image: img, Err: err})
			continue
		}
		res.Removed = append(res.Removed, img)
	}
	return res
}

// pruneProbe runs one runtime CLI call under the sweep's timeout, folding the
// CLI's stderr into a failure so the report carries the runtime's reason.
func pruneProbe(ctx context.Context, rt Runtime, args []string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, imagePruneProbeTimeout)
	defer cancel()
	out, err := probeExec(cctx, rt.Binary(), args)
	if err == nil {
		return out, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(strings.TrimSpace(string(ee.Stderr))) > 0 {
		return out, fmt.Errorf("%s %s: %w: %s", rt.Binary(), args[0], err, strings.TrimSpace(string(ee.Stderr)))
	}
	return out, fmt.Errorf("%s %s: %w", rt.Binary(), args[0], err)
}

// imageCandidateFilters select what PlanImagePrune inspects: the two
// ownership labels, plus the agent repo name so unlabelled look-alikes are
// seen (and reported as unowned) rather than silently ignored.
func imageCandidateFilters() []string {
	return []string{
		"label=" + labelImageKind,
		"label=" + provenanceLabel,
		"reference=" + agentImageRepo + "*",
	}
}

// listImageCandidates unions the image IDs every candidate filter yields.
func listImageCandidates(ctx context.Context, rt Runtime) ([]string, error) {
	seen := map[string]bool{}
	var ids []string
	for _, f := range imageCandidateFilters() {
		out, err := pruneProbe(ctx, rt, rt.imageListArgs(f))
		if err != nil {
			return nil, err
		}
		for _, id := range strings.Fields(out) {
			if id = normalizeImageID(id); !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	return ids, nil
}

// imageInspectJSON is the slice of `image inspect` both docker and podman
// render identically.
type imageInspectJSON struct {
	ID       string    `json:"Id"`
	RepoTags []string  `json:"RepoTags"`
	Created  time.Time `json:"Created"`
	Config   struct {
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
}

// inspectImageCandidates inspects ids in one call and splits them into the
// images ctxloom owns and the display names of those it does not.
func inspectImageCandidates(ctx context.Context, rt Runtime, ids []string) ([]OwnedImage, []string, error) {
	if len(ids) == 0 {
		return nil, nil, nil
	}
	out, err := pruneProbe(ctx, rt, rt.imageInspectArgs("", ids...))
	if err != nil {
		return nil, nil, err
	}
	var raw []imageInspectJSON
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return nil, nil, fmt.Errorf("%s image inspect: %w", rt.Binary(), err)
	}
	var owned []OwnedImage
	var unowned []string
	for _, r := range raw {
		for i, ref := range r.RepoTags {
			r.RepoTags[i] = rt.canonicalRef(ref)
		}
		img, ok := ownedImage(r)
		if !ok {
			unowned = append(unowned, img.Name())
			continue
		}
		owned = append(owned, img)
	}
	sort.Strings(unowned)
	return owned, unowned, nil
}

// ownedImage reads one inspected image (refs already canonical); ok is the
// ownership proof. A labelled image must ALSO carry one of its own refs as
// ctxloom.tag: labels inherit through FROM, a tag does not, so a user image
// built on a ctxloom image — and a dangling one, which has no ref left to
// prove — is unowned.
func ownedImage(r imageInspectJSON) (OwnedImage, bool) {
	l := r.Config.Labels
	img := OwnedImage{
		ID:         normalizeImageID(r.ID),
		Refs:       r.RepoTags,
		Kind:       ImageKind(l[labelImageKind]),
		Engine:     l[labelEngine],
		Slot:       l[labelImageSlot],
		Companions: l[labelImageCompanions],
		From:       l[labelImageFrom],
		Created:    r.Created,
	}
	if img.Kind != "" {
		img.OwnershipTag = l[labelImageTag]
		return img, slices.Contains(img.Refs, img.OwnershipTag)
	}
	if l[provenanceLabel] != "" && l[labelEngine] != "" {
		img.Kind = ImageComposed // no slot label: rules 1, 2 and 4 only
		return img, true
	}
	return img, false
}

// referencedImageIDs is every image ID some container — running or stopped —
// was created from.
func referencedImageIDs(ctx context.Context, rt Runtime) (map[string]bool, error) {
	out, err := pruneProbe(ctx, rt, rt.containerListAllArgs())
	if err != nil {
		return nil, err
	}
	containers := strings.Fields(out)
	refs := map[string]bool{}
	if len(containers) == 0 {
		return refs, nil
	}
	out, err = pruneProbe(ctx, rt, rt.containerImageArgs(containers...))
	if err != nil {
		return nil, err
	}
	for _, id := range strings.Fields(out) {
		refs[normalizeImageID(id)] = true
	}
	return refs, nil
}

// applyUniqueSizes fills each image's unique-layer size. Best-effort: sizes
// only inform the report, so a runtime that cannot say leaves them 0 with a
// warning rather than blocking the plan.
func applyUniqueSizes(ctx context.Context, rt Runtime, imgs []OwnedImage) {
	if len(imgs) == 0 {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, imagePruneProbeTimeout)
	defer cancel()
	sizes, err := rt.imageUniqueSizes(cctx)
	if err != nil {
		clidiag.Warn("ctxloom", "image sizes (%s) unavailable, reclaimable bytes will read 0: %v", rt.Name(), err)
		return
	}
	for i := range imgs {
		imgs[i].Size = sizes[shortImageID(imgs[i].ID)]
	}
}

// slotKey is what "the same slot" means: two images differing in any of
// these fill different slots and never supersede one another.
type slotKey struct {
	kind                     ImageKind
	engine, slot, companions string
}

// classifyImages applies the keep rules — a pure decision over what the
// runtime reported, touching nothing. Verdicts come back ordered by name.
func classifyImages(imgs []OwnedImage, referenced map[string]bool, opts ImagePruneOptions) []ImageVerdict {
	live := map[string]bool{}
	for _, ref := range opts.Live {
		live[ref] = true
	}
	newest := newestPerSlot(imgs)
	verdicts := make([]ImageVerdict, len(imgs))
	for i, img := range imgs {
		verdicts[i] = ImageVerdict{Image: img, Keep: keepReason(img, live, referenced, newest, opts)}
	}
	keepParents(verdicts)
	sort.Slice(verdicts, func(i, j int) bool { return verdicts[i].Image.Name() < verdicts[j].Image.Name() })
	return verdicts
}

// keepReason applies rules 1-4 in order; the parent rule needs every other
// verdict first (keepParents).
func keepReason(img OwnedImage, live, referenced map[string]bool, newest map[slotKey]string, opts ImagePruneOptions) KeepReason {
	for _, ref := range img.Refs {
		if live[ref] {
			return KeepCurrent
		}
	}
	switch {
	case referenced[img.ID]:
		return KeepReferenced
	case img.Slot != "" && newest[keyOf(img)] == img.ID:
		return KeepNewestInSlot
	case opts.Now.Sub(img.Created) < opts.MinAge:
		return KeepYoung
	}
	return Superseded
}

func keyOf(img OwnedImage) slotKey {
	return slotKey{kind: img.Kind, engine: img.Engine, slot: img.Slot, companions: img.Companions}
}

// newestPerSlot maps each slot to its most recently created image's ID (ties
// broken by ID, so the answer never depends on listing order). Pre-label
// images have no slot and take no part.
func newestPerSlot(imgs []OwnedImage) map[slotKey]string {
	best := map[slotKey]OwnedImage{}
	for _, img := range imgs {
		if img.Slot == "" {
			continue
		}
		k := keyOf(img)
		cur, ok := best[k]
		if !ok || img.Created.After(cur.Created) || (img.Created.Equal(cur.Created) && img.ID > cur.ID) {
			best[k] = img
		}
	}
	out := make(map[slotKey]string, len(best))
	for k, img := range best {
		out[k] = img.ID
	}
	return out
}

// keepParents keeps every superseded image a kept image names as its FROM,
// to a fixed point (a parent kept this way may itself name a parent).
func keepParents(verdicts []ImageVerdict) {
	for changed := true; changed; {
		changed = false
		from := map[string]bool{}
		for _, v := range verdicts {
			if v.Keep != Superseded && v.Image.From != "" {
				from[v.Image.From] = true
			}
		}
		for i := range verdicts {
			if verdicts[i].Keep == Superseded && namedBy(verdicts[i].Image, from) {
				verdicts[i].Keep = KeepParent
				changed = true
			}
		}
	}
}

// namedBy reports whether any of img's refs, or its ID, is in names.
func namedBy(img OwnedImage, names map[string]bool) bool {
	if names[img.ID] {
		return true
	}
	for _, ref := range img.Refs {
		if names[ref] {
			return true
		}
	}
	return false
}

// normalizeImageID strips the digest algorithm prefix, which docker prints
// and podman does not, so IDs from either compare equal.
func normalizeImageID(id string) string {
	return strings.TrimPrefix(strings.TrimSpace(id), "sha256:")
}

// shortImageID is the 12-hex prefix both runtimes' disk-usage reports key by.
func shortImageID(id string) string {
	id = normalizeImageID(id)
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
