// Package container embeds the agent-image Containerfiles so the isolation
// policy can build an absent agent image locally at run time (the on-the-fly
// half of the container degrade gate). The `container-build-*` just recipes are
// the ahead-of-time half over these same files — one source of truth, two build
// paths.
//
// Every asset is delivered through an accessor returning a fresh COPY, never as
// an exported `[]byte` global: the embedded bytes are the process-wide source of
// truth for every image build and probe in a run, so no caller may be in a
// position to corrupt them for every later reader.
package container

import (
	"embed"
	"fmt"
	"io/fs"
	"slices"
)

//go:embed base/Containerfile entrypoint.sh seccomp/probe-seccomp.json
var assets embed.FS

// Base is the DEFAULT shared base image Containerfile (stage 1 of every
// locally-built agent image): the distro plus the coding-agent tool layer (git,
// ripgrep, curl, certs, unzip, jq). The generated composed agent Containerfile
// (isolation.composeAgentContainerfile) layers engine specifics onto it via
// ARG BASE_IMAGE; a user-provided base Containerfile, or an auto-detected
// project devcontainer, replaces this one (isolation_base_containerfile /
// `container build --base-containerfile`, or .devcontainer/devcontainer.json).
func Base() []byte { return asset("base/Containerfile") }

// Entrypoint is the agent-image entrypoint script: the runtime PUID/PGID
// identity remap (generic ctxloom user → the launching uid/gid, then a
// privilege drop). Staged into every agent-stage build context as
// `ctxloom-entrypoint` and installed by the agent Containerfiles.
func Entrypoint() []byte { return asset("entrypoint.sh") }

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
func ProbeSeccomp() []byte { return asset("seccomp/probe-seccomp.json") }

// asset reads one embedded file and returns a private copy of its bytes.
func asset(name string) []byte { return assetFrom(assets, name) }

// assetFrom is asset over an injectable filesystem, so the guards below are
// exercisable against a truncated asset that cannot be committed to this
// package (an empty file here would only fail the build if it were MISSING).
//
// BOTH failures panic rather than returning bytes, mirroring
// resources.MustGetPromptText: each is a build-time bug, not a runtime
// condition, because the bytes are compiled into the binary.
//   - unreadable: the embed pattern and the accessor name disagree.
//   - EMPTY: `//go:embed` accepts a 0-byte file happily and fs.ReadFile reports
//     success for it, so a truncated Containerfile would otherwise flow on as a
//     zero-instruction build context, an empty entrypoint script, or an empty
//     seccomp document — each failing far from its cause, and the seccomp one
//     weakening the probe's confinement rather than failing at all.
func assetFrom(fsys fs.FS, name string) []byte {
	b, err := fs.ReadFile(fsys, name)
	if err != nil {
		panic(fmt.Sprintf("container: embedded asset %q: %v", name, err))
	}
	if len(b) == 0 {
		panic(fmt.Sprintf("container: embedded asset %q is empty — the file in the container/ package is truncated", name))
	}
	return slices.Clone(b)
}
