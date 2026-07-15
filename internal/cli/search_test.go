package cli

import (
	"reflect"
	"testing"
)

func TestSearchScopes(t *testing.T) {
	tests := []struct {
		name                  string
		localOnly, remoteOnly bool
		wantLocal, wantRemote bool
	}{
		{"neither flag searches both", false, false, true, true},
		{"local only", true, false, true, false},
		{"remote only", false, true, false, true},
		{"both flags searches both", true, true, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotLocal, gotRemote := searchScopes(tt.localOnly, tt.remoteOnly)
			if gotLocal != tt.wantLocal || gotRemote != tt.wantRemote {
				t.Errorf("searchScopes(%v, %v) = (%v, %v), want (%v, %v)",
					tt.localOnly, tt.remoteOnly, gotLocal, gotRemote, tt.wantLocal, tt.wantRemote)
			}
		})
	}
}

func TestResolveSearchTypes(t *testing.T) {
	tests := []struct {
		name       string
		itemType   string
		wantLocal  []string
		wantRemote []string
		wantErr    bool
	}{
		{"empty searches all", "", []string{"fragment", "command", "profile", "mcp_server"}, []string{"bundle"}, false},
		{"fragment is local-only", "fragment", []string{"fragment"}, nil, false},
		{"prompt is local-only", "command", []string{"command"}, nil, false},
		{"mcp_server is local-only", "mcp_server", []string{"mcp_server"}, nil, false},
		{"profile is local-only", "profile", []string{"profile"}, nil, false},
		{"bundle is remote-only", "bundle", nil, []string{"bundle"}, false},
		{"unknown type errors", "widget", nil, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotLocal, gotRemote, err := resolveSearchTypes(tt.itemType)
			if (err != nil) != tt.wantErr {
				t.Fatalf("resolveSearchTypes(%q) err = %v, wantErr %v", tt.itemType, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if !reflect.DeepEqual(gotLocal, tt.wantLocal) {
				t.Errorf("local = %v, want %v", gotLocal, tt.wantLocal)
			}
			if !reflect.DeepEqual(gotRemote, tt.wantRemote) {
				t.Errorf("remote = %v, want %v", gotRemote, tt.wantRemote)
			}
		})
	}
}
