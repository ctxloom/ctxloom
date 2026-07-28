// Package container embeds the agent-image Containerfiles so the isolation
// policy can build an absent agent image locally at run time (the on-the-fly
// half of the container degrade gate). The `container-build-*` just recipes are
// the ahead-of-time half over these same files — one source of truth, two build
// paths.
package container

import _ "embed"

// Base is the DEFAULT shared base image (stage 1 of every locally-built agent
// image): the distro plus the coding-agent tool layer (git, ripgrep, curl,
// certs, unzip, jq). The generated composed agent Containerfile
// (isolation.composeAgentContainerfile) layers engine specifics onto it via
// ARG BASE_IMAGE; a user-provided base Containerfile, or an auto-detected
// project devcontainer, replaces this one (isolation_base_containerfile /
// `container build --base-containerfile`, or .devcontainer/devcontainer.json).
//
//go:embed base/Containerfile
var Base []byte

// Entrypoint is the agent-image entrypoint script: the runtime PUID/PGID
// identity remap (generic ctxloom user → the launching uid/gid, then a
// privilege drop). Staged into every agent-stage build context as
// `ctxloom-entrypoint` and installed by the agent Containerfiles.
//
//go:embed entrypoint.sh
var Entrypoint []byte

// ProbeSeccomp is the PROBE-ONLY seccomp profile the isolation read-observation
// probe passes via `--security-opt seccomp=<file>`. It is a complete, TIGHT
// container default (defaultAction SCMP_ACT_ERRNO, the full standard syscall
// allowlist) that additionally permits the ptrace family (ptrace,
// process_vm_readv, process_vm_writev) UNCONDITIONALLY — the one delta from
// Docker's compiled-in default, which gates those behind CAP_SYS_PTRACE. It is
// NOT `seccomp=unconfined`: every other syscall stays under the default policy.
// This is the minimal privilege delta that lets strace trace its OWN children
// in-container (same-uid tracing) without the far broader CAP_SYS_PTRACE. Used
// ONLY on the probe path (isolation.TraceProbe); production runs keep Docker's
// default profile untouched.
//
//go:embed seccomp/probe-seccomp.json
var ProbeSeccomp []byte
