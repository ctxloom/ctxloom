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

// j002200StubRuntimeScript answers exactly the two probes that decide whether a
// container tier is SELECTED and then whether it can START:
//
//	`info`          -> 0 AND prints SecurityOptions containing "rootless", so
//	                   isolation.runtimeReachable reports the daemon up and
//	                   isolation.dockerIsRootless resolves the OWNERSHIP the
//	                   agent asked for. Reporting reachable without ownership
//	                   sends the run to chainFor's runtime gate instead — the
//	                   wrong gate, and exactly what this row must not assert.
//	`image inspect` -> 1, so isolation.Container.imagePresent reports absent.
//
// Everything else fails, because nothing else should be reached: the run must
// abort at the image gate, and a stub that cheerfully answered `run` would let
// a broken gate look healthy.
const j002200StubRuntimeScript = `#!/bin/sh
case "$1" in
  info) echo '[name=seccomp,profile=builtin name=rootless name=cgroupns]'; exit 0 ;;
  *) exit 1 ;;
esac
`

// j002200StubContainerRuntime is the MIRROR of j002200MaskContainerRuntime.
// That helper removes every runtime from PATH to force chainFor's
// runtime-unreachable gate; this one supplies a runtime that reports itself
// reachable so a container policy IS selected, and then cannot produce the
// image — which is the only way to reach isolation.prepareChain's
// container-to-host downgrade branch.
//
// It FAILS rather than proceeding if the stub does not resolve. A stub that
// quietly stopped taking effect would hand this row back to whichever gate the
// host machine happens to hit, and it would still be green.
func j002200StubContainerRuntime(w *World) error {
	binDir := filepath.Join(w.env.Root, "j002200-stub-runtime-bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return fmt.Errorf("create stub-runtime bin dir: %w", err)
	}
	stub := filepath.Join(binDir, "docker")
	if err := os.WriteFile(stub, []byte(j002200StubRuntimeScript), 0o755); err != nil {
		return fmt.Errorf("write stub docker: %w", err)
	}
	w.env.SetEnv("PATH", isoSanitizedPATH(binDir))

	found, err := exec.LookPath("docker")
	if err != nil {
		return fmt.Errorf("stub install failed: docker does not resolve on the masked PATH, so no container policy would be selected and this row would assert the wrong gate")
	}
	if found != stub {
		return fmt.Errorf("stub install failed: docker resolves to %q, not the stub %q — this row would run against a real daemon", found, stub)
	}
	if err := exec.Command(stub, "info").Run(); err != nil {
		return fmt.Errorf("stub docker does not report a reachable daemon (%v); chainFor would take the runtime-unreachable branch instead", err)
	}
	sec, err := exec.Command(stub, "info", "--format", "{{.SecurityOptions}}").Output()
	if err != nil || !strings.Contains(string(sec), "rootless") {
		return fmt.Errorf("stub docker does not report ROOTLESS ownership (%q, err=%v); the agent asks for container-rootless, so the run would abort at chainFor's runtime gate instead of the START gate this row asserts", strings.TrimSpace(string(sec)), err)
	}
	if err := exec.Command(stub, "image", "inspect", "anything").Run(); err == nil {
		return fmt.Errorf("stub docker reports the image PRESENT; the run would proceed past the image gate and this row would assert nothing")
	}
	return nil
}

func registerJ002200StartGateSteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^Alice runs a container-bound agent whose image cannot be produced, with flags "([^"]*)"$`,
		func(c context.Context, flags string) error {
			w := worldFrom(c)
			j002200 := j002200Of(w)
			if err := j002200StubContainerRuntime(w); err != nil {
				return err
			}
			recPath := filepath.Join(j002200.recordDir, fmt.Sprintf("startgate-%d.txt", len(j002200.records)))
			if err := w.env.WriteHomeFile(".ctxloom/config.yaml", j002200StartGateConfigYAML(recPath)); err != nil {
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

	ctx.Step(`^the run aborts at the container START gate$`, func(c context.Context) error {
		return j002200AssertStartGateAbort(worldFrom(c))
	})
}

// j002200StartGateConfigYAML pins an isolation_images override for the mock
// engine. ImageConfig.Image is documented "run AS-IS and never built", so an
// image that does not exist is absent AND unbuildable — which is what makes
// isolation.Container.ensureImage error instead of building. The DEFAULT image
// name is content-addressed and therefore absent-but-BUILDABLE, so ctxloom
// builds it and never reaches the gate; the override is the only way in.
func j002200StartGateConfigYAML(recordFile string) string {
	return j002200HomeConfigYAML(recordFile) +
		"isolation_images:\n  mock: ctxloom-nonexistent-image-for-start-gate:absent\n"
}

// j002200AssertStartGateAbort reads an abort for prepareChain's START gate
// SPECIFICALLY — the sibling gate the fail-loud row deliberately excludes. The
// two are read apart rather than matched on the word "container", because a
// generic substring is satisfied by either and that confusion has already cost
// this feature one falsely-green row.
func j002200AssertStartGateAbort(w *World) error {
	out := w.env.LastOutput()
	if code := w.env.LastExitCode(); code != 3 {
		return fmt.Errorf("expected exit 3 (fatal isolation finding), got %d; output:\n%s", code, out)
	}
	if !strings.Contains(out, j002200StartGateFinding) {
		return fmt.Errorf("output does not name the container START gate (%q) — the abort came from somewhere else; output:\n%s", j002200StartGateFinding, out)
	}
	if strings.Contains(out, j002200RuntimeGateFinding) {
		return fmt.Errorf("the abort came from the RUNTIME-unreachable gate (%q), not the START gate this row asserts; output:\n%s", j002200RuntimeGateFinding, out)
	}
	class := "[" + string(strictness.ClassIsolation) + "]"
	if !strings.Contains(out, class) {
		return fmt.Errorf("output does not classify the abort as %s; output:\n%s", class, out)
	}
	return nil
}
