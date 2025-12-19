package ai

import (
	"context"
	"io"
)

// Request represents a request to an AI backend.
type Request struct {
	Prompt  string
	Context string // Pre-assembled context to include
	WorkDir string // Working directory for execution
	Print   bool   // Print mode: output response and exit (non-interactive)
}

// Response represents the response from an AI backend.
type Response struct {
	Output   string
	ExitCode int
}

// PluginConfig holds configuration for a specific AI plugin.
type PluginConfig struct {
	BinaryPath string            `mapstructure:"binary_path" yaml:"binary_path,omitempty"`
	Args       []string          `mapstructure:"args" yaml:"args,omitempty"`
	Env        map[string]string `mapstructure:"env" yaml:"env,omitempty"`
}

// Plugin defines the interface for AI backend plugins.
type Plugin interface {
	// Name returns the unique identifier for this plugin.
	Name() string

	// Run executes the AI with the given request.
	// The provided context can be used for cancellation.
	// Stdout and stderr writers allow streaming output.
	Run(ctx context.Context, req Request, stdout, stderr io.Writer) (*Response, error)
}

// ConfigurablePlugin is an optional interface for plugins that accept configuration.
type ConfigurablePlugin interface {
	Plugin
	// Configure applies the given configuration to the plugin.
	Configure(cfg PluginConfig)
}

// CommandPreviewPlugin is an optional interface for plugins that can show the command to be executed.
type CommandPreviewPlugin interface {
	Plugin
	// CommandPreview returns the command that would be executed for the given request.
	CommandPreview(req Request) string
}

// StreamingPlugin is an optional interface for plugins that support streaming.
type StreamingPlugin interface {
	Plugin
	// RunStreaming executes with real-time output streaming.
	RunStreaming(ctx context.Context, req Request, stdout, stderr io.Writer) (*Response, error)
}
