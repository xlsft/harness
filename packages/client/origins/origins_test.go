package origins

import "testing"

func TestMatcher(t *testing.T) {
	m := New([]string{"http://localhost:3210/", "vscode-webview://*"})
	for origin, want := range map[string]bool{
		"http://localhost:3210":     true,
		"HTTP://LOCALHOST:3210":     true,
		"http://localhost:3211":     false,
		"https://evil.example":      false,
		"vscode-webview://abc123":   true,
		"vscode-webview://":         false,
		"vscode-webview":            false,
		"null":                      false,
		"evil-vscode-webview://abc": false,
	} {
		if got := m.Allowed(origin); got != want {
			t.Errorf("Allowed(%q) = %v, want %v", origin, got, want)
		}
	}
}

func TestValidateRefusesWebWildcards(t *testing.T) {
	for _, bad := range []string{"http://*", "HTTPS://*", "*://*", "://*", "http://*.example.com", "file://*"} {
		if Validate([]string{bad}) == nil {
			t.Errorf("Validate(%q) accepted it", bad)
		}
		if m := New([]string{bad}); m.Allowed("http://evil.example") || m.Allowed("https://evil.example") {
			t.Errorf("New(%q) admits a web page", bad)
		}
	}
	if err := Validate([]string{"vscode-webview://*", "http://127.0.0.1:3210", "tauri://localhost"}); err != nil {
		t.Error(err)
	}
}
