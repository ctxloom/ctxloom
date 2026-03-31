package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var metaCmd = &cobra.Command{
	Use:   "meta",
	Short: "Output metadata for session tracking",
}

// Stamp is the structured output for session markers.
// Uses the SCM wrapper PID (ctxloom run/plugin/init) which is stable across /clear,
// unlike the Claude Code PID which may change.
type Stamp struct {
	PID  int    `json:"pid"`
	Time string `json:"time"`
}


// findSCMWrapperPID walks up the process tree to find the SCM wrapper process.
// Returns the PID of the first ancestor that is an SCM command (run, plugin, init).
// Falls back to grandparent PID if no SCM wrapper is found.
func findSCMWrapperPID() int {
	if runtime.GOOS != "linux" {
		// Fallback for non-Linux: use grandparent
		ppid := os.Getppid()
		if gppid := getParentPID(ppid); gppid > 0 {
			return gppid
		}
		return ppid
	}

	// Walk up the process tree
	pid := os.Getppid()
	visited := make(map[int]bool)

	for pid > 1 && !visited[pid] {
		visited[pid] = true

		if isSCMWrapper(pid) {
			return pid
		}

		// Move to parent
		ppid := getParentPID(pid)
		if ppid <= 0 {
			break
		}
		pid = ppid
	}

	// Fallback: return grandparent if no SCM wrapper found
	ppid := os.Getppid()
	if gppid := getParentPID(ppid); gppid > 0 {
		return gppid
	}
	return ppid
}

// isSCMWrapper checks if a process is a registered SCM LLM wrapper command.
func isSCMWrapper(pid int) bool {
	cmdline := getProcessCmdline(pid)
	if len(cmdline) < 2 {
		return false
	}

	// Check if first arg is ctxloom binary
	exe := cmdline[0]
	if !strings.HasSuffix(exe, "ctxloom") && !strings.HasSuffix(exe, "/ctxloom") {
		return false
	}

	// Check if second arg is a registered wrapper verb
	return IsLLMWrapper(cmdline[1])
}

// getProcessCmdline reads the command line arguments of a process.
func getProcessCmdline(pid int) []string {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return nil
	}

	// cmdline is null-separated
	var args []string
	for _, arg := range strings.Split(string(data), "\x00") {
		if arg != "" {
			args = append(args, arg)
		}
	}
	return args
}

// getParentPID reads the parent PID of a given process from /proc.
// Returns -1 if unable to read (e.g., on non-Linux systems or invalid PID).
func getParentPID(pid int) int {
	if runtime.GOOS != "linux" {
		return -1
	}

	statPath := fmt.Sprintf("/proc/%d/stat", pid)
	f, err := os.Open(statPath)
	if err != nil {
		return -1
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		return -1
	}

	// /proc/[pid]/stat format: pid (comm) state ppid ...
	// We need field 4 (ppid), but comm can contain spaces and parens,
	// so we find the last ')' and parse from there.
	line := scanner.Text()
	lastParen := strings.LastIndex(line, ")")
	if lastParen < 0 || lastParen+2 >= len(line) {
		return -1
	}

	// Fields after comm: state ppid pgrp session tty_nr ...
	fields := strings.Fields(line[lastParen+2:])
	if len(fields) < 2 {
		return -1
	}

	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return -1
	}

	return ppid
}

var metaStampCmd = &cobra.Command{
	Use:   "stamp",
	Short: "Output PID and timestamp for session tracking",
	Long: `Outputs the SCM wrapper process ID (ctxloom run/plugin/init) and current timestamp.
The SCM wrapper PID is used because it remains stable across /clear commands,
while the Claude Code process may restart.`,
	Run: func(cmd *cobra.Command, args []string) {
		stamp := Stamp{
			PID:  findSCMWrapperPID(),
			Time: time.Now().UTC().Format(time.RFC3339),
		}
		data, _ := json.Marshal(stamp)
		fmt.Printf("<!-- SCM_STAMP: %s -->\n", string(data))
	},
}

func init() {
	rootCmd.AddCommand(metaCmd)
	metaCmd.AddCommand(metaStampCmd)
}
