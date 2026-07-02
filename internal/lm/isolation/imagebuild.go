package isolation

import (
	"bytes"
	"context"
	"debug/elf"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// imageBuildTimeout caps one on-the-fly agent-image build. The production
// builds are network-bound (npm / the kiro installer) so the first build takes
// minutes; a hung build must still degrade rather than block the LLM forever.
const imageBuildTimeout = 10 * time.Minute

// resolveSelfExe is the seam over the running-binary check (overridable in
// tests, like hostHomeDir): it yields the path buildImage bakes into the image.
var resolveSelfExe = selfStaticLinuxExe

// ensureImage is the image half of the container degrade gate. Present → run.
// Absent, the profile carries an embedded Containerfile, and the running binary
// can serve as the in-container ctxloom → build the image LOCALLY (the same
// context shape as the container-build just recipes: Containerfile + static
// ctxloom, except the running binary IS the artifact — no go toolchain needed).
// Anything else errors so the caller degrades (CLAUDE.md fault tolerance): the
// build is a best-effort convenience, never a blocker.
func (c Container) ensureImage(ctx context.Context) error {
	if c.imagePresent(ctx) {
		return nil
	}
	if len(c.profile.containerfile) == 0 {
		return fmt.Errorf("container image %q is not present (no embedded build recipe for this engine; build one with the container-build just recipes)", c.image)
	}
	selfExe, err := resolveSelfExe()
	if err != nil {
		return fmt.Errorf("container image %q is not present and cannot be built from this binary: %w", c.image, err)
	}
	clidiag.Warn("ctxloom", "container image %q not found; building it locally (first run — this may take a few minutes)", c.image)
	if err := buildImage(ctx, c.runtime, c.image, c.profile.containerfile, selfExe); err != nil {
		return fmt.Errorf("local build of container image %q failed: %w", c.image, err)
	}
	if !c.imagePresent(ctx) {
		return fmt.Errorf("container image %q is still absent after a local build", c.image)
	}
	return nil
}

// selfStaticLinuxExe returns the running executable's path when it can serve as
// the in-container ctxloom binary: a linux host (the container shares the host
// kernel/arch), an ELF, and statically linked (no PT_INTERP) — which is how the
// release/container builds produce ctxloom (CGO_ENABLED=0). Anything else errors
// so the caller degrades instead of baking an unrunnable binary into an image.
func selfStaticLinuxExe() (string, error) {
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("host is %s and the agent image needs a linux ctxloom (build it ahead of time via the container-build just recipes)", runtime.GOOS)
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve running executable: %w", err)
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return "", fmt.Errorf("resolve running executable: %w", err)
	}
	f, err := elf.Open(exe)
	if err != nil {
		return "", fmt.Errorf("running executable is not an ELF binary: %w", err)
	}
	defer f.Close()
	for _, p := range f.Progs {
		if p.Type == elf.PT_INTERP {
			return "", fmt.Errorf("running executable is dynamically linked; the image needs a static (CGO_ENABLED=0) ctxloom")
		}
	}
	return exe, nil
}

// buildImage runs `<runtime> build -t <image>` over a temp context holding the
// embedded Containerfile plus the running static ctxloom binary. Build output is
// captured and surfaced (tail only) on failure; success is silent. Capped at
// imageBuildTimeout.
func buildImage(ctx context.Context, rt ContainerRuntime, image string, containerfile []byte, selfExe string) error {
	dir, err := os.MkdirTemp("", "ctxloom-imgbuild-")
	if err != nil {
		return fmt.Errorf("image build context: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if err := os.WriteFile(filepath.Join(dir, "Containerfile"), containerfile, 0o644); err != nil {
		return fmt.Errorf("image build context: %w", err)
	}
	if err := copyExecutable(selfExe, filepath.Join(dir, "ctxloom")); err != nil {
		return fmt.Errorf("image build context: %w", err)
	}

	bctx, cancel := context.WithTimeout(ctx, imageBuildTimeout)
	defer cancel()
	cmd := exec.CommandContext(bctx, rt.Binary(), "build", "-t", image, "-f", filepath.Join(dir, "Containerfile"), dir)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s build: %w\n%s", rt.Name(), err, tailLines(out.String(), 20))
	}
	return nil
}

// copyExecutable copies src to dst with the executable bit set (the build
// context is a fresh temp dir, so a plain copy suffices — no atomicity needed).
func copyExecutable(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// tailLines returns the last n lines of s — enough build-failure context to
// diagnose without dumping a whole multi-minute build log into a warning.
func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
