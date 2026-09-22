package wire

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHookEvents_EveryUnifiedHooksFieldIsAnEvent turns the drift
// UnifiedHooks.Append warns about into a failing test: HookEvents, Event and
// SetEvent enumerate the events by hand, and a field added to UnifiedHooks
// and not added there would not fail to compile — it would silently never
// be assembled, never reported and never written.
func TestHookEvents_EveryUnifiedHooksFieldIsAnEvent(t *testing.T) {
	typ := reflect.TypeOf(UnifiedHooks{})
	var fields []string
	for i := 0; i < typ.NumField(); i++ {
		fields = append(fields, strings.Split(typ.Field(i).Tag.Get("yaml"), ",")[0])
	}
	events := HookEvents()
	sortedFields := append([]string(nil), fields...)
	sortedEvents := append([]string(nil), events...)
	sort.Strings(sortedFields)
	sort.Strings(sortedEvents)
	require.Equal(t, sortedEvents, sortedFields,
		"every UnifiedHooks field must be in HookEvents(), or it is assembled by nothing and reported by nobody")

	// And the accessors must actually reach each one, in both directions.
	for _, event := range events {
		var u UnifiedHooks
		marker := []Hook{{Command: "marker-" + event}}
		u.SetEvent(event, marker)
		assert.Equal(t, marker, u.Event(event), "accessors must round-trip %q", event)
		assert.True(t, IsHookEvent(event))
	}
	assert.Nil(t, UnifiedHooks{}.Event("not-an-event"))
	assert.False(t, IsHookEvent("not-an-event"))
}
