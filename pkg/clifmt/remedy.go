package clifmt

// Remedier is the structural contract clifmt reads a fix from: any value
// that can name the one-line remedy for what it reports. clifmt ships as a
// standalone library and imports nothing of its callers, so it recognises a
// remedy by method set alone — the same way net.Error's Timeout() is read.
type Remedier interface{ Remedy() string }

// RemedyOf walks err's tree in errors.As order (Unwrap() error and
// Unwrap() []error, depth first, outermost first) and returns the first
// NON-EMPTY Remedy(). An empty Remedy() does not stop the walk: a wrapper
// with nothing to add must not hide the fix its cause names.
func RemedyOf(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	if r, ok := err.(Remedier); ok {
		if fix := r.Remedy(); fix != "" {
			return fix, true
		}
	}
	switch u := err.(type) {
	case interface{ Unwrap() error }:
		return RemedyOf(u.Unwrap())
	case interface{ Unwrap() []error }:
		for _, e := range u.Unwrap() {
			if fix, ok := RemedyOf(e); ok {
				return fix, true
			}
		}
	}
	return "", false
}

// FixLine is the ONE human text form of a remedy: "" when remedy is "",
// otherwise "\n"+indent+"fix: "+remedy. Every text listing appends it after
// the line it remedies, so the fix always reads the same wherever it shows.
// ErrorEnvelope's `label:"fix"` renders the identical line through the
// reflective text path; TestRenderError_TextFixMatchesFixLine binds the two.
func FixLine(indent, remedy string) string {
	if remedy == "" {
		return ""
	}
	return "\n" + indent + "fix: " + remedy
}

// remedyOf is RemedyOf without the found flag, for envelope construction
// where an absent remedy is simply the empty (omitted) field.
func remedyOf(err error) string {
	fix, _ := RemedyOf(err)
	return fix
}
