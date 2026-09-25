// Package docplant is the doc-comment rule's test-file fixture. Its plants are
// in _test.go files; a production plant lives in docprod, because a package
// with tests is analyzed twice and analysistest expects a want in every pass
// that repeats the file.
package docplant

// Fine is documented once.
func Fine() {}
