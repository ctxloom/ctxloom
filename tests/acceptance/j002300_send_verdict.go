// Package acceptance: j002300's agent_send cell — the verdict half.
//
// UNTAGGED, like probe_p6_steer_echo.go, so plain `go test ./...` runs the
// check below with no built binary, no engine and no paid turn; the @live
// cell that uses it needs all three.
package acceptance

import (
	"encoding/json"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/spool"
)

// j002300SendScan is what the coordinator's mail from one child holds, for
// the cell that passes only when the child's marker arrives in a body the
// child SENT with agent_send.
//
// Two paths carry a child's words to its coordinator's spool, and both are
// messages from the child's harp: the child's own agent_send, and its
// runner's automatic turn report, which carries whatever the model said
// (runner.Home.ReportTurnResult). A model that only SAYS the marker therefore
// lands it in the mailbox without calling any tool. The runner marks the
// report as automatic in its structured companion (coord.AutoReportStructured),
// and that marking — not the kind, which an agent_send caller picks — is what
// tells the paths apart.
type j002300SendScan struct {
	// hit is the first unmarked body from the child containing the marker:
	// the child's own send.
	hit string
	// autoReport is the first automatic report from the child containing
	// the marker: the model said it rather than sending it.
	autoReport string
	// seen is every body from the child; failed the bodies of its error-kind
	// messages.
	seen, failed []string
}

// j002300ScanSends scans mail from harp for want.
func j002300ScanSends(mail []*spool.Message, harp, want string) j002300SendScan {
	var s j002300SendScan
	for _, m := range mail {
		if m.FromHarp != harp {
			continue
		}
		s.seen = append(s.seen, m.Body)
		auto := j002300IsAutoReport(m)
		switch {
		case strings.Contains(m.Body, want) && !auto:
			s.hit = m.Body
			return s
		case strings.Contains(m.Body, want) && s.autoReport == "":
			s.autoReport = m.Body
		case m.Kind == coord.KindError:
			s.failed = append(s.failed, m.Body)
		}
	}
	return s
}

// j002300IsAutoReport is coord.IsAutoReport on a spool message's structured
// companion, which the spool holds as the parsed frontmatter map.
func j002300IsAutoReport(m *spool.Message) bool {
	if len(m.Structured) == 0 {
		return false
	}
	raw, err := json.Marshal(m.Structured)
	if err != nil {
		return false
	}
	return coord.IsAutoReport(raw)
}
