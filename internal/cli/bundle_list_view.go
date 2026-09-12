package cli

import (
	"strings"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/shared/textutil"
)

// bundleListRow is the structured contract of one `bundle list` entry — what
// `--format json` (and yaml/toml/markdown) publishes. Deliberately a separate
// type from bundles.BundleInfo, the same posture as SessionRow: the loader's
// type is free to grow fields for its own callers without every addition
// silently becoming part of the CLI's wire shape, and the wire shape is
// pinned here by its json tags and by TestBundleListRow_JSONShape.
//
// The counts and the state flags are never omitted: a consumer asking "is
// this held?" must read false, not a missing key. Signed is derived from
// Signer so "unsigned" is a positive fact rather than an absent one.
type bundleListRow struct {
	Name          string   `json:"name"`
	Ref           string   `json:"ref"`
	Path          string   `json:"path,omitempty"`
	Version       string   `json:"version,omitempty"`
	Description   string   `json:"description,omitempty"`
	Tags          []string `json:"tags,omitempty"`
	FragmentCount int      `json:"fragment_count"`
	CommandCount  int      `json:"command_count"`
	MCPCount      int      `json:"mcp_count"`
	ProfileCount  int      `json:"profile_count"`
	// Deleted/Held/Retracted mirror the like-named bundles.BundleInfo flags;
	// see those for what each state means and why the listing carries it.
	Deleted         bool   `json:"deleted"`
	Held            bool   `json:"held"`
	Retracted       bool   `json:"retracted"`
	RetractedReason string `json:"retracted_reason,omitempty"`
	Signed          bool   `json:"signed"`
	Signer          string `json:"signer,omitempty"`
}

func newBundleListRow(info *bundles.BundleInfo) bundleListRow {
	return bundleListRow{
		Name:            info.Name,
		Ref:             string(info.Ref),
		Path:            info.Path,
		Version:         info.Version,
		Description:     info.Description,
		Tags:            info.Tags,
		FragmentCount:   info.FragmentCount,
		CommandCount:    info.CommandCount,
		MCPCount:        info.MCPCount,
		ProfileCount:    info.ProfileCount,
		Deleted:         info.Deleted,
		Held:            info.Held,
		Retracted:       info.Retracted,
		RetractedReason: info.RetractedReason,
		Signed:          info.Signer != "",
		Signer:          info.Signer,
	}
}

// newBundleListRows projects the loader's listing in its own order. The
// result is never nil: an empty listing encodes as [] rather than null, which
// is what a consumer checking "$ is empty" can actually read.
func newBundleListRows(infos []*bundles.BundleInfo) []bundleListRow {
	rows := make([]bundleListRow, 0, len(infos))
	for _, info := range infos {
		rows = append(rows, newBundleListRow(info))
	}
	return rows
}

// bundleShowView is the structured contract of `bundle show`: the header,
// the trust state, and one section per item kind keyed by item name. It
// describes the container's STRUCTURE and stops there — an item's body is
// `bundle view`'s to hand back — so items carry a first-line preview, never
// their content, distilled rendering or content hash. Profiles and hooks are
// listed by identity only: a profile's definition is `profile show`'s, and a
// hook's "<event>/<index>" id is the handle `ctxloom review` takes back.
type bundleShowView struct {
	Name         string                        `json:"name"`
	Path         string                        `json:"path,omitempty"`
	Version      string                        `json:"version,omitempty"`
	Author       string                        `json:"author,omitempty"`
	Description  string                        `json:"description,omitempty"`
	Tags         []string                      `json:"tags,omitempty"`
	Notes        string                        `json:"notes,omitempty"`
	Installation string                        `json:"installation,omitempty"`
	Signed       bool                          `json:"signed"`
	Signer       string                        `json:"signer,omitempty"`
	Fragments    map[string]bundleShowFragment `json:"fragments,omitempty"`
	Commands     map[string]bundleShowCommand  `json:"commands,omitempty"`
	MCP          map[string]bundleShowMCP      `json:"mcp,omitempty"`
	Skills       map[string]bundleShowSkill    `json:"skills,omitempty"`
	Profiles     []string                      `json:"profiles,omitempty"`
	Hooks        []string                      `json:"hooks,omitempty"`
}

type bundleShowFragment struct {
	Tags      []string `json:"tags,omitempty"`
	Preview   string   `json:"preview"`
	Distilled bool     `json:"distilled"`
	NoDistill bool     `json:"no_distill"`
	Premise   string   `json:"premise,omitempty"`
}

type bundleShowCommand struct {
	Tags        []string `json:"tags,omitempty"`
	Description string   `json:"description,omitempty"`
	Preview     string   `json:"preview"`
	Distilled   bool     `json:"distilled"`
	NoDistill   bool     `json:"no_distill"`
}

type bundleShowMCP struct {
	Command      string            `json:"command"`
	Args         []string          `json:"args,omitempty"`
	Env          map[string]string `json:"env,omitempty"`
	Notes        string            `json:"notes,omitempty"`
	Installation string            `json:"installation,omitempty"`
}

type bundleShowSkill struct {
	Path  string   `json:"path,omitempty"`
	Tags  []string `json:"tags,omitempty"`
	Notes string   `json:"notes,omitempty"`
}

// itemPreview is the one-line preview `show` gives an item in every format:
// the first non-blank line, trimmed, capped at 70 bytes with the ellipsis
// reserved by Ellipsize. The text renderer prints exactly this.
func itemPreview(content string) string {
	first, _, _ := strings.Cut(strings.TrimSpace(content), "\n")
	return textutil.Ellipsize(strings.TrimSpace(first), 70)
}

func newBundleShowView(b *bundles.Bundle) bundleShowView {
	v := bundleShowView{
		Name:         b.Name,
		Path:         b.Path,
		Version:      b.Version,
		Author:       b.Author,
		Description:  b.Description,
		Tags:         b.Tags,
		Notes:        b.Notes,
		Installation: b.Installation,
		Signed:       b.Signer() != "",
		Signer:       b.Signer(),
	}
	if len(b.Fragments) > 0 {
		v.Fragments = make(map[string]bundleShowFragment, len(b.Fragments))
		for name, f := range b.Fragments {
			v.Fragments[name] = bundleShowFragment{
				Tags:      f.Tags,
				Preview:   itemPreview(f.Content),
				Distilled: f.Distilled != "",
				NoDistill: f.NoDistill,
				Premise:   f.Premise,
			}
		}
	}
	if len(b.Commands) > 0 {
		v.Commands = make(map[string]bundleShowCommand, len(b.Commands))
		for name, c := range b.Commands {
			v.Commands[name] = bundleShowCommand{
				Tags:        c.Tags,
				Description: c.Description,
				Preview:     itemPreview(c.Content),
				Distilled:   c.Distilled != "",
				NoDistill:   c.NoDistill,
			}
		}
	}
	if len(b.MCP) > 0 {
		v.MCP = make(map[string]bundleShowMCP, len(b.MCP))
		for name, m := range b.MCP {
			v.MCP[name] = bundleShowMCP{
				Command:      m.Command,
				Args:         m.Args,
				Env:          m.Env,
				Notes:        m.Notes,
				Installation: m.Installation,
			}
		}
	}
	if len(b.Skills) > 0 {
		v.Skills = make(map[string]bundleShowSkill, len(b.Skills))
		for name, s := range b.Skills {
			v.Skills[name] = bundleShowSkill{Path: s.Path, Tags: s.Tags, Notes: s.Notes}
		}
	}
	if len(b.Profiles) > 0 {
		v.Profiles = b.ProfileNames()
	}
	for _, e := range b.Hooks.Entries() {
		v.Hooks = append(v.Hooks, e.ID())
	}
	return v
}
