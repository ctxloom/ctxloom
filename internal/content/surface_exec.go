package content

import (
	"fmt"
	"path"
	"strings"

	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// execForms is the single form an executable surface carries. Reporting FormRaw
// here instead would be a correctness bug, not a naming nit: the countersign
// header binds (assertion, kind, ref, form), so a layer above would rebuild the
// wrong preimage and every existing mcp/hook approval would silently fail to
// match.
var execForms = []signing.Form{signing.FormExec}

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
func readExecItem(dir string, src Source, depth int, content, meta any) (string, error) {
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
			if meta == nil {
				return "", fmt.Errorf("content: %s carries the metadata sidecar %q, but %s items have no sidecar", name, p, dir)
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
func encodeExecItem(dir, name string, content, meta any) ([]Component, error) {
	if name == "" {
		return nil, fmt.Errorf("%w: surface has no name", ErrSurfaceType)
	}
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
	sidecar, err := marshalYAML(meta)
	if err != nil {
		return nil, err
	}
	if len(sidecar) > 0 {
		out = append(out, Component{Path: MetaPath(contentPath), Mode: ModeRegular, Bytes: sidecar})
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
	name, err := readExecItem(t.Dir(), src, 0, &content, &meta)
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
	return encodeExecItem(t.Dir(), m.Name,
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
// # There is deliberately NO order field
//
// An earlier draft of this design kept the ordinal as "declared metadata". It is
// gone, because VERIFIED against the merge path it would have been a field that
// looks load-bearing and is silently ignored — this project's characteristic bug:
//
//   - The ONLY thing that ever read a per-hook ordinal was IDENTITY:
//     HookEntry.ID() rendering "<event>/<index>" and EntryByID parsing it back
//     (internal/bundles/bundles.go, consumed by operations/trust.go). Name
//     identity removes both readers.
//   - hookEventOrder orders EVENTS, not hooks within an event, and it stays.
//   - Within an event, hooks are merged by pure APPEND across every source
//     (wire.UnifiedHooks.Append, agent.MergeHooksConfig). Nothing sorts, and
//     nothing consults a per-hook field. Sequence is a property of the order
//     bundles and profiles are merged in, which a single hook's file cannot
//     express and must not appear to.
//
// So hooks enumerate by NAME within an event, and a hook has no metadata of its
// own — hence no sidecar.
type Hook struct {
	// Event is the lifecycle event, and the first path segment of the ref.
	Event string
	// Name is the hook's name within the event, and the second segment.
	Name string

	Matcher         string
	Type            string
	Command         string
	Prompt          string
	Timeout         int
	Async           bool
	PreToolFallback bool
}

func (Hook) Kind() trust.ItemKind      { return trust.KindHook }
func (Hook) TrustKind() trust.ItemKind { return trust.KindHook }

// Ref name is "<event>/<name>".
func (h Hook) refName() string { return h.Event + "/" + h.Name }

// hookContent is the hook's behavioural configuration — the whole of a hook's
// authored data. It deliberately carries NO name: the filename is the identity,
// and keeping the name out of the content file keeps it out of any payload a later
// layer builds from those bytes, which is what lets existing hook approvals and
// content-rejections survive the identity change with no exec-preimage contract
// bump.
type hookContent struct {
	Matcher         string `yaml:"matcher,omitempty"`
	Type            string `yaml:"type,omitempty"`
	Command         string `yaml:"command,omitempty"`
	Prompt          string `yaml:"prompt,omitempty"`
	Timeout         int    `yaml:"timeout,omitempty"`
	Async           bool   `yaml:"async,omitempty"`
	PreToolFallback bool   `yaml:"pre_tool_fallback,omitempty"`
}

type hookType struct{}

func (hookType) Name() string { return trust.KindHook.Dir() }
func (hookType) Dir() string  { return trust.KindHook.Dir() }

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
	name, err := readExecItem(t.Dir(), src, 1, &content, nil)
	if err != nil {
		return nil, err
	}
	event, hookName, _ := strings.Cut(name, "/")
	return Hook{
		Event:           event,
		Name:            hookName,
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
	return encodeExecItem(t.Dir(), h.refName(),
		hookContent{
			Matcher:         h.Matcher,
			Type:            h.Type,
			Command:         h.Command,
			Prompt:          h.Prompt,
			Timeout:         h.Timeout,
			Async:           h.Async,
			PreToolFallback: h.PreToolFallback,
		},
		nil)
}
