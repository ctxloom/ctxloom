package discover

import (
	"bytes"
	"fmt"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
)

// The credential must never surface through ANY generic formatting path.
// Endpoint's String() has a value receiver, so it is in the method set of
// both Endpoint and *Endpoint — but that is exactly the kind of thing that
// silently stops being true under a refactor, so both forms are asserted,
// along with an Endpoint reached as a slice element.
func TestEndpoint_Format_RedactsCredKeepsURL(t *testing.T) {
	const url = "http://127.0.0.1:54321/mcp"
	const cred = "tok-secret-do-not-print"
	ep := Endpoint{URL: url, Cred: cred}

	cases := map[string]string{
		"value %v":    fmt.Sprintf("%v", ep),
		"value %+v":   fmt.Sprintf("%+v", ep),
		"value %#v":   fmt.Sprintf("%#v", ep),
		"pointer %v":  fmt.Sprintf("%v", &ep),
		"pointer %+v": fmt.Sprintf("%+v", &ep),
		"pointer %#v": fmt.Sprintf("%#v", &ep),
		"slice %v":    fmt.Sprintf("%v", []Endpoint{ep}),
		"slice %+v":   fmt.Sprintf("%+v", []Endpoint{ep}),
		"Sprint":      fmt.Sprint(ep),
	}
	for name, got := range cases {
		t.Run(name, func(t *testing.T) {
			assert.NotContains(t, got, cred, "credential leaked through %s: %q", name, got)
			assert.Contains(t, got, url, "URL must stay legible through %s: %q", name, got)
		})
	}
}

func TestEndpoint_LogValue_RedactsCredKeepsURL(t *testing.T) {
	const url = "http://127.0.0.1:54321/mcp"
	const cred = "tok-secret-do-not-print"
	ep := Endpoint{URL: url, Cred: cred}

	for name, v := range map[string]any{"value": ep, "pointer": &ep} {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			slog.New(slog.NewTextHandler(&buf, nil)).Info("dial", "endpoint", v)
			got := buf.String()
			assert.NotContains(t, got, cred, "credential leaked through slog: %q", got)
			assert.Contains(t, got, url, "URL must stay legible through slog: %q", got)
		})
	}
}
