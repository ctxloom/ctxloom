// Package bindplant writes a session binding outside the admitted callers.
package bindplant

type manager struct{}

func (manager) BindSession(string) {}

// Rebind binds from an unreviewed call site.
func Rebind(m manager) {
	m.BindSession("harp") // want `internal/bindplant/bindplant.go calls something named BindSession but is not in bindSessionAllowedCallers`
}
