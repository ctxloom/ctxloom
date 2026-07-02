// Package container embeds the agent-image Containerfiles so the isolation
// policy can build an absent agent image locally at run time (the on-the-fly
// half of the container degrade gate). The `container-build-*` just recipes are
// the ahead-of-time half over these same files — one source of truth, two build
// paths.
package container

import _ "embed"

// Minimal is the transport-only agent image: the static ctxloom binary on a
// small base, no engine CLI (Info()/mock level).
//
//go:embed minimal/Containerfile
var Minimal []byte

// ClaudeCode is the production claude agent image: a real claude CLI plus the
// static ctxloom binary.
//
//go:embed production/Containerfile-claude-code
var ClaudeCode []byte

// Kiro is the production kiro agent image: a real kiro-cli plus the static
// ctxloom binary.
//
//go:embed production/Containerfile-kiro
var Kiro []byte
