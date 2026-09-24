package lockwait

// WaitNotice exposes the stderr line to the external test package, so the
// test compares the exact line the watchdog prints rather than a substring.
var WaitNotice = waitNotice
