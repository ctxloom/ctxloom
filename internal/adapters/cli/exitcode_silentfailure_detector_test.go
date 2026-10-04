package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"testing"
)

func parseFixture(t *testing.T, src string) map[string]*ast.File {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "fixture.go", "package cli\n"+src, 0)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	return map[string]*ast.File{"fixture.go": f}
}

// The detector keys on STRUCTURE — a function ranges over an `.Errors` slice
// and has no error return conditioned on it — never on how the loop prints.
// Every case below that warns and exits 0 must be found whatever writer it
// warns through; every case that propagates must not.
func TestSilentFailureSites_DetectsByStructureNotPrintSpelling(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{"clidiag warn, return nil", `
func run(result R) error {
	for _, e := range result.Errors {
		clidiag.Warn("p", "%s", e)
	}
	return nil
}`, []string{"fixture.go:run"}},
		{"a writer the gate never heard of", `
func run(result R) error {
	for _, e := range result.Errors {
		someLogger.Emit(e)
	}
	return nil
}`, []string{"fixture.go:run"}},
		{"index-only loop", `
func run(result R) error {
	for i := range result.Errors {
		fmt.Println(i)
	}
	return nil
}`, []string{"fixture.go:run"}},
		{"conditioned return that is nil", `
func run(result R) error {
	for _, e := range result.Errors {
		warn(e)
	}
	if len(result.Errors) > 0 {
		return nil
	}
	return nil
}`, []string{"fixture.go:run"}},
		{"error returned only from a closure", `
func run(result R) error {
	return emit(func() error {
		for _, e := range result.Errors {
			warn(e)
		}
		return clidiag.WarnErrors("p", result.Errors)
	})
}`, []string{"fixture.go:run"}},
		{"method that does not check", `
func run(result R) error {
	for _, e := range result.Errors {
		warn(e)
	}
	return result.done()
}
func (r R) done() error { return errors.New("x") }`, []string{"fixture.go:run"}},
		{"returns WarnErrors over the slice", `
func run(result R) error {
	for _, e := range result.Errors {
		warn(e)
	}
	return clidiag.WarnErrors("p", result.Errors)
}`, nil},
		{"conditioned non-nil return, nested", `
func run(result R) error {
	for _, e := range result.Errors {
		warn(e)
	}
	if result.Count == 0 {
		if len(result.Errors) > 0 {
			return fmt.Errorf("failed")
		}
		return nil
	}
	return nil
}`, nil},
		{"loop inside a closure, check in the function", `
func run(result R) error {
	if err := emit(func() {
		for _, e := range result.Errors {
			warn(e)
		}
	}); err != nil {
		return err
	}
	return result.exitErr(3)
}
func (r *R) exitErr(n int) error {
	if len(r.Errors) > 0 {
		return fmt.Errorf("%d failed", len(r.Errors))
	}
	return nil
}`, nil},
		{"loop returns the error", `
func run(result R) error {
	for _, e := range result.Errors {
		return e
	}
	return nil
}`, nil},
		{"two-value return with a nil error", `
func run(result R) (int, error) {
	for _, e := range result.Errors {
		warn(e)
	}
	if len(result.Errors) > 0 {
		return len(result.Errors), nil
	}
	return 0, nil
}`, []string{"fixture.go:run"}},
		{"no Errors loop at all", `
func run(result R) error {
	for _, f := range result.Files {
		warn(f)
	}
	return nil
}`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := silentFailureSites(parseFixture(t, tc.src))
			if !slices.Equal(got, tc.want) {
				t.Fatalf("sites = %q, want %q", got, tc.want)
			}
		})
	}
}
