package layerscope

import "testing"

func TestLayer_String(t *testing.T) {
	tests := []struct {
		l    Layer
		want string
	}{
		{LayerHome, "home"},
		{LayerProject, "project"},
		{LayerEnv, "env"},
		{LayerFlag, "flag"},
		{Layer(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.l.String(); got != tt.want {
			t.Errorf("Layer(%d).String() = %q, want %q", tt.l, got, tt.want)
		}
	}
}

func TestLayer_File(t *testing.T) {
	tests := []struct {
		name        string
		l           Layer
		appPath     string
		homeAppPath string
		want        string
	}{
		{"home resolves against homeAppPath", LayerHome, "/proj/.ctxloom", "/home/u/.ctxloom", "/home/u/.ctxloom/config.yaml"},
		{"home with no resolvable home path is empty", LayerHome, "/proj/.ctxloom", "", ""},
		{"project resolves against appPath", LayerProject, "/proj/.ctxloom", "/home/u/.ctxloom", "/proj/.ctxloom/config.yaml"},
		{"env has no file", LayerEnv, "/proj/.ctxloom", "/home/u/.ctxloom", ""},
		{"flag has no file", LayerFlag, "/proj/.ctxloom", "/home/u/.ctxloom", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.l.File(tt.appPath, tt.homeAppPath); got != tt.want {
				t.Errorf("File(%q, %q) = %q, want %q", tt.appPath, tt.homeAppPath, got, tt.want)
			}
		})
	}
}
