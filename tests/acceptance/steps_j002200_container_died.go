//go:build acceptance

package acceptance

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/cucumber/godog"
)

// j002200DiedRuntimeScript is a daemon that ACCEPTS everything and then never
// has a container — the one failure the image and runtime gates cannot produce.
//
//	info            -> 0 + rootless, so a container tier is SELECTED and the
//	                   requested ownership resolves. Reporting reachable
//	                   without rootless sends the run to chainFor's runtime
//	                   gate, which is a DIFFERENT row.
//	image inspect   -> 0, and build -> 0, so the image gate passes. This is
//	                   what separates this row from the
//	                   image-cannot-be-produced one: everything before the
//	                   start succeeds.
//	run             -> 0. Two callers matter and both must succeed. The
//	                   SHARED-FILESYSTEM PROBE bind-mounts a directory and cats
//	                   /probe/marker, and a daemon that cannot echo the marker
//	                   back is judged not to share this filesystem — measured,
//	                   and it aborts at the WORKSPACE gate with a
//	                   plausible-looking isolation finding from the wrong gate.
//	                   So the stub serves that one call honestly. The KEEPALIVE
//	                   run then exits 0 immediately: StartRunner returns as soon
//	                   as it is spawned, so the run proceeds believing it has a
//	                   container.
//	container inspect -> 1, so {{.State.Running}} never reads "true" and the
//	                   container is never observed running.
//
// The keepalive's immediate exit is what makes this fast: AwaitContainerRunning
// races the runner's Wait against a 30s deadline and takes the Wait arm on the
// first poll rather than burning the deadline.
const j002200DiedRuntimeScript = `#!/bin/sh
case "$1 $2" in
  "image inspect") exit 0 ;;
  "container inspect") exit 1 ;;
esac
case "$1" in
  info) echo '[name=seccomp,profile=builtin name=rootless name=cgroupns]'; exit 0 ;;
  build) exit 0 ;;
  run)
    src=""
    for a in "$@"; do
      case "$a" in
        type=bind,source=*) src=${a#type=bind,source=}; src=${src%%,*} ;;
      esac
    done
    for a in "$@"; do
      if [ "$a" = "/probe/marker" ] && [ -n "$src" ]; then
        # Builtins only. This fixture's PATH is the stub dir ALONE, so cat and
        # every other external is absent — a stub that shelled out here read
        # back an empty marker, the probe judged the filesystem unshared, and
        # the run aborted at the WORKSPACE gate looking convincingly correct.
        # NOT "read ... && printf": the probe writes the marker with NO
        # trailing newline, and POSIX read returns NON-ZERO at EOF without a
        # delimiter even though it assigned the line. Gating the printf on that
        # status printed nothing, the daemon "read" an empty marker, and the run
        # aborted at the WORKSPACE gate looking entirely plausible.
        IFS= read -r line < "$src/marker"
        printf '%s' "$line"
        exit 0
      fi
    done
    exit 0 ;;
  *) exit 0 ;;
esac
`

// j002200InstallDiedStub writes that daemon onto a PATH of its own and returns
// the stub's path, having confirmed that "docker" now resolves TO IT. Without
// that last check the row would silently run against a real daemon.
func j002200InstallDiedStub(w *World) (string, error) {
	binDir := filepath.Join(w.env.Root, "j002200-died-runtime-bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return "", fmt.Errorf("create died-runtime bin dir: %w", err)
	}
	stub := filepath.Join(binDir, "docker")
	if err := os.WriteFile(stub, []byte(j002200DiedRuntimeScript), 0o755); err != nil {
		return "", fmt.Errorf("write stub docker: %w", err)
	}
	w.env.SetEnv("PATH", isoSanitizedPATH(binDir))

	found, err := exec.LookPath("docker")
	if err != nil || found != stub {
		return "", fmt.Errorf("stub install failed: docker resolves to %q (err=%v), not the stub %q — this row would run against a real daemon", found, err, stub)
	}
	return stub, nil
}

// j002200StubDiedRuntime installs that daemon and PROVES each probe answers as
// the scenario needs before the run starts. Every check here corresponds to a
// gate this row must NOT hit: a stub that silently stopped resolving, or that
// reported the image absent, would hand the assertion to a different gate and
// the row would stay green while testing nothing.
func j002200StubDiedRuntime(w *World) error {
	stub, err := j002200InstallDiedStub(w)
	if err != nil {
		return err
	}
	if err := exec.Command(stub, "info").Run(); err != nil {
		return fmt.Errorf("stub docker does not report a reachable daemon (%v); chainFor would take the runtime-unreachable branch and this row would assert the wrong gate", err)
	}
	sec, err := exec.Command(stub, "info", "--format", "{{.SecurityOptions}}").Output()
	if err != nil || !strings.Contains(string(sec), "rootless") {
		return fmt.Errorf("stub docker does not report ROOTLESS ownership (%q, err=%v); the agent asks for container-rootless and would abort at the runtime gate", strings.TrimSpace(string(sec)), err)
	}
	if err := exec.Command(stub, "image", "inspect", "anything").Run(); err != nil {
		return fmt.Errorf("stub docker reports the image ABSENT (%v); the run would abort at the IMAGE gate, which is a different row", err)
	}
	if err := exec.Command(stub, "container", "inspect", "-f", "{{.State.Running}}", "anything").Run(); err == nil {
		return fmt.Errorf("stub docker reports the container RUNNING; AwaitContainerRunning would succeed and this row would assert nothing")
	}
	if err := exec.Command(stub, "run", "--rm", "hello").Run(); err != nil {
		return fmt.Errorf("stub docker cannot run a container at all (%v); the shared-filesystem probe would fail and the run would abort at the WORKSPACE gate, not the transport-start gate this row asserts", err)
	}
	return nil
}

// j002200DiedFinding is the message the transport-start gate must carry. It
// names the specific fault rather than the word "container", because three
// different isolation gates mention containers and matching the generic word
// is how a row ends up asserting whichever gate the host happens to hit.
const j002200DiedFinding = "never reached running state"

func registerJ002200ContainerDiedSteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^Alice runs a container-bound agent whose container dies at the daemon, with flags "([^"]*)"$`,
		func(c context.Context, flags string) error {
			w := worldFrom(c)
			j002200 := j002200Of(w)
			if err := j002200StubDiedRuntime(w); err != nil {
				return err
			}
			recPath := filepath.Join(j002200.recordDir, fmt.Sprintf("died-%d.txt", len(j002200.records)))
			if err := w.env.WriteHomeFile(".ctxloom/config.yaml", j002200HomeConfigYAML(recPath)); err != nil {
				return err
			}
			j002200.lastContainerRecPath = recPath
			args := []string{"run", "--agent", "mock-container", "--one-shot"}
			if flags != "" {
				args = append(args, strings.Fields(flags)...)
			}
			args = append(args, "isolation-check")
			_ = w.env.Run(args...) // exit status asserted by the Then step
			return nil
		})

	ctx.Step(`^the run aborts because the container never reached running state$`, func(c context.Context) error {
		w := worldFrom(c)
		j002200 := j002200Of(w)
		out := w.env.LastOutput()

		if code := w.env.LastExitCode(); code != 3 {
			return fmt.Errorf("expected exit 3 (fatal isolation finding), got %d — a plain error return is exit 1 and is exactly the defect this row exists to catch; output:\n%s", code, out)
		}
		if !strings.Contains(out, j002200DiedFinding) {
			return fmt.Errorf("output does not name the transport-start fault (%q); the abort came from a different gate; output:\n%s", j002200DiedFinding, out)
		}
		class := "[" + string(strictness.ClassIsolation) + "]"
		if !strings.Contains(out, class) {
			return fmt.Errorf("output does not classify the abort as %s, so a caller cannot branch on whether isolation held; output:\n%s", class, out)
		}
		if strings.Contains(out, j002200StartGateFinding) || strings.Contains(out, j002200RuntimeGateFinding) {
			return fmt.Errorf("the abort came from the image or runtime gate, not the transport-start gate this row asserts; output:\n%s", out)
		}
		// The EFFECT, not just the message: a container that never ran must not
		// have produced an engine turn. The mock writes its record only when it
		// actually executes, so a present record means the engine launched
		// anyway — the unsandboxed launch the finding claims to have prevented.
		if _, err := os.Stat(j002200.lastContainerRecPath); err == nil {
			body, _ := os.ReadFile(j002200.lastContainerRecPath)
			return fmt.Errorf("the engine RAN despite the abort (record at %s):\n%s", j002200.lastContainerRecPath, body)
		}
		return nil
	})
}
