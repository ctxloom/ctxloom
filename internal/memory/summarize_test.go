package memory

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSummarizeToolResult_SmallResult(t *testing.T) {
	// Results smaller than maxSize are returned as-is
	result := "small output"
	summarized := summarizeToolResult("Bash", result, 100)
	assert.Equal(t, result, summarized)
}

func TestSummarizeToolResult_ToolDispatch(t *testing.T) {
	largeResult := strings.Repeat("x", 1000)
	// Bash needs many lines to trigger the "omitted" path
	bashLargeResult := strings.Repeat("line\n", 200)

	tests := []struct {
		name     string
		toolName string
		result   string
		contains string
	}{
		{"Read tool", "Read", largeResult, "[Read"},
		{"Glob tool", "Glob", largeResult, "[Glob"},
		{"Grep tool", "Grep", largeResult, "[Grep"},
		{"Bash tool", "Bash", bashLargeResult, "omitted"},
		{"Write tool", "Write", largeResult, "[truncated]"},
		{"Edit tool", "Edit", largeResult, "[truncated]"},
		{"Unknown tool", "Unknown", largeResult, "[truncated]"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			summarized := summarizeToolResult(tt.toolName, tt.result, 100)
			assert.Contains(t, summarized, tt.contains)
		})
	}
}

func TestSummarizeReadResult(t *testing.T) {
	tests := []struct {
		name     string
		result   string
		expected string
	}{
		{
			name:     "with unix path",
			result:   "/home/user/file.txt\nline1\nline2\nline3",
			expected: "[Read 4 lines from /home/user/file.txt]",
		},
		{
			name:     "without path",
			result:   "line1\nline2\nline3",
			expected: "[Read 3 lines]",
		},
		{
			name:     "single line",
			result:   "single line content",
			expected: "[Read 1 lines]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := summarizeReadResult(tt.result, 50)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestSummarizeGlobResult(t *testing.T) {
	tests := []struct {
		name     string
		result   string
		contains []string
	}{
		{
			name:     "no matches - empty",
			result:   "",
			contains: []string{"[Glob: no matches]"},
		},
		{
			name:     "no matches - whitespace",
			result:   "   \n  ",
			contains: []string{"[Glob: no matches]"},
		},
		{
			name:     "few files",
			result:   "file1.go\nfile2.go\nfile3.go",
			contains: []string{"[Glob: 3 files]", "file1.go", "file2.go", "file3.go"},
		},
		{
			name:     "many files - truncated",
			result:   "file1.go\nfile2.go\nfile3.go\nfile4.go\nfile5.go\nfile6.go\nfile7.go",
			contains: []string{"[Glob: 7 files]", "file1.go", "and 2 more"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := summarizeGlobResult(tt.result, 500)
			for _, want := range tt.contains {
				assert.Contains(t, result, want)
			}
		})
	}
}

func TestSummarizeGrepResult(t *testing.T) {
	tests := []struct {
		name     string
		result   string
		expected string
	}{
		{
			name:     "no matches - empty",
			result:   "",
			expected: "[Grep: no matches]",
		},
		{
			name:     "no matches - whitespace",
			result:   "  ",
			expected: "[Grep: no matches]",
		},
		{
			name:     "single file multiple matches",
			result:   "file.go:10:match1\nfile.go:20:match2\nfile.go:30:match3",
			expected: "[Grep: 3 matches in 1 files]",
		},
		{
			name:     "multiple files",
			result:   "file1.go:10:match1\nfile2.go:20:match2\nfile3.go:30:match3",
			expected: "[Grep: 3 matches in 3 files]",
		},
		{
			name:     "lines without colon",
			result:   "no colon here\nanother line",
			expected: "[Grep: 2 matches in 0 files]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := summarizeGrepResult(tt.result, 500)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestSummarizeBashResult(t *testing.T) {
	tests := []struct {
		name     string
		result   string
		maxSize  int
		contains []string
	}{
		{
			name:     "short output - returned as-is",
			result:   "short output",
			maxSize:  100,
			contains: []string{"short output"},
		},
		{
			name:     "long output with many lines",
			result:   "line1\nline2\nline3\nline4\nline5\nline6\nline7\nline8\nline9\nline10\nline11\nline12",
			maxSize:  50,
			contains: []string{"line1", "line2", "line3", "omitted", "line10", "line11", "line12"},
		},
		{
			name:     "long output few lines",
			result:   strings.Repeat("x", 200),
			maxSize:  50,
			contains: []string{"[truncated]"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := summarizeBashResult(tt.result, tt.maxSize)
			for _, want := range tt.contains {
				assert.Contains(t, result, want)
			}
		})
	}
}

func TestSummarizeWriteResult(t *testing.T) {
	tests := []struct {
		name     string
		result   string
		maxSize  int
		expected string
	}{
		{
			name:     "success message preserved",
			result:   "File written successfully to /path/file.txt",
			maxSize:  50,
			expected: "File written successfully to /path/file.txt",
		},
		{
			name:     "long result truncated",
			result:   strings.Repeat("x", 200),
			maxSize:  50,
			expected: strings.Repeat("x", 30) + "\n... [truncated]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := summarizeWriteResult(tt.result, tt.maxSize)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestSummarizeEditResult(t *testing.T) {
	tests := []struct {
		name     string
		result   string
		maxSize  int
		expected string
	}{
		{
			name:     "success message preserved",
			result:   "Edit applied successfully",
			maxSize:  50,
			expected: "Edit applied successfully",
		},
		{
			name:     "long result truncated",
			result:   strings.Repeat("y", 200),
			maxSize:  50,
			expected: strings.Repeat("y", 30) + "\n... [truncated]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := summarizeEditResult(tt.result, tt.maxSize)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestTruncateWithMarker(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxSize  int
		contains string
	}{
		{
			name:     "short string unchanged",
			input:    "short",
			maxSize:  100,
			contains: "short",
		},
		{
			name:     "long string truncated",
			input:    strings.Repeat("a", 100),
			maxSize:  50,
			contains: "[truncated]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := truncateWithMarker(tt.input, tt.maxSize)
			assert.Contains(t, result, tt.contains)
			if tt.maxSize < len(tt.input) {
				assert.LessOrEqual(t, len(result), tt.maxSize)
			}
		})
	}
}

func TestExtractPathFromResult(t *testing.T) {
	tests := []struct {
		name     string
		result   string
		expected string
	}{
		{
			name:     "unix absolute path",
			result:   "/home/user/file.txt\nsome content",
			expected: "/home/user/file.txt",
		},
		{
			name:     "windows path",
			result:   "C:\\Users\\file.txt\nsome content",
			expected: "C:\\Users\\file.txt",
		},
		{
			name:     "no path",
			result:   "just some content",
			expected: "",
		},
		{
			name:     "path with colon separator",
			result:   "/home/user/file.txt: content here",
			expected: "/home/user/file.txt",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractPathFromResult(tt.result)
			assert.Equal(t, tt.expected, result)
		})
	}
}
