// Command genman generates man pages for ctxloom CLI.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra/doc"

	"github.com/ctxloom/ctxloom/cmd"
)

func main() {
	outDir := "man/man1"
	if len(os.Args) > 1 {
		outDir = os.Args[1]
	}

	if err := os.MkdirAll(outDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating output directory: %v\n", err)
		os.Exit(1)
	}

	header := &doc.GenManHeader{
		Title:   "CTXLOOM",
		Section: "1",
		Source:  "ctxloom",
		Manual:  "User Commands",
	}

	rootCmd := cmd.GetRootCmd()
	if err := doc.GenManTree(rootCmd, header, outDir); err != nil {
		fmt.Fprintf(os.Stderr, "Error generating man pages: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Man pages generated in %s\n", outDir)
}
