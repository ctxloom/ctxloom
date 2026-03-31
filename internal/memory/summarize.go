package memory

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/ctxloom/ctxloom/internal/collections"
)

// summarizeToolResult creates a concise summary of a tool result.
// Tool-specific summarization helps reduce log size while preserving key info.
func summarizeToolResult(toolName, result string, maxSize int) string {
	// If result is small enough, return as-is
	if len(result) <= maxSize {
		return result
	}

	// Tool-specific summarization
	switch toolName {
	case "Read":
		return summarizeReadResult(result, maxSize)
	case "Glob":
		return summarizeGlobResult(result, maxSize)
	case "Grep":
		return summarizeGrepResult(result, maxSize)
	case "Bash":
		return summarizeBashResult(result, maxSize)
	case "Write":
		return summarizeWriteResult(result, maxSize)
	case "Edit":
		return summarizeEditResult(result, maxSize)
	default:
		return truncateWithMarker(result, maxSize)
	}
}

func summarizeReadResult(result string, maxSize int) string {
	lines := strings.Count(result, "\n") + 1

	// Extract file path if present (usually in first line or error message)
	path := extractPathFromResult(result)
	if path != "" {
		return fmt.Sprintf("[Read %d lines from %s]", lines, path)
	}
	return fmt.Sprintf("[Read %d lines]", lines)
}

func summarizeGlobResult(result string, maxSize int) string {
	lines := strings.Split(strings.TrimSpace(result), "\n")
	count := len(lines)
	if count == 0 || (count == 1 && lines[0] == "") {
		return "[Glob: no matches]"
	}

	// Show first few matches
	preview := lines
	if len(preview) > 5 {
		preview = preview[:5]
	}

	if count > 5 {
		return fmt.Sprintf("[Glob: %d files]\n%s\n... and %d more", count, strings.Join(preview, "\n"), count-5)
	}
	return fmt.Sprintf("[Glob: %d files]\n%s", count, strings.Join(preview, "\n"))
}

func summarizeGrepResult(result string, maxSize int) string {
	lines := strings.Split(strings.TrimSpace(result), "\n")
	count := len(lines)
	if count == 0 || (count == 1 && lines[0] == "") {
		return "[Grep: no matches]"
	}

	// Count unique files
	files := collections.NewSet[string]()
	for _, line := range lines {
		if idx := strings.Index(line, ":"); idx > 0 {
			files.Add(line[:idx])
		}
	}

	return fmt.Sprintf("[Grep: %d matches in %d files]", count, files.Len())
}

func summarizeBashResult(result string, maxSize int) string {
	lines := strings.Split(result, "\n")
	lineCount := len(lines)

	// For short outputs, include the content
	if len(result) <= maxSize {
		return result
	}

	// For long outputs, show first and last few lines
	if lineCount > 10 {
		first := strings.Join(lines[:3], "\n")
		last := strings.Join(lines[lineCount-3:], "\n")
		return fmt.Sprintf("%s\n... [%d lines omitted] ...\n%s", first, lineCount-6, last)
	}

	return truncateWithMarker(result, maxSize)
}

func summarizeWriteResult(result string, maxSize int) string {
	// Write results are usually short confirmations
	if strings.Contains(result, "successfully") {
		return result
	}
	return truncateWithMarker(result, maxSize)
}

func summarizeEditResult(result string, maxSize int) string {
	// Edit results are usually short confirmations
	if strings.Contains(result, "successfully") {
		return result
	}
	return truncateWithMarker(result, maxSize)
}

func truncateWithMarker(s string, maxSize int) string {
	if len(s) <= maxSize {
		return s
	}
	// Leave room for truncation marker
	return s[:maxSize-20] + "\n... [truncated]"
}

// extractPathFromResult attempts to extract a file path from result text.
func extractPathFromResult(result string) string {
	// Common patterns for file paths
	patterns := []string{
		`(?m)^(/[^\s:]+)`,        // Unix absolute path at line start
		`(?m)([A-Za-z]:\\[^\s:]+)`, // Windows path
	}

	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		if match := re.FindStringSubmatch(result); len(match) > 1 {
			return match[1]
		}
	}

	return ""
}
