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

// Base is the DEFAULT shared base image (stage 1 of every locally-built agent
// image): the distro plus the coding-agent tool layer (git, ripgrep, curl,
// certs, unzip, jq). The production Containerfiles layer agent specifics onto
// it via ARG BASE_IMAGE; a user-provided base Containerfile replaces this one
// (isolation_base_containerfile / `container build --base-containerfile`).
//
//go:embed base/Containerfile
var Base []byte

// ClaudeCode is the production claude agent image's AGENT stage: a real claude
// CLI + the claude-code-acp adapter plus the static ctxloom binary, layered
// onto the base image via ARG BASE_IMAGE.
//
//go:embed production/Containerfile-claude-code
var ClaudeCode []byte

// Kiro is the production kiro agent image's AGENT stage: a real kiro-cli plus
// the static ctxloom binary, layered onto the base image via ARG BASE_IMAGE.
//
//go:embed production/Containerfile-kiro
var Kiro []byte
