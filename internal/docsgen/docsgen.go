// Package docsgen generates a product's reference documentation from its
// sources of truth: the CLI reference (man pages + Starlight markdown) from a
// cobra command tree, and the MCP reference from a live MCP server's
// registrations. It is generic over the product — ctxloom, taskloom, and ltk
// each describe themselves with a Product and share this one generator, so a
// cobra Short/Long edit or a new tool can never drift from the published docs.
//
// Output is deterministic (cobra auto-gen timestamps disabled, tools/resources
// sorted by name) so CI can gate on drift via `just gen-docs-check`.
package docsgen

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
	"github.com/spf13/cobra/doc"
)

// Product is one documented binary: its cobra tree, its optional MCP surface,
// and the site/man metadata needed to render them. The fields that appear in
// generated prose (CLISource, MCPSource, MCPIntro) name the code that produced
// the page, so a reader always knows which source of truth to edit.
type Product struct {
	// Bin is the binary name ("ctxloom"). It prefixes every generated file, so
	// products can share the man directory without clobbering each other.
	Bin string
	// Root is the cobra root command. It is the single source of truth for the
	// CLI reference.
	Root *cobra.Command
	// CLISource is the package the tree is defined in ("internal/cli"), cited in
	// the generated-file banner.
	CLISource string
	// LinkBase is the site route prefix for this product's CLI pages
	// ("/reference/cli/"), used to rewrite cobra's inter-page .md links.
	LinkBase string
	// ManTitle and ManManual fill the man page header ("CTXLOOM", "User Commands").
	ManTitle  string
	ManManual string
	// Unhide names top-level commands that are hidden from --help as advanced
	// but must still be documented.
	Unhide []string
	// ConfigSchema is the default path to the product's config JSON Schema, the
	// source of truth for its configuration reference. Empty for products with
	// no configuration file (ltk).
	ConfigSchema string

	// MCPServer is the product's documentation-time MCP server, with the full
	// tool/resource surface registered but no live config. Nil for products with
	// no MCP surface (ltk).
	MCPServer *mcp.Server
	// MCPSource is the code the registrations live in, cited in the banner.
	MCPSource string
	// MCPCommand is the command that serves the registered surface to an agent
	// ("ctxloom run", "taskloom mcp") — the one a reader can actually reach,
	// which is not always the command literally named "mcp serve".
	MCPCommand string
	// MCPIntro is the page's opening prose. It comes from code (e.g. the
	// server's instructions constant), never hand-copied into the page.
	MCPIntro string
}

// envPrefix reconstructs this product's confload.Product.EnvPrefix
// (internal/shared/confload) purely from p.Bin, for GenConfig's override-chain
// prose: every product in this family follows the SAME convention — its
// upper-cased binary name plus "_CONFIG_" ("ctxloom" -> "CTXLOOM_CONFIG_",
// "taskloom" -> "TASKLOOM_CONFIG_") — so docsgen doesn't need its own copy of
// each product's confload.Product to state it.
func (p *Product) envPrefix() string {
	return strings.ToUpper(p.Bin) + "_CONFIG_"
}

// PrepareTree readies the command tree for documentation generation: every
// command drops cobra's auto-gen timestamp (the CI drift check depends on
// byte-identical output), and the product's Unhide commands are unhidden so
// their pages still generate.
func (p *Product) PrepareTree() {
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		c.DisableAutoGenTag = true
		if c.Parent() == p.Root && slices.Contains(p.Unhide, c.Name()) {
			c.Hidden = false
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(p.Root)
}

// GenMan writes the product's man pages into dir. Products share man/man1, so
// only this product's own pages are swept.
func GenMan(p *Product, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := removeGenerated(dir, p.Bin+"*.1"); err != nil {
		return err
	}
	header := &doc.GenManHeader{
		Title:   p.ManTitle,
		Section: "1",
		Source:  p.Bin,
		Manual:  p.ManManual,
	}
	return doc.GenManTree(p.Root, header, dir)
}

// GenMarkdown writes the product's per-command Starlight pages into dir.
func GenMarkdown(p *Product, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := removeGenerated(dir, p.Bin+"_*.md"); err != nil {
		return err
	}
	return doc.GenMarkdownTreeCustom(p.Root, dir, p.filePrepender, p.linkHandler)
}

// removeGenerated deletes previously generated files matching pattern inside
// dir, so pages for renamed or removed commands don't linger (cobra's tree
// generators only ever add). The patterns are product-prefixed and never match
// the hand-written index.md.
func removeGenerated(dir, pattern string) error {
	stale, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil {
		return err
	}
	for _, f := range stale {
		if err := os.Remove(f); err != nil {
			return err
		}
	}
	return nil
}

// filePrepender emits Starlight frontmatter plus a generated-file banner for
// each per-command page. The title is the command path, recovered from the
// filename (ctxloom_profile_create.md -> "ctxloom profile create"). Only the
// generated <bin>_*.md files pass through here; a hand-written index.md is
// never touched.
func (p *Product) filePrepender(filename string) string {
	base := strings.TrimSuffix(filepath.Base(filename), filepath.Ext(filename))
	command := strings.ReplaceAll(base, "_", " ")
	return fmt.Sprintf(`---
title: "%[1]s"
---
<!-- GENERATED by %[2]sjust gen-docs%[2]s from the command definitions in %[3]s.
     Do not edit; edit the cobra Short/Long/Example fields instead. -->
:::note
This page is generated from %[2]s%[1]s --help%[2]s.
:::

`, command, "`", p.CLISource)
}

// linkHandler rewrites cobra's inter-page .md links to the site's routes:
// ctxloom_profile_create.md -> /reference/cli/ctxloom_profile_create/.
func (p *Product) linkHandler(name string) string {
	return p.LinkBase + strings.TrimSuffix(name, filepath.Ext(name)) + "/"
}
