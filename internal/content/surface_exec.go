package content

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/wire"
	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// execForms is the single LAYOUT form an executable surface carries: the BASE
// form, whose component has an unsuffixed filename ("mcp/postgres.yaml").
//
// Naming the role here would be the wrong axis, not a stricter one. What a
// countersignature binds is the composite ATTESTATION form ("exec/mcp"), and
// that is derived from the item's KIND at the trust layer — so this axis stays
// purely about layout, and an executable surface reports the same base form a
// never-distilled document does.
var execForms = []signing.Form{signing.FormRaw}

// detectSingleYAML is the shared recognition for the executable surfaces: exactly
// one non-sidecar component, a .yaml file, at the expected depth below the kind
// directory. depth 0 means a direct child ("mcp/postgres.yaml"); depth 1 means one
// intermediate directory ("hooks/pre_tool/guard.yaml").
//
// Requiring an exact depth is what lets the walker stay kind-agnostic: a hook
// EVENT directory offers itself as a candidate first and is declined here because
// it holds two content files (or, holding one, resolves to the same ref either
// way), so the walker descends and the event becomes a path segment rather than an
// item.
func detectSingleYAML(dir string, src Source, depth int) (string, bool) {
	paths, err := src.List()
	if err != nil {
		return "", false
	}
	content := nonMetaPaths(paths)
	if len(content) != 1 {
		return "", false
	}
	rel, ok := relToKind(dir, content[0])
	if !ok || path.Ext(rel) != ".yaml" {
		return "", false
	}
	if strings.Count(rel, "/") != depth {
		return "", false
	}
	name := strings.TrimSuffix(rel, ".yaml")
	if name == "" || strings.HasPrefix(path.Base(name), ".") {
		return "", false
	}
	return name, true
}

// readExecItem decodes a single-YAML surface's content file and, when the kind has
// one, its metadata sidecar.
//
// A nil meta target means the kind has no sidecar, and a sidecar found anyway is
// REFUSED rather than skipped. Skipping it would leave bytes that are grouped into
// the item and hashed into the digest but that nothing in the decode path can
// explain — exactly the shape in which unexplained content rides along under a
// valid signature.
func readExecItem(t SurfaceType, src Source, depth int, content, meta any) (string, error) {
	dir := t.Dir()
	name, ok := detectSingleYAML(dir, src, depth)
	if !ok {
		return "", fmt.Errorf("%w: not a %s item", ErrUnrecognized, dir)
	}
	paths, err := src.List()
	if err != nil {
		return "", err
	}
	for _, p := range paths {
		target := content
		if IsMetaPath(p) {
			if err := refuseUnexplainedMeta(t, name, p); err != nil {
				return "", err
			}
			if meta == nil {
				return "", fmt.Errorf("content: %s declares a metadata file but supplied no decode target for %q", dir, p)
			}
			target = meta
		}
		data, err := src.Open(p)
		if err != nil {
			return "", err
		}
		if err := unmarshalYAML(data, target); err != nil {
			return "", fmt.Errorf("content: %s: %w", p, err)
		}
	}
	return name, nil
}

// encodeExecItem renders an executable surface: the content file, plus a
// dot-prefixed sidecar when there is any ctxloom metadata to record.
//
// The split is the point. The content file stays PURE configuration in the shape
// the consuming tool expects, with none of our keys (name, order, executable) in
// it — many JSON/YAML consumers reject unknown keys, and polluting a foreign
// contract to store our bookkeeping is how a format stops being directly usable
// by the tool that owns its schema. Our keys go in the sidecar, which is a
// component of the item and therefore hashed: changing metadata changes Content
// and invalidates the signature, exactly as changing the content file does.
func encodeExecItem(t SurfaceType, name string, content, meta any) ([]Component, error) {
	if name == "" {
		return nil, fmt.Errorf("%w: surface has no name", ErrSurfaceType)
	}
	dir := t.Dir()
	contentPath := itemPath(dir, name, ".yaml")
	if err := validateDigestPath(contentPath); err != nil {
		return nil, err
	}
	body, err := marshalYAML(content)
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		body = []byte("{}\n")
	}
	out := []Component{{Path: contentPath, Mode: ModeRegular, Bytes: body}}
	if meta == nil {
		return out, nil
	}
	metaPath, hasMeta := t.Meta().PathFor(dir, name)
	if !hasMeta {
		return nil, fmt.Errorf("%w: %s supplied metadata but its MetaStore declares no metadata file", ErrSurfaceType, dir)
	}
	sidecar, err := marshalYAML(meta)
	if err != nil {
		return nil, err
	}
	if len(sidecar) > 0 {
		out = append(out, Component{Path: metaPath, Mode: ModeRegular, Bytes: sidecar})
	}
	return out, nil
}

// --------------------------------------------------------------------- mcp

// MCP is an MCP server definition: an executable surface, one form.
type MCP struct {
	Name    string
	Command string
	Args    []string
	Env     map[string]string
	// Notes and Installation are human-facing and live in the sidecar, keeping
	// the content file consumable by an MCP client as-is.
	Notes        string
	Installation string
	// ContentHash is change-detection bookkeeping, carried verbatim, never a
	// trust input.
	ContentHash string
}

func (MCP) Kind() trust.ItemKind      { return trust.KindMCP }
func (MCP) TrustKind() trust.ItemKind { return trust.KindMCP }

// mcpContent is the content file's shape: what an MCP client needs, nothing else.
type mcpContent struct {
	Command string            `yaml:"command,omitempty"`
	Args    []string          `yaml:"args,omitempty"`
	Env     map[string]string `yaml:"env,omitempty"`
}

// mcpMeta is the sidecar's shape: our keys only.
type mcpMeta struct {
	Notes        string `yaml:"notes,omitempty"`
	Installation string `yaml:"installation,omitempty"`
	ContentHash  string `yaml:"content_hash,omitempty"`
}

type mcpType struct{}

func (mcpType) Name() string { return trust.KindMCP.Dir() }
func (mcpType) Dir() string  { return trust.KindMCP.Dir() }

// Meta: a sidecar, so mcp/<name>.yaml stays pure MCP-client config with none of
// our keys in it.
func (mcpType) Meta() MetaStore { return SidecarMeta{} }

func (t mcpType) Detect(src Source) bool {
	_, ok := detectSingleYAML(t.Dir(), src, 0)
	return ok
}

func (t mcpType) Forms(src Source) ([]signing.Form, error) {
	if _, ok := detectSingleYAML(t.Dir(), src, 0); !ok {
		return nil, fmt.Errorf("%w: not an mcp item", ErrUnrecognized)
	}
	return execForms, nil
}

func (t mcpType) RefFor(bundle string, src Source) (trust.Ref, error) {
	name, ok := detectSingleYAML(t.Dir(), src, 0)
	if !ok {
		return trust.Ref{}, fmt.Errorf("%w: not an mcp item", ErrUnrecognized)
	}
	return trust.Ref{Bundle: bundle, Kind: trust.KindMCP, Name: name}, nil
}

func (t mcpType) Decode(src Source) (Surface, error) {
	var content mcpContent
	var meta mcpMeta
	name, err := readExecItem(t, src, 0, &content, &meta)
	if err != nil {
		return nil, err
	}
	return MCP{
		Name:         name,
		Command:      content.Command,
		Args:         content.Args,
		Env:          content.Env,
		Notes:        meta.Notes,
		Installation: meta.Installation,
		ContentHash:  meta.ContentHash,
	}, nil
}

func (t mcpType) Encode(s Surface) ([]Component, error) {
	m, ok := s.(MCP)
	if !ok {
		return nil, fmt.Errorf("%w: %T is not an MCP", ErrSurfaceType, s)
	}
	return encodeExecItem(t, m.Name,
		mcpContent{Command: m.Command, Args: m.Args, Env: m.Env},
		mcpMeta{Notes: m.Notes, Installation: m.Installation, ContentHash: m.ContentHash})
}

// -------------------------------------------------------------------- hooks

// Hook is one lifecycle hook. Its trust identity is "<event>/<name>" — the NAME,
// never an ordinal position.
//
// The old identity was the hook's INDEX within its event, which is connascence of
// position: inserting a hook at the top of an event silently changed the identity
// of every hook below it, invalidating approvals for items that had not changed.
//
// # Order IS a field, and here is why the earlier retraction no longer holds
//
// An earlier draft proposed a per-hook `order:` and then RETRACTED it on the
// correct grounds that nothing read it: within an event, hooks merged by pure
// APPEND across every source, so sequence was a property of merge order that a
// single hook's file could not express. A field nobody reads is this project's
// characteristic bug — it looks load-bearing and is silently ignored.
//
// That reasoning was sound and is now obsolete, because the field HAS readers:
//
//   - SortHooks, which is how a tree's hooks resolve into execution order at all.
//     The tree enumerates by SORTED FILENAME, and names now carry identity only,
//     so without this field the tree has NO carrier for order — the alternative
//     is the retired `<NN>-` filename ordinal, which is positional identity under
//     another name.
//   - The apply path: bundles.BundleHook carries the same field, and
//     config.extractHooksFromBundle orders each event by it before appending.
//   - The inspection surface (`ctxloom manage hooks list`), which reports the
//     resolved order and the value that produced it.
//
// The division of labour is the part worth holding onto: a bundle's own order
// field sequences ITS hooks WITHIN an event; the order sources are merged in
// sequences the bundles. Append across bundles is unchanged.
//
// Order lives in a metadata SIDECAR, not in the content file — see hookType.Meta.
type Hook struct {
	// Event is the lifecycle event, and the first path segment of the ref.
	Event string
	// Name is the hook's name within the event, and the second segment.
	Name string

	// Order is this hook's declared position within its event, sparse (see
	// wire.HookOrderStep). A POINTER, because "declared 0" and "declared
	// nothing" resolve differently — see wire.HookOrderLess — and a plain int
	// could not tell them apart.
	Order *int

	Matcher         string
	Type            string
	Command         string
	Prompt          string
	Timeout         int
	Async           bool
	PreToolFallback bool
}

// HookOrderStep is re-exported so a caller ordering content.Hooks does not have
// to reach past this package for the spacing that produced the values.
const HookOrderStep = wire.HookOrderStep

// SortHooks sorts one event's hooks into execution order, in place.
//
// It is a STABLE sort over a TOTAL rule, which matters more than it looks: the
// failure this whole change guards against is a tree that holds the right bytes
// in the wrong order, and such a tree is byte-identical to a correct one. Nothing
// downstream can detect it, so the ordering has to be deterministic here.
//
// Callers must pass hooks from a SINGLE event. Ordering is only meaningful within
// an event, and a slice spanning events would interleave them into a sequence no
// engine ever executes.
func SortHooks(hooks []Hook) {
	sort.SliceStable(hooks, func(i, j int) bool {
		return wire.HookOrderLess(hooks[i].Order, hooks[i].Name, hooks[j].Order, hooks[j].Name)
	})
}

func (Hook) Kind() trust.ItemKind      { return trust.KindHook }
func (Hook) TrustKind() trust.ItemKind { return trust.KindHook }

// Ref name is "<event>/<name>".
func (h Hook) refName() string { return h.Event + "/" + h.Name }

// hookContent is the hook's behavioural configuration. It deliberately carries
// NEITHER the name NOR the order: the filename is the identity, order is ctxloom
// bookkeeping, and keeping both out of the content file keeps them out of any
// payload a later layer builds from those bytes — which is what lets existing hook
// approvals and content-rejections survive with no exec-preimage contract bump.
type hookContent struct {
	Matcher         string `yaml:"matcher,omitempty"`
	Type            string `yaml:"type,omitempty"`
	Command         string `yaml:"command,omitempty"`
	Prompt          string `yaml:"prompt,omitempty"`
	Timeout         int    `yaml:"timeout,omitempty"`
	Async           bool   `yaml:"async,omitempty"`
	PreToolFallback bool   `yaml:"pre_tool_fallback,omitempty"`
}

// hookMeta is the sidecar's shape: our keys only, which today is exactly one.
//
// Residency follows encodeExecItem's rule verbatim — the content file stays PURE
// hook configuration with none of ctxloom's keys in it. `order` is ours: it says
// nothing about what the hook DOES, only about when we run it relative to its
// siblings. There is a second payoff, and it is the one that matters at apply
// time: reordering an event rewrites SIDECARS, leaving every hook's behavioural
// bytes untouched, so a reader diffing what a hook actually executes sees nothing.
type hookMeta struct {
	Order *int `yaml:"order,omitempty"`
}

type hookType struct{}

func (hookType) Name() string { return trust.KindHook.Dir() }
func (hookType) Dir() string  { return trust.KindHook.Dir() }

// Meta: a sidecar, so hooks/<event>/<name>.yaml stays pure hook configuration.
// The sidecar is a COMPONENT and therefore hashed — changing a hook's order
// invalidates that hook's signature, exactly as changing its command does, rather
// than order riding along unattested.
func (hookType) Meta() MetaStore { return SidecarMeta{} }

func (t hookType) Detect(src Source) bool {
	_, ok := detectSingleYAML(t.Dir(), src, 1)
	return ok
}

func (t hookType) Forms(src Source) ([]signing.Form, error) {
	if _, ok := detectSingleYAML(t.Dir(), src, 1); !ok {
		return nil, fmt.Errorf("%w: not a hook item", ErrUnrecognized)
	}
	return execForms, nil
}

func (t hookType) RefFor(bundle string, src Source) (trust.Ref, error) {
	name, ok := detectSingleYAML(t.Dir(), src, 1)
	if !ok {
		return trust.Ref{}, fmt.Errorf("%w: not a hook item", ErrUnrecognized)
	}
	return trust.Ref{Bundle: bundle, Kind: trust.KindHook, Name: name}, nil
}

func (t hookType) Decode(src Source) (Surface, error) {
	var content hookContent
	var meta hookMeta
	name, err := readExecItem(t, src, 1, &content, &meta)
	if err != nil {
		return nil, err
	}
	event, hookName, _ := strings.Cut(name, "/")
	return Hook{
		Event:           event,
		Name:            hookName,
		Order:           meta.Order,
		Matcher:         content.Matcher,
		Type:            content.Type,
		Command:         content.Command,
		Prompt:          content.Prompt,
		Timeout:         content.Timeout,
		Async:           content.Async,
		PreToolFallback: content.PreToolFallback,
	}, nil
}

func (t hookType) Encode(s Surface) ([]Component, error) {
	h, ok := s.(Hook)
	if !ok {
		return nil, fmt.Errorf("%w: %T is not a Hook", ErrSurfaceType, s)
	}
	if h.Event == "" || h.Name == "" {
		return nil, fmt.Errorf("%w: a hook needs both an event and a name (got event=%q name=%q)", ErrSurfaceType, h.Event, h.Name)
	}
	if strings.ContainsAny(h.Event, `/\`) || strings.ContainsAny(h.Name, `/\`) {
		return nil, fmt.Errorf("%w: hook event %q and name %q must each be a single path segment", ErrBadPath, h.Event, h.Name)
	}
	return encodeExecItem(t, h.refName(),
		hookContent{
			Matcher:         h.Matcher,
			Type:            h.Type,
			Command:         h.Command,
			Prompt:          h.Prompt,
			Timeout:         h.Timeout,
			Async:           h.Async,
			PreToolFallback: h.PreToolFallback,
		},
		// marshalYAML renders an all-omitempty struct as nothing, so a hook that
		// declares no order writes NO sidecar — absence is represented by the
		// absence of a file, not by an empty one. An empty `{}` per hook would be
		// bytes in the digest that mean nothing, and would make "authored before
		// the field existed" indistinguishable from "deliberately unordered".
		hookMeta{Order: h.Order})
}
