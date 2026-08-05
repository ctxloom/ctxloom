package layerscope

import "testing"

// TestScope_Allows_Matrix pins the exact permission matrix the design doc
// specifies: Shared keeps project+flag; Machine keeps home+env+flag;
// Preference keeps all four; Invocation keeps env+flag; Never keeps none.
// Every cell is asserted explicitly (not just "at least one true/false") so a
// single flipped bit in Allows fails exactly one row, naming which layer×scope
// pair regressed.
func TestScope_Allows_Matrix(t *testing.T) {
	tests := []struct {
		scope Scope
		layer Layer
		want  bool
	}{
		{ScopeShared, LayerHome, false},
		{ScopeShared, LayerProject, true},
		{ScopeShared, LayerEnv, false},
		{ScopeShared, LayerFlag, true},

		{ScopeMachine, LayerHome, true},
		{ScopeMachine, LayerProject, false},
		{ScopeMachine, LayerEnv, true},
		{ScopeMachine, LayerFlag, true},

		{ScopePreference, LayerHome, true},
		{ScopePreference, LayerProject, true},
		{ScopePreference, LayerEnv, true},
		{ScopePreference, LayerFlag, true},

		{ScopeInvocation, LayerHome, false},
		{ScopeInvocation, LayerProject, false},
		{ScopeInvocation, LayerEnv, true},
		{ScopeInvocation, LayerFlag, true},

		{ScopeNever, LayerHome, false},
		{ScopeNever, LayerProject, false},
		{ScopeNever, LayerEnv, false},
		{ScopeNever, LayerFlag, false},
	}
	for _, tt := range tests {
		if got := tt.scope.Allows(tt.layer); got != tt.want {
			t.Errorf("%s.Allows(%s) = %v, want %v", tt.scope, tt.layer, got, tt.want)
		}
	}
}

func TestScope_Why_NonEmptyAndDistinctPerScope(t *testing.T) {
	scopes := []Scope{ScopeShared, ScopeMachine, ScopePreference, ScopeInvocation, ScopeNever}
	seen := map[string]Scope{}
	for _, s := range scopes {
		why := s.Why()
		if why == "" {
			t.Errorf("%s.Why() is empty", s)
		}
		if prior, dup := seen[why]; dup {
			t.Errorf("%s.Why() collides with %s.Why(): %q", s, prior, why)
		}
		seen[why] = s
	}
}

func TestScope_String(t *testing.T) {
	tests := []struct {
		s    Scope
		want string
	}{
		{ScopeShared, "shared"},
		{ScopeMachine, "machine"},
		{ScopePreference, "preference"},
		{ScopeInvocation, "invocation"},
		{ScopeNever, "never"},
		{Scope(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.s.String(); got != tt.want {
			t.Errorf("Scope(%d).String() = %q, want %q", tt.s, got, tt.want)
		}
	}
}
