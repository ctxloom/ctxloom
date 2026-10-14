package interaction

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/ctxloom/ctxloom/internal/adapters/runner"
)

// maxMailRequest bounds a mail-peek request body: an op, a cap, a lease and
// a few ids.
const maxMailRequest = 64 << 10

// mailHandler serves the mail-peek hook's POST (runner.MailPath): a fetch
// leases what waits for the engine (Home.PeekMail), a confirm delivers what
// the hook wrote (Home.ConfirmMail). The runner stays the spool's one reader;
// this is only the hook's way to ask it.
func mailHandler(home *runner.Home) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "the mail peek POSTs", http.StatusMethodNotAllowed)
			return
		}
		raw, err := io.ReadAll(io.LimitReader(r.Body, maxMailRequest+1))
		if err != nil || len(raw) > maxMailRequest {
			http.Error(w, "unreadable or oversized mail-peek request", http.StatusBadRequest)
			return
		}
		var req runner.MailPeekRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			http.Error(w, fmt.Sprintf("decode the mail-peek request: %v", err), http.StatusBadRequest)
			return
		}
		var answer any
		switch req.Op {
		case runner.MailOpFetch:
			if req.MaxChars <= 0 {
				http.Error(w, "a fetch names the context it may fill (max_chars)", http.StatusBadRequest)
				return
			}
			answer = home.PeekMail(req.MaxChars)
		case runner.MailOpConfirm:
			answer = runner.MailConfirmed{Confirmed: home.ConfirmMail(req.Lease, req.IDs)}
		default:
			http.Error(w, fmt.Sprintf("unknown mail-peek op %q", req.Op), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(answer)
	})
}
