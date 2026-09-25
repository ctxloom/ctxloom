package cli

import (
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
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
	// SelfSigned: ctxloom's own loadout, whose signature verified but is
	// circular — Signed without a Signer, and deliberately not "unsigned".
	SelfSigned bool `json:"self_signed,omitempty"`
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
		Signed:          info.Signer != "" || info.SelfSigned,
		Signer:          info.Signer,
		SelfSigned:      info.SelfSigned,
	}
}

// newBundleListRows projects the loader's listing in its own order.
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
	SelfSigned   bool                          `json:"self_signed,omitempty"`
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
		Signed:       b.Signer() != "" || b.SelfSigned(),
		Signer:       b.Signer(),
		SelfSigned:   b.SelfSigned(),
		Fragments:    showFragments(b.Fragments),
		Commands:     showCommands(b.Commands),
		MCP:          showMCP(b.MCP),
		Skills:       showSkills(b.Skills),
	}
	if len(b.Profiles) > 0 {
		v.Profiles = b.ProfileNames()
	}
	for _, e := range b.Hooks.Entries() {
		v.Hooks = append(v.Hooks, e.ID())
	}
	return v
}

// showFragments is the fragments' show rows; nil when there are none.
func showFragments(frags map[string]bundles.BundleFragment) map[string]bundleShowFragment {
	if len(frags) == 0 {
		return nil
	}
	out := make(map[string]bundleShowFragment, len(frags))
	for name, f := range frags {
		out[name] = bundleShowFragment{
			Tags:      f.Tags,
			Preview:   itemPreview(f.Content),
			Distilled: f.Distilled != "",
			NoDistill: f.NoDistill,
			Premise:   f.Premise,
		}
	}
	return out
}

// showCommands is the commands' show rows; nil when there are none.
func showCommands(cmds map[string]bundles.BundleCommand) map[string]bundleShowCommand {
	if len(cmds) == 0 {
		return nil
	}
	out := make(map[string]bundleShowCommand, len(cmds))
	for name, c := range cmds {
		out[name] = bundleShowCommand{
			Tags:        c.Tags,
			Description: c.Description,
			Preview:     itemPreview(c.Content),
			Distilled:   c.Distilled != "",
			NoDistill:   c.NoDistill,
		}
	}
	return out
}

// showMCP is the MCP servers' show rows; nil when there are none.
func showMCP(servers map[string]bundles.BundleMCP) map[string]bundleShowMCP {
	if len(servers) == 0 {
		return nil
	}
	out := make(map[string]bundleShowMCP, len(servers))
	for name, m := range servers {
		out[name] = bundleShowMCP{
			Command:      m.Command,
			Args:         m.Args,
			Env:          m.Env,
			Notes:        m.Notes,
			Installation: m.Installation,
		}
	}
	return out
}

// showSkills is the skills' show rows; nil when there are none.
func showSkills(skills map[string]bundles.BundleSkill) map[string]bundleShowSkill {
	if len(skills) == 0 {
		return nil
	}
	out := make(map[string]bundleShowSkill, len(skills))
	for name, sk := range skills {
		out[name] = bundleShowSkill{Path: sk.Path, Tags: sk.Tags, Notes: sk.Notes}
	}
	return out
}
