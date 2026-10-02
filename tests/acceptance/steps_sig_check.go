//go:build acceptance

// The --disable-sig-check owner rulings of 2026-10-02 (sig_check.feature):
//
//   - an installed tree pulled SIGNED and then edited locally is accepted under
//     the switch — the flag then also hides tampering, so doctor and the dry
//     run name every edited tree it accepted;
//   - the session's own ctxloom children share its waiver (the launch hands the
//     engine bundles.SessionSigCheckEnv, observed on the mock's recorded engine
//     env), while an invocation started from the session's shell — an agent
//     launched by hand — does not.
//
// The edited-tree scenario reuses steps_j001500.go's company-signed tree.

package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/cucumber/godog"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
)

// sigCheckEditedMarker is what Alice's local edit writes into the installed
// signed tree; it exists nowhere else.
const sigCheckEditedMarker = "SIGCHECK-LOCALLY-EDITED-GUIDANCE-MARKER"

// sigCheckInstalledGuidance finds the installed copy of the company bundle's
// guidance file under the project's .ctxloom — wherever `deps pull` laid the
// tree out — so the edit lands on the bytes the reader verifies.
func sigCheckInstalledGuidance(w *World) (string, error) {
	suffix := filepath.FromSlash(treeBundlePath(j001500Of(w).bundleName) + "/fragments/guidance.md")
	var found string
	err := filepath.WalkDir(filepath.Join(w.env.ProjectDir, ".ctxloom"), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(p, suffix) {
			found = p
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", fmt.Errorf("no installed %s under the project's .ctxloom — did the pull install the tree?", suffix)
	}
	return found, nil
}

// sigCheckDelivered reads the delivered context; a session the strict gate
// aborted delivered nothing, which is "not received".
func sigCheckDelivered(w *World) string {
	body, err := w.env.ReadFile(filepath.Join("out", "CLAUDE.md"))
	if err != nil {
		return ""
	}
	w.docStepMaterialized = body
	return body
}

func registerSigCheckSteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^Alice edits her installed copy of the company's guidance$`, func(c context.Context) error {
		w := worldFrom(c)
		p, err := sigCheckInstalledGuidance(w)
		if err != nil {
			return err
		}
		return os.WriteFile(p, []byte(sigCheckEditedMarker+"\n"), 0o644)
	})

	ctx.Step(`^her assistant (receives|does not receive) her edited guidance$`, func(c context.Context, verb string) error {
		body := sigCheckDelivered(worldFrom(c))
		has := strings.Contains(body, sigCheckEditedMarker)
		if want := verb == "receives"; has != want {
			return fmt.Errorf("edited guidance delivered=%v, want %v; delivered context:\n%s\nlast output:\n%s", has, want, body, worldFrom(c).env.LastOutput())
		}
		return nil
	})

	ctx.Step(`^doctor, with signature verification disabled, names the company's bundle as accepted although edited$`, func(c context.Context) error {
		w := worldFrom(c)
		_ = w.env.Run("--"+bundles.SigCheckFlag, "--format", "json", "doctor")
		var doc struct {
			Checks []struct {
				Marker string `json:"marker"`
				Status string `json:"status"`
				Detail string `json:"detail"`
			} `json:"checks"`
		}
		out := w.env.LastStdout()
		if err := json.Unmarshal([]byte(out), &doc); err != nil {
			return fmt.Errorf("doctor JSON: %w; output:\n%s", err, w.env.LastOutput())
		}
		for _, ch := range doc.Checks {
			if ch.Marker != "DOCTOR-CHECK-SIG-CHECK-e2" {
				continue
			}
			w.docStepMaterialized = ch.Detail
			if ch.Status != "warn" || !strings.Contains(ch.Detail, bundles.EditedSignedTreeWords) || !strings.Contains(ch.Detail, j001500Of(w).bundleName) {
				return fmt.Errorf("doctor's sig-check row does not name the edited bundle %q: status %q, detail %q", j001500Of(w).bundleName, ch.Status, ch.Detail)
			}
			return nil
		}
		return fmt.Errorf("doctor reported no sig-check row; output:\n%s", out)
	})

	ctx.Step(`^the dry run, with signature verification disabled, names the company's bundle as accepted although edited$`, func(c context.Context) error {
		w := worldFrom(c)
		_ = w.env.Run("--"+bundles.SigCheckFlag, "run", "--dry-run", "--format", "json", "--profile", "default", "hello")
		var doc struct {
			SignatureCheck    string   `json:"signature_check"`
			EditedSignedTrees []string `json:"edited_signed_trees"`
		}
		if err := json.Unmarshal([]byte(w.env.LastStdout()), &doc); err != nil {
			return fmt.Errorf("dry-run JSON: %w; output:\n%s", err, w.env.LastOutput())
		}
		name := j001500Of(w).bundleName
		if doc.SignatureCheck != "disabled" || !slices.ContainsFunc(doc.EditedSignedTrees, func(r string) bool { return strings.Contains(r, name) }) {
			return fmt.Errorf("dry run does not name the edited bundle %q: signature_check %q, edited_signed_trees %v", name, doc.SignatureCheck, doc.EditedSignedTrees)
		}
		return nil
	})
}
