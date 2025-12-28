// git is a generator that provides git repository context.
//
// This generator is primarily exemplary, demonstrating how to implement
// dynamic context generation. In practice, most AI tools already have
// native git integration that provides richer functionality.
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/benjaminabbitt/scm/internal/gitutil"
	"github.com/benjaminabbitt/scm/internal/markdown"
)

func main() {
	workDir := "."
	if len(os.Args) > 1 {
		workDir = os.Args[1]
	}

	// Check if we're in a git repo
	if !gitutil.IsRepo(workDir) {
		fmt.Fprintln(os.Stderr, "not in a git repository")
		os.Exit(1)
	}

	repo, err := gitutil.Open(workDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to open repository: %v\n", err)
		os.Exit(1)
	}

	frag := markdown.NewFragment()
	ctx := frag.Context()
	ctx.H1("Git Context")

	// Get branch
	branch, err := repo.Branch()
	if err == nil {
		ctx.P(fmt.Sprintf("**Branch:** `%s`", branch))
		frag.SetVar("branch", branch)
	}

	// Get remote URL
	if remoteURL := repo.RemoteURL("origin"); remoteURL != "" {
		ctx.P(fmt.Sprintf("**Remote:** `%s`", remoteURL))
		frag.SetVar("remote", remoteURL)
	}

	// Get status
	status, err := repo.StatusShort()
	if err == nil && status != "" {
		ctx.P("**Status:**")
		ctx.CodeBlock("", status)
		frag.SetVar("has_changes", "true")
	} else {
		frag.SetVar("has_changes", "false")
	}

	// Get recent commits
	commits, err := repo.RecentCommits(5)
	if err == nil && len(commits) > 0 {
		var commitLines []string
		for _, c := range commits {
			commitLines = append(commitLines, fmt.Sprintf("- `%s` %s", c.Hash, c.Message))
		}
		ctx.P("**Recent commits:**")
		ctx.P(strings.Join(commitLines, "\n"))
	}

	fmt.Print(frag.String())
}
