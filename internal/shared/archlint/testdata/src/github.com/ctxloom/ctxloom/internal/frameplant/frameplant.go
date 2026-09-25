package frameplant // want `internal/frameplant/frameplant.go constructs a <ctxloom-reminder> frame by hand`

// Notice builds a frame by hand.
func Notice(body string) string {
	return "<ctxloom-reminder" + ">" + body
}
