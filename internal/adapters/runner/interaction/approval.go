package interaction

import (
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/ctxloom/ctxloom/internal/adapters/runner"
)

// errNoApprovalRoute is the approval hook's refusal in a run that routes no
// approvals: nobody is asked.
var errNoApprovalRoute = errors.New("approval hook: this run routes no approvals to a human")

// maxHookPayload bounds an approval hook's body: an ask carries one tool
// call's input.
const maxHookPayload = 8 << 20

// hookHandler serves the approval hook's POST: the engine's native payload
// for the event the query names, answered with the engine's native
// decision. Anything else is an error status, which the hook command turns
// into no decision at all — and an engine nobody sits at denies a call no
// hook decided (the approval route's fail-closed rule).
func hookHandler(home *runner.Home) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "the approval hook POSTs", http.StatusMethodNotAllowed)
			return
		}
		route := home.ApprovalRoute()
		if route == nil {
			http.Error(w, errNoApprovalRoute.Error(), http.StatusNotFound)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, maxHookPayload+1))
		if err != nil {
			http.Error(w, fmt.Sprintf("read the hook payload: %v", err), http.StatusBadRequest)
			return
		}
		if len(body) > maxHookPayload {
			http.Error(w, "the hook payload is larger than an ask can be", http.StatusRequestEntityTooLarge)
			return
		}
		out, err := route.Hook(r.Context(), r.URL.Query().Get(runner.HookEventParam), body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(out)
	})
}
