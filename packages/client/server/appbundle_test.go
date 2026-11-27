package server

import "testing"

func TestAppBundleOf(t *testing.T) {
	for exe, want := range map[string]string{
		"/Applications/xlsft`s harness.app/Contents/MacOS/harness-client": "/Applications/xlsft`s harness.app",
		"/Applications/H.app/Contents/Helpers/Other.app/Contents/MacOS/x": "/Applications/H.app",
		"/Users/me/Projects/harness/.dev/harness-client":                  "",
	} {
		if got := appBundleOf(exe); got != want {
			t.Errorf("appBundleOf(%q) = %q, want %q", exe, got, want)
		}
	}
}
