// Package coordgrpc is the wire codec's home: the one place a resolved
// launch is projected onto the coordination proto's Launch message and read
// back off it. Both ends are typed — launch.Launch on one side, pb.Launch on
// the other — and WireFieldNames pins that they carry the same field set.
package coordgrpc

import (
	"errors"
	"fmt"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/structpb"

	pb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/core/trust"
)

// MaxRecvMsgSize is the frame ceiling both ends of the runner channel are
// configured with: the largest package that may ride inline plus one MiB of
// headroom for the rest of the Launch (the plan, the exports, the index,
// the cell). A package above the inline ceiling rides as a claim, so the
// frame is bounded without bounding the package.
const MaxRecvMsgSize = composite.DefaultInlineMax + (1 << 20)

// ErrNoLaunch is DecodeLaunch's refusal of a StartRun that carries no
// launch: a zero Launch would deliver nothing and drive nothing, so it is
// never produced.
var ErrNoLaunch = errors.New("coordgrpc: the StartRun carries no launch")

// wireFields maps each launch.Launch field to the wire field that carries
// it. Every entry is a Go field name; WireFieldNames reads the proto side
// live, so a Go field added without a wire field (or the reverse) fails the
// parity test rather than silently dropping on the wire.
var wireFields = map[string]protoreflect.Name{
	"Identity":   "identity",
	"Engine":     "engine",
	"Label":      "label",
	"Mode":       "mode",
	"Permission": "permission",
	"Declared":   "declared",
	"Axes":       "axes",
	"Cell":       "cell",
	"Home":       "home",
	"Package":    "package",
	"Exports":    "exports",
	"Plan":       "plan",
	"Index":      "index",
	"MCP":        "mcp",
	"Prompt":     "prompt",
	"Resume":     "resume",
	"Env":        "env",
}

// WireFieldNames is the set of launch.Launch field names the proto Launch
// carries, read off the message descriptor: a wire field with no Go name in
// wireFields, or a Go name whose wire field is absent, is reported as its
// own name so the parity diff names the drift.
func WireFieldNames() []string {
	fields := (&pb.Launch{}).ProtoReflect().Descriptor().Fields()
	byWire := make(map[protoreflect.Name]string, len(wireFields))
	for goName, wire := range wireFields {
		byWire[wire] = goName
	}
	names := make([]string, 0, fields.Len())
	for i := 0; i < fields.Len(); i++ {
		wire := fields.Get(i).Name()
		goName, ok := byWire[wire]
		if !ok {
			goName = "wire:" + string(wire)
		}
		names = append(names, goName)
	}
	return names
}

// EncodeLaunch projects a resolved launch onto the wire message. Every field
// crosses typed; the cell's process-local handles (Cleanup, Handle) stay
// with the originator, and the carrier's shape (inline or claim) is the
// oneof.
func EncodeLaunch(l launch.Launch) *pb.Launch {
	return &pb.Launch{
		Identity:   encodeIdentity(l.Identity),
		Engine:     string(l.Engine),
		Label:      encodeLabel(l.Label),
		Mode:       encodeMode(l.Mode),
		Permission: pb.PermissionMode(l.Permission),
		Declared:   &pb.Axes{Workspace: string(l.Declared.Workspace), Runtime: string(l.Declared.Runtime)},
		Axes:       &pb.Axes{Workspace: string(l.Axes.Workspace), Runtime: string(l.Axes.Runtime)},
		Cell:       encodeCell(l.Cell),
		Home:       encodeHome(l.Home),
		Package:    encodeCarrier(l.Package),
		Exports:    encodeExports(l.Exports),
		Plan:       encodePlan(l.Plan),
		Index:      encodeIndex(l.Index),
		Mcp:        &pb.Endpoint{Url: l.MCP.URL, Credential: l.MCP.Credential},
		Prompt:     l.Prompt,
		Resume:     &pb.ResumeRef{Harp: l.Resume.Harp, NativeKey: l.Resume.NativeKey},
		Env:        l.Env,
	}
}

// DecodeLaunch reads the wire message back into the typed launch. It is the
// only constructor of a launch.Launch outside launch.Resolve, and it
// constructs exactly what EncodeLaunch wrote.
func DecodeLaunch(w *pb.Launch) (launch.Launch, error) {
	if w == nil {
		return launch.Launch{}, ErrNoLaunch
	}
	label, err := decodeLabel(w.GetLabel())
	if err != nil {
		return launch.Launch{}, err
	}
	carrier, err := decodeCarrier(w.GetPackage())
	if err != nil {
		return launch.Launch{}, err
	}
	workspace, err := launch.ParseWorkspaceAxis(w.GetAxes().GetWorkspace())
	if err != nil {
		return launch.Launch{}, fmt.Errorf("coordgrpc: launch axes: %w", err)
	}
	runtime, err := launch.ParseRuntimeAxis(w.GetAxes().GetRuntime())
	if err != nil {
		return launch.Launch{}, fmt.Errorf("coordgrpc: launch axes: %w", err)
	}
	declared, err := decodeDeclaredAxes(w.GetDeclared())
	if err != nil {
		return launch.Launch{}, err
	}
	cell, err := decodeCell(w.GetCell())
	if err != nil {
		return launch.Launch{}, err
	}
	index, err := decodeIndex(w.GetIndex())
	if err != nil {
		return launch.Launch{}, err
	}
	l := launch.Launch{
		Identity:   decodeIdentity(w.GetIdentity()),
		Engine:     engine.Name(w.GetEngine()),
		Label:      label,
		Mode:       decodeMode(w.GetMode()),
		Permission: engine.PermissionMode(w.GetPermission()),
		Declared:   declared,
		Axes:       launch.Axes{Workspace: workspace, Runtime: runtime},
		Cell:       cell,
		Home:       decodeHome(w.GetHome()),
		Package:    carrier,
		Exports:    decodeExports(w.GetExports()),
		Plan:       decodePlan(w.GetPlan()),
		Index:      index,
		MCP:        sessions.Endpoint{URL: w.GetMcp().GetUrl(), Credential: w.GetMcp().GetCredential()},
		Prompt:     w.GetPrompt(),
		Resume:     sessions.ResumeRef{Harp: w.GetResume().GetHarp(), NativeKey: w.GetResume().GetNativeKey()},
		Env:        w.GetEnv(),
	}
	if err := l.Identity.Validate(); err != nil {
		return launch.Launch{}, fmt.Errorf("coordgrpc: launch identity: %w", err)
	}
	return l, nil
}

func encodeIdentity(id sessions.Identity) *pb.Identity {
	return &pb.Identity{Harp: id.Harp, RunId: id.RunID, Depth: int32(id.Depth), OneShot: id.OneShot, Project: id.Project, Leaf: id.Leaf}
}

func decodeIdentity(w *pb.Identity) sessions.Identity {
	return sessions.Identity{Harp: w.GetHarp(), RunID: w.GetRunId(), Depth: int(w.GetDepth()), OneShot: w.GetOneShot(), Project: w.GetProject(), Leaf: w.GetLeaf()}
}

func encodeLabel(l engine.LabelConfig) *pb.LabelConfig {
	out := &pb.LabelConfig{Label: l.Label, Model: l.Model, Binary: l.Binary, Args: l.Args}
	if l.Body != nil {
		// The body is the entry as written; every YAML scalar and
		// collection is a Struct value, so a body that cannot be one is a
		// body the config reader would have refused already.
		if body, err := structpb.NewStruct(l.Body); err == nil {
			out.Body = body
		}
	}
	return out
}

func decodeLabel(w *pb.LabelConfig) (engine.LabelConfig, error) {
	out := engine.LabelConfig{Label: w.GetLabel(), Model: w.GetModel(), Binary: w.GetBinary(), Args: w.GetArgs()}
	if w.GetBody() != nil {
		out.Body = w.GetBody().AsMap()
	}
	return out, nil
}

func encodeMode(m engine.Mode) pb.Mode {
	if m == engine.Structured {
		return pb.Mode_MODE_STRUCTURED
	}
	return pb.Mode_MODE_INTERACTIVE
}

func decodeMode(m pb.Mode) engine.Mode {
	if m == pb.Mode_MODE_STRUCTURED {
		return engine.Structured
	}
	return engine.Interactive
}

func encodeCell(c launch.Cell) *pb.Cell {
	out := &pb.Cell{
		Paths:     encodePaths(c.Paths.Paths()),
		Mounts:    encodeMounts(c.Paths.Mounts()),
		Workspace: c.Workspace,
		Env:       c.Env,
		Home:      encodeHome(c.Home),
	}
	if c.Container != nil {
		out.Container = &pb.ContainerCell{Runtime: string(c.Container.Runtime), Image: c.Container.Image, Mounts: encodeMounts(c.Container.Mounts), Home: c.Container.Home}
	}
	return out
}

func decodeCell(w *pb.Cell) (launch.Cell, error) {
	out := launch.Cell{
		Paths:     present.Advised(decodePaths(w.GetPaths()), decodeMounts(w.GetMounts())),
		Workspace: w.GetWorkspace(),
		Env:       w.GetEnv(),
		Home:      decodeHome(w.GetHome()),
	}
	if c := w.GetContainer(); c != nil {
		runtime, err := launch.ParseRuntimeAxis(c.GetRuntime())
		if err != nil {
			return launch.Cell{}, fmt.Errorf("coordgrpc: container cell: %w", err)
		}
		out.Container = &launch.ContainerCell{Runtime: runtime, Image: c.GetImage(), Mounts: decodeMounts(c.GetMounts()), Home: c.GetHome()}
	}
	return out, nil
}

func encodePaths(p present.Paths) *pb.Paths {
	root := func(r present.Root) *pb.Root { return &pb.Root{Host: r.Host, Engine: r.Engine} }
	return &pb.Paths{ProjectRoot: root(p.ProjectRoot), EngineHome: root(p.EngineHome), CtxloomHome: root(p.CtxloomHome), Scratch: root(p.Scratch)}
}

func decodePaths(w *pb.Paths) present.Paths {
	root := func(r *pb.Root) present.Root { return present.Root{Host: r.GetHost(), Engine: r.GetEngine()} }
	return present.Paths{ProjectRoot: root(w.GetProjectRoot()), EngineHome: root(w.GetEngineHome()), CtxloomHome: root(w.GetCtxloomHome()), Scratch: root(w.GetScratch())}
}

func encodeMounts(ms []present.Mount) []*pb.Mount {
	if ms == nil {
		return nil
	}
	out := make([]*pb.Mount, 0, len(ms))
	for _, m := range ms {
		out = append(out, &pb.Mount{HostDir: m.HostDir, TargetDir: m.TargetDir})
	}
	return out
}

func decodeMounts(ws []*pb.Mount) []present.Mount {
	if ws == nil {
		return nil
	}
	out := make([]present.Mount, 0, len(ws))
	for _, m := range ws {
		out = append(out, present.Mount{HostDir: m.GetHostDir(), TargetDir: m.GetTargetDir()})
	}
	return out
}

func encodeHome(hs []engine.HomeBinding) []*pb.HomeBinding {
	if hs == nil {
		return nil
	}
	out := make([]*pb.HomeBinding, 0, len(hs))
	for _, h := range hs {
		out = append(out, &pb.HomeBinding{Var: h.Var, Path: h.Path})
	}
	return out
}

func decodeHome(ws []*pb.HomeBinding) []engine.HomeBinding {
	if ws == nil {
		return nil
	}
	out := make([]engine.HomeBinding, 0, len(ws))
	for _, h := range ws {
		out = append(out, engine.HomeBinding{Var: h.GetVar(), Path: h.GetPath()})
	}
	return out
}

func encodeCarrier(c composite.Carrier) *pb.Carrier {
	out := &pb.Carrier{Digest: c.Digest[:]}
	if c.Claim != nil {
		out.Form = &pb.Carrier_Claim{Claim: &pb.Claim{Location: c.Claim.Location, Size: c.Claim.Size}}
	} else {
		out.Form = &pb.Carrier_Inline{Inline: c.Inline}
	}
	return out
}

func decodeCarrier(w *pb.Carrier) (composite.Carrier, error) {
	var out composite.Carrier
	if len(w.GetDigest()) != len(out.Digest) {
		return composite.Carrier{}, fmt.Errorf("coordgrpc: the carrier's digest is %d bytes, not %d", len(w.GetDigest()), len(out.Digest))
	}
	copy(out.Digest[:], w.GetDigest())
	switch f := w.GetForm().(type) {
	case *pb.Carrier_Inline:
		out.Inline = f.Inline
	case *pb.Carrier_Claim:
		out.Claim = &composite.Claim{Location: f.Claim.GetLocation(), Size: f.Claim.GetSize()}
	default:
		return composite.Carrier{}, errors.New("coordgrpc: the carrier is neither inline nor a claim")
	}
	return out, nil
}

func encodeExports(e engine.Exports) *pb.Exports {
	out := &pb.Exports{HookEvent: e.HookEvent, DenyTools: e.DenyTools}
	for _, c := range e.Commands {
		out.Commands = append(out.Commands, &pb.CommandExport{Name: c.Name, Body: c.Body, Enabled: c.Enabled, Description: c.Description, ArgumentHint: c.ArgumentHint, AllowedTools: c.AllowedTools, Model: c.Model})
	}
	for _, s := range e.Skills {
		files := make([]*pb.SkillFile, 0, len(s.Files))
		for _, f := range s.Files {
			files = append(files, &pb.SkillFile{Path: f.Path, Digest: f.Digest, Size: f.Size, Mode: f.Mode, Bytes: f.Bytes})
		}
		out.Skills = append(out.Skills, &pb.SkillExport{Name: s.Name, Description: s.Description, Files: files, Enabled: s.Enabled})
	}
	return out
}

func decodeExports(w *pb.Exports) engine.Exports {
	out := engine.Exports{HookEvent: w.GetHookEvent(), DenyTools: w.GetDenyTools()}
	for _, c := range w.GetCommands() {
		out.Commands = append(out.Commands, engine.CommandExport{Name: c.GetName(), Body: c.GetBody(), Enabled: c.GetEnabled(), Description: c.GetDescription(), ArgumentHint: c.GetArgumentHint(), AllowedTools: c.GetAllowedTools(), Model: c.GetModel()})
	}
	for _, s := range w.GetSkills() {
		var files []engine.SkillFile
		for _, f := range s.GetFiles() {
			files = append(files, engine.SkillFile{Path: f.GetPath(), Digest: f.GetDigest(), Size: f.GetSize(), Mode: f.GetMode(), Bytes: f.GetBytes()})
		}
		out.Skills = append(out.Skills, engine.SkillExport{Name: s.GetName(), Description: s.GetDescription(), Files: files, Enabled: s.GetEnabled()})
	}
	return out
}

func encodePlan(p delivery.Plan) *pb.Plan {
	out := &pb.Plan{Dynamic: p.Dynamic}
	for _, s := range p.Static {
		out.Static = append(out.Static, &pb.StaticItem{Kind: encodeKind(s.Kind), Approach: s.Approach, Root: pb.RootKind(s.Root), Traits: encodeTraits(s.Traits)})
	}
	for _, l := range p.Losses {
		out.Losses = append(out.Losses, &pb.Loss{Kind: encodeKind(l.Kind)})
	}
	return out
}

func decodePlan(w *pb.Plan) delivery.Plan {
	// A plan exists even when empty (delivery.Route's shape).
	out := delivery.Plan{Static: []delivery.StaticItem{}, Dynamic: w.GetDynamic()}
	for _, s := range w.GetStatic() {
		out.Static = append(out.Static, delivery.StaticItem{Kind: decodeKind(s.GetKind()), Approach: s.GetApproach(), Root: present.RootKind(s.GetRoot()), Traits: decodeTraits(s.GetTraits())})
	}
	for _, l := range w.GetLosses() {
		out.Losses = append(out.Losses, delivery.Loss{Kind: decodeKind(l.GetKind())})
	}
	return out
}

func encodeTraits(t present.Traits) *pb.Traits {
	out := &pb.Traits{Channel: pb.Channel(t.Channel), LaunchOnly: t.LaunchOnly, Persists: t.Persists}
	for _, r := range t.Roots {
		out.Roots = append(out.Roots, pb.RootKind(r))
	}
	return out
}

func decodeTraits(w *pb.Traits) present.Traits {
	out := present.Traits{Channel: present.Channel(w.GetChannel()), LaunchOnly: w.GetLaunchOnly(), Persists: w.GetPersists()}
	for _, r := range w.GetRoots() {
		out.Roots = append(out.Roots, present.RootKind(r))
	}
	return out
}

// encodeKind offsets present.Kind by one: Context is the Go zero value, and
// the wire keeps zero for "unspecified".
func encodeKind(k present.Kind) pb.SurfaceKind { return pb.SurfaceKind(int32(k) + 1) }

func decodeKind(k pb.SurfaceKind) present.Kind { return present.Kind(int32(k) - 1) }

func encodeIndex(i composite.Index) *pb.Index {
	out := &pb.Index{}
	for _, e := range i.Entries {
		out.Entries = append(out.Entries, &pb.IndexEntry{Ref: e.Ref, Kind: string(e.Kind), Description: e.Description, Premise: e.Premise})
	}
	return out
}

func decodeIndex(w *pb.Index) (composite.Index, error) {
	var out composite.Index
	for _, e := range w.GetEntries() {
		kind, ok := trust.ParseItemKind(e.GetKind())
		if !ok {
			return composite.Index{}, fmt.Errorf("coordgrpc: index entry %q names no item kind: %q", e.GetRef(), e.GetKind())
		}
		out.Entries = append(out.Entries, composite.IndexEntry{Ref: e.GetRef(), Kind: kind, Description: e.GetDescription(), Premise: e.GetPremise()})
	}
	return out, nil
}

// decodeDeclaredAxes reads the axes as ASKED: an empty axis is a declaration
// nobody made and stays empty, unlike the settled pair, which every launch
// carries in full.
func decodeDeclaredAxes(a *pb.Axes) (launch.Axes, error) {
	var out launch.Axes
	if ws := a.GetWorkspace(); ws != "" {
		parsed, err := launch.ParseWorkspaceAxis(ws)
		if err != nil {
			return launch.Axes{}, fmt.Errorf("coordgrpc: launch declared axes: %w", err)
		}
		out.Workspace = parsed
	}
	if rt := a.GetRuntime(); rt != "" {
		parsed, err := launch.ParseRuntimeAxis(rt)
		if err != nil {
			return launch.Axes{}, fmt.Errorf("coordgrpc: launch declared axes: %w", err)
		}
		out.Runtime = parsed
	}
	return out, nil
}
