package isolation

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/ctxloom/ctxloom/internal/shared/cliversion"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// companionLookPath resolves a companion binary to its ADMITTED copy (see
// pinnedCompanionLookPath) — never a bare PATH lookup, which would bake
// whatever binary of that name happened to come first. It is the seam
// stageCompanions and companionVersionKey SHARE, so the key is computed over
// exactly the files a build would stage — a key describing a different binary
// than the one baked in would be worse than no key at all.
var companionLookPath = pinnedCompanionLookPath

// companionVersionProbe reads one companion's self-reported version. It goes
// through cliversion, the single owner of the `<bin> version --format json`
// contract that boot-time companion discovery reads too: two probes could
// disagree about what a companion's version IS, and the disagreement would
// surface as an image that never rebuilds.
var companionVersionProbe = cliversion.Probe

// companionKeySeparator joins the ctxloom half of the provenance key to the
// companion half (hostImageKeys). "+" and not "-" because the ctxloom half
// already contains "-" and a reader should be able to see where it ends.
const companionKeySeparator = "+c"

// companionTagSeparator is companionKeySeparator's form in an image TAG, where
// "+" is not a legal character.
const companionTagSeparator = "-c"

// companionVersionUnreportableToken stands in for a staged companion whose
// version could not be read. It is NOT a silent omission: the same condition
// raises a strictness finding that REFUSES the launch by default (see
// companionVersionKey). It exists for the --degraded path, which must still
// reach a working LLM, and it keeps that run's key honest — a companion whose
// version is unknown is a distinct key from both "absent" and any version it
// might have reported.
const companionVersionUnreportableToken = "\x00unreportable"

// companionVersionKey digests the self-reported version of every companion a
// build would stage into the agent image (companionBinaries), so updating a
// companion invalidates the image exactly as updating ctxloom does.
//
// WHY THIS EXISTS. The image bakes ctxloom AND its companions, but the
// provenance key was ctxloom's version alone: a new ltk, taskloom or reprise
// left the image reading as FRESH and the OLD companion baked in, silently,
// until ctxloom's own version happened to move. Companions in this project are
// released independently of ctxloom, so that window is not theoretical — and a
// stale binary producing confidently wrong work is the failure this project
// pays for most.
//
// A companion ABSENT from the host is not staged, and is simply not in the
// digest — installing one therefore changes the key, which is correct: the
// image it would be baked into is a different image.
//
// A companion PRESENT but unable to report a version BLOCKS: the finding is
// fatal by default and the launch refuses. That is deliberate, and it is the
// choice against "record it as absent and stay visible-if-you-look" — the
// whole defect being repaired is that nobody was looking at the companion
// half. `--degraded` is the way through (strictness owns that decision; this
// site must never branch on it).
func companionVersionKey() string {
	h := sha256.New()
	for _, name := range companionBinaries {
		path, err := companionLookPath(name)
		if err != nil {
			continue
		}
		version, err := companionVersionProbe(path)
		if err != nil {
			strictness.FailOnce(report.KindConfig,
				"repair or remove "+path+" so `"+name+" version --format json` answers, then re-run",
				"companion %s (%s) is staged into every agent image but cannot report a version (%v), so the image-staleness key cannot cover it and an image holding an OLD %s would read as fresh",
				name, path, err, name)
			version = companionVersionUnreportableToken
		}
		h.Write([]byte(name))
		h.Write([]byte{0})
		h.Write([]byte(version))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}
