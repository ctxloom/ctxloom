//go:build acceptance

// J24: "an engine asked for a container actually lands in one"
// (j24_container.feature) — the positive half of the runtime axis.
//
// j22_isolation.feature owns the NEGATIVE half (a requested container that
// cannot launch aborts, or degrades under --degraded) and self-skips on a
// machine where a container CAN launch, because asserting an abort there would
// be asserting a bug. That leaves the positive claim with no executable
// evidence on exactly the machines that can produce it — this file is that
// evidence.
//
// The fixture is J22's, reused rather than copied: the same two agent bindings
// ("mock" on the host, "mock-container" with runtime: container) over the same
// mock LLM label. Only the record file's LOCATION differs and it has to: J22
// writes records beside the project, while a containerized engine can only
// write somewhere the container can see, so J24's records live INSIDE the
// workspace, which is mounted into the container at the same absolute path.
package acceptance

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"

	"github.com/ctxloom/ctxloom/internal/testsupport/containercell"
)

// j24Record is one run's evidence of WHERE the engine executed, read from the
// record file the mock backend writes (internal/lm/backends' writeMockRecord).
//
// Both fields are needed and neither is sufficient. Markers is a heuristic
// that reads non-empty on BOTH legs when the harness itself runs inside a
// devcontainer, so a scenario trusting it alone would pass with no container
// launched. Hostname breaks that tie — a container gets its own UTS namespace
// — but on its own it only says "a different name", not "a container". Read
// together, and compared against a host leg run on this same machine, they say
// what the journey claims.
//
// cwd and workdir deliberately play no part: the container mounts the project
// at the SAME absolute path by design, so both are byte-identical across the
// two legs (measured 2026-08-06) and would make a vacuous assertion.
type j24Record struct {
	Hostname string
	Markers  string
}

type j24State struct {
	last j24Record
}

func j24Of(w *World) *j24State {
	if w.j24 == nil {
		w.j24 = &j24State{}
	}
	return w.j24
}

// j24ParseRecord reads the two where-did-this-run lines out of a record body.
// A field the record does not carry stays empty, which every assertion below
// treats as a failure rather than a pass — a record missing its hostname line
// means the writer changed and the evidence is gone, not that the run was fine.
func j24ParseRecord(body string) j24Record {
	var rec j24Record
	for _, line := range strings.Split(body, "\n") {
		switch {
		case strings.HasPrefix(line, "hostname="):
			rec.Hostname = strings.TrimPrefix(line, "hostname=")
		case strings.HasPrefix(line, "container_markers="):
			rec.Markers = strings.TrimPrefix(line, "container_markers=")
		}
	}
	return rec
}

// j24RecordPath is this run's record file, inside the project workspace so a
// containerized engine's write reaches the host through the workspace mount. A
// fresh name per runtime keeps the two legs from reading each other's evidence
// — the failure that would make the differential comparison meaningless.
func j24RecordPath(w *World, runtime string) string {
	return filepath.Join(w.env.ProjectDir, "j24-record-"+runtime+".txt")
}

func registerJ24Steps(ctx *godog.ScenarioContext) {
	// Runs the mock agent once under the named runtime, leaving its record in
	// the workspace. The container leg SKIPS (rather than fails) where no
	// runtime is reachable: this scenario's claim is about what happens when
	// one IS present, and a machine without docker has nothing to say about it.
	ctx.Step(`^Alice runs the mock agent with runtime "([^"]*)"$`, func(c context.Context, runtime string) error {
		w := worldFrom(c)
		j24 := j24Of(w)

		agentName := "mock"
		if runtime == "container" {
			agentName = "mock-container"
			if rt, _, msg := containercell.Select(c, "J24's containerized-run row"); !rt.Available {
				fmt.Printf("SKIPPED (J24 container row): no container runtime reachable here (%s)\n", msg)
				return godog.ErrSkip
			}
		}

		if err := w.env.InitGitRepo(); err != nil {
			return err
		}
		// J22's fixture verbatim — same labels, same two agent bindings. See
		// this file's package doc for why the record path is the one thing
		// that differs.
		if err := w.env.WriteFile(".ctxloom/config.yaml", j22ConfigYAML()); err != nil {
			return err
		}
		recPath := j24RecordPath(w, runtime)
		if err := os.Remove(recPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("clear stale record %s: %w", recPath, err)
		}
		if err := w.env.WriteHomeFile(".ctxloom/config.yaml", j22HomeConfigYAML(recPath)); err != nil {
			return err
		}

		_ = w.env.Run("run", "--agent", agentName, "--print", "where-did-i-run")
		if code := w.env.LastExitCode(); code != 0 {
			return fmt.Errorf("run --agent %s exited %d; output:\n%s", agentName, code, w.env.LastOutput())
		}

		body, err := os.ReadFile(recPath)
		if err != nil {
			// The characteristic silent no-op: exit 0, a plausible echo, and
			// no payload. Naming it here is the whole reason this step reads a
			// file instead of trusting the exit code.
			return fmt.Errorf("the %s run exited 0 but left no record at %s — the engine either never ran or its write never reached the host: %w", runtime, recPath, err)
		}
		j24.last = j24ParseRecord(string(body))
		w.docStepMaterialized = fmt.Sprintf("hostname=%s container_markers=%s", j24.last.Hostname, j24.last.Markers)
		return nil
	})

	// The BASELINE leg. It exists to establish what this machine looks like
	// from inside the engine, so the container leg can claim a difference
	// rather than an absolute — and it is asserted, not merely performed,
	// because a baseline nobody checks cannot be relied on later.
	ctx.Step(`^the engine left its record, and it ran on this host$`, func(c context.Context) error {
		w := worldFrom(c)
		rec := j24Of(w).last
		host, err := os.Hostname()
		if err != nil {
			return fmt.Errorf("read this host's name: %w", err)
		}
		if rec.Hostname != host {
			return fmt.Errorf("a host run must report this machine's own name: record says %q, this host is %q", rec.Hostname, host)
		}
		return nil
	})

	ctx.Step(`^the engine left its record, and it ran inside a container, not on this host$`, func(c context.Context) error {
		w := worldFrom(c)
		rec := j24Of(w).last
		host, err := os.Hostname()
		if err != nil {
			return fmt.Errorf("read this host's name: %w", err)
		}
		// The differential claim: a container gets its own UTS namespace, so
		// the engine cannot report the launching machine's name. This is the
		// assertion that survives a harness running inside a devcontainer,
		// where the marker check below is true on both legs.
		if rec.Hostname == "" {
			return fmt.Errorf("the record carries no hostname at all, so it cannot say where the engine ran")
		}
		if rec.Hostname == host {
			return fmt.Errorf("the engine reported this host's own name (%q), so it did not run in a container — an explicitly requested container silently ran on the host", host)
		}
		// And the positive identification: somewhere-else is not enough on its
		// own, since a differing name is also what a remote or a renamed host
		// would produce. The markers say it is a container.
		if rec.Markers == "" {
			return fmt.Errorf("the engine ran somewhere else (hostname %q, this host is %q) but reported no container markers, so what it ran in is unidentified", rec.Hostname, host)
		}
		return nil
	})
}
