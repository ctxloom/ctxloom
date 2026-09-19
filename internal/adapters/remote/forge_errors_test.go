package remote

import (
	"errors"
	"net/http"
	"testing"

	"github.com/google/go-github/v60/github"
	"github.com/stretchr/testify/assert"
)

// TestIs401Error pins the mushy-unvented-cake typed-error detection: a 401 must
// be recognized from a real go-github *github.ErrorResponse (or a github.Response
// carrying StatusCode 401), via errors.As / status code — never by matching
// "401" in the message text. A 404 or nil must not be mistaken for a 401.
func TestIs401Error(t *testing.T) {
	resp401 := &github.Response{Response: &http.Response{StatusCode: http.StatusUnauthorized}}
	resp404 := &github.Response{Response: &http.Response{StatusCode: http.StatusNotFound}}

	errResp401 := &github.ErrorResponse{
		Response: &http.Response{StatusCode: http.StatusUnauthorized},
		Message:  "Bad credentials",
	}
	errResp404 := &github.ErrorResponse{
		Response: &http.Response{StatusCode: http.StatusNotFound},
		Message:  "Not Found",
	}

	tests := []struct {
		name string
		resp *github.Response
		err  error
		want bool
	}{
		{"response carrying 401", resp401, nil, true},
		{"typed ErrorResponse 401", nil, errResp401, true},
		{"typed ErrorResponse 401 wrapped", nil, errors.New("retry: " + errResp401.Error()), false}, // text-only, not typed
		{"wrapped typed 401 via %w", nil, wrap(errResp401), true},
		{"response carrying 404", resp404, nil, false},
		{"typed ErrorResponse 404", nil, errResp404, false},
		{"plain error", nil, errors.New("boom"), false},
		{"nothing", nil, nil, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, is401Error(tc.resp, tc.err))
		})
	}
}

// wrap returns err wrapped so errors.As still unwraps to the typed value,
// proving is401Error matches the sentinel through wrapping rather than text.
func wrap(err error) error { return wrappedErr{err} }

type wrappedErr struct{ err error }

func (w wrappedErr) Error() string { return "context: " + w.err.Error() }
func (w wrappedErr) Unwrap() error { return w.err }
