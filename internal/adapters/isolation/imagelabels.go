package isolation

// agentImageRepo is the repository stem every ctxloom-built agent image is
// tagged under (the base as <stem>-base, a composed image as <stem>-<engine>).
// It NAMES images; it never proves ownership — a user may call their own image
// ctxloom-agent-foo. Ownership is the labels below, and only those.
const agentImageRepo = "ctxloom-agent"

// The build-time labels runImageBuild stamps (via --label, so they land on
// every stage, including a base built from the user's own Containerfile, whose
// text ctxloom does not control). They are what PlanImagePrune reads to decide
// what ctxloom owns and which slot an image fills, so the slot is never parsed
// back out of a tag whose grammar has already changed once.
const (
	// labelImageKind carries the ImageKind — its presence is the ownership proof.
	labelImageKind = "ctxloom.image"
	// labelImageSlot is the content key the image fills: composedContentHash
	// for a composed image, baseContentHash for a base.
	labelImageSlot = "ctxloom.slot"
	// labelImageCompanions is the companion half of hostImageKeys' tag key:
	// two HOMEs at one commit admit different companions and hold different
	// images, which must not supersede one another.
	labelImageCompanions = "ctxloom.companions"
	// labelImageFrom names the base ref a composed image was built FROM, so
	// pruning keeps the parent of every image it keeps.
	labelImageFrom = "ctxloom.from"
	// labelEngine is stamped by the composed Containerfile itself. With
	// provenanceLabel it is the ownership proof for an image built before
	// labelImageKind existed.
	labelEngine = "ctxloom.engine"
)

// ImageKind is what a ctxloom-built image is; its values are the
// ctxloom.image label's.
type ImageKind string

const (
	// ImageComposed is an engine's agent image (composedImageTagFor).
	ImageComposed ImageKind = "composed"
	// ImageBase is a stage-1 base image (baseImageTagFor).
	ImageBase ImageKind = "base"
)

// imageStamp is the ownership/slot label set one build carries. The zero value
// stamps nothing: an image ctxloom does not slot (a legacy fixed-tag image, a
// test build) stays unowned, and pruning never touches it.
type imageStamp struct {
	kind       ImageKind
	slot       string
	companions string
	from       string
}

// labelArgs renders the stamp as --label flags, omitting empty values so an
// absent fact stays absent rather than becoming an empty-string label.
func (s imageStamp) labelArgs() []string {
	if s.kind == "" {
		return nil
	}
	args := []string{"--label", labelImageKind + "=" + string(s.kind)}
	for _, kv := range [][2]string{
		{labelImageSlot, s.slot},
		{labelImageCompanions, s.companions},
		{labelImageFrom, s.from},
	} {
		if kv[1] != "" {
			args = append(args, "--label", kv[0]+"="+kv[1])
		}
	}
	return args
}
